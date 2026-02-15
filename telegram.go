package main

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	maxbot "github.com/max-messenger/max-bot-api-client-go"
	maxschemes "github.com/max-messenger/max-bot-api-client-go/schemes"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func (b *Bridge) listenTelegram(ctx context.Context) {
	var updates tgbotapi.UpdatesChannel

	if b.cfg.WebhookURL != "" {
		whPath := b.tgWebhookPath()
		whURL := strings.TrimRight(b.cfg.WebhookURL, "/") + whPath
		wh, err := tgbotapi.NewWebhook(whURL)
		if err != nil {
			slog.Error("TG webhook config error", "err", err)
			return
		}
		if _, err := b.tgBot.Request(wh); err != nil {
			slog.Error("TG set webhook failed", "err", err)
			return
		}
		updates = b.tgBot.ListenForWebhook(whPath)
		slog.Info("TG webhook mode")
	} else {
		// Удаляем webhook если был, переключаемся на polling
		b.tgBot.Request(tgbotapi.DeleteWebhookConfig{})
		u := tgbotapi.NewUpdate(0)
		u.Timeout = 60
		updates = b.tgBot.GetUpdatesChan(u)
		slog.Info("TG polling mode")
	}

	for {
		select {
		case <-ctx.Done():
			return
		case update, ok := <-updates:
			if !ok {
				slog.Warn("TG updates channel closed")
				return
			}

			// Обработка channel posts (crosspost forwarding only)
			if update.EditedChannelPost != nil {
				b.handleTgEditedChannelPost(ctx, update.EditedChannelPost)
				continue
			}
			if update.ChannelPost != nil {
				b.handleTgChannelPost(ctx, update.ChannelPost)
				continue
			}

			// Обработка edit
			if update.EditedMessage != nil {
				edited := update.EditedMessage
				if edited.From != nil && edited.From.IsBot {
					continue
				}
				maxMsgID, ok := b.repo.LookupMaxMsgID(edited.Chat.ID, edited.MessageID)
				if !ok {
					continue
				}
				prefix := b.repo.HasPrefix("tg", edited.Chat.ID)
				fwd := formatTgMessage(edited, prefix)
				if fwd == "" {
					continue
				}
				maxChatID, linked := b.repo.GetMaxChat(edited.Chat.ID)
				if !linked {
					continue
				}
				m := maxbot.NewMessage().SetChat(maxChatID).SetText(fwd)
				if err := b.maxApi.Messages.EditMessage(ctx, maxMsgID, m); err != nil {
					slog.Error("TG→MAX edit failed", "err", err)
				} else {
					slog.Info("TG→MAX edited", "mid", maxMsgID)
				}
				continue
			}

			// Обработка inline-кнопок (crosspost management)
			if update.CallbackQuery != nil {
				b.handleTgCallback(ctx, update.CallbackQuery)
				continue
			}

			if update.Message == nil {
				continue
			}

			msg := update.Message
			text := strings.TrimSpace(msg.Text)
			slog.Debug("TG msg received", "from", msg.From.FirstName, "chat", msg.Chat.ID)

			if text == "/start" || text == "/help" {
				b.tgBot.Send(tgbotapi.NewMessage(msg.Chat.ID,
					"Бот-мост между Telegram и MAX.\n\n"+
						"Команды (группы):\n"+
						"/bridge — создать ключ для связки чатов\n"+
						"/bridge <ключ> — связать этот чат с MAX-чатом по ключу\n"+
						"/bridge prefix on/off — включить/выключить префикс [TG]/[MAX]\n"+
						"/unbridge — удалить связку\n\n"+
						"Кросспостинг каналов:\n"+
						"1. Перешлите пост из TG-канала в личку TG-бота\n"+
						"2. Бот покажет ID — скопируйте\n"+
						"3. В личке MAX-бота: /crosspost <TG_ID>\n"+
						"4. Перешлите пост из MAX-канала → готово!\n\n"+
						"Управление: перешлите пост из связанного канала → кнопки\n\n"+
						"Как связать группы:\n"+
						"1. Добавьте бота в оба чата\n"+
						"   TG: "+b.cfg.TgBotURL+"\n"+
						"   MAX: "+b.cfg.MaxBotURL+"\n"+
						"2. В MAX сделайте бота админом группы\n"+
						"3. В одном из чатов отправьте /bridge\n"+
						"4. Бот выдаст ключ — отправьте /bridge <ключ> в другом чате\n"+
						"5. Готово!"))
				continue
			}

			// /crosspost в личке TG — показать список связок
			if msg.Chat.Type == "private" && text == "/crosspost" {
				links := b.repo.ListCrossposts()
				if len(links) == 0 {
					b.tgBot.Send(tgbotapi.NewMessage(msg.Chat.ID,
						"Нет активных связок.\n\nНастройка: перешлите пост из TG-канала сюда, затем в MAX-боте /crosspost <ID>"))
				} else {
					for _, l := range links {
						kb := tgCrosspostKeyboard(l.Direction, l.MaxChatID)
						m := tgbotapi.NewMessage(msg.Chat.ID, tgCrosspostStatusText("", l.Direction)+fmt.Sprintf("\nTG: %d ↔ MAX: %d", l.TgChatID, l.MaxChatID))
						m.ReplyMarkup = kb
						b.tgBot.Send(m)
					}
				}
				continue
			}

			// Пересланное сообщение из канала → показать ID или управление (только в личке)
			if msg.Chat.Type == "private" && msg.ForwardFromChat != nil && msg.ForwardFromChat.Type == "channel" {
				channelID := msg.ForwardFromChat.ID
				channelTitle := msg.ForwardFromChat.Title

				// Проверяем, уже связан ли канал
				if maxChatID, direction, ok := b.repo.GetCrosspostMaxChat(channelID); ok {
					text := tgCrosspostStatusText(channelTitle, direction)
					kb := tgCrosspostKeyboard(direction, maxChatID)
					m := tgbotapi.NewMessage(msg.Chat.ID, text)
					m.ReplyMarkup = kb
					b.tgBot.Send(m)
					continue
				}

				b.tgBot.Send(tgbotapi.NewMessage(msg.Chat.ID,
					fmt.Sprintf("TG-канал «%s»\nID: %d\n\nВ личке MAX-бота напишите:\n/crosspost %d\n\nЗатем перешлите пост из MAX-канала.", channelTitle, channelID, channelID)))
				continue
			}

			// Проверка прав админа в группах
			isGroup := isTgGroup(msg.Chat.Type)
			isAdmin := false
			if isGroup && msg.From != nil {
				member, err := b.tgBot.GetChatMember(tgbotapi.GetChatMemberConfig{
					ChatConfigWithUser: tgbotapi.ChatConfigWithUser{
						ChatID: msg.Chat.ID,
						UserID: msg.From.ID,
					},
				})
				if err == nil {
					isAdmin = isTgAdmin(member.Status)
				}
			}

			// /bridge prefix on/off
			if text == "/bridge prefix on" || text == "/bridge prefix off" {
				if isGroup && !isAdmin {
					b.tgBot.Send(tgbotapi.NewMessage(msg.Chat.ID, "Эта команда доступна только админам группы."))
					continue
				}
				on := text == "/bridge prefix on"
				if b.repo.SetPrefix("tg", msg.Chat.ID, on) {
					if on {
						b.tgBot.Send(tgbotapi.NewMessage(msg.Chat.ID, "Префикс [TG]/[MAX] включён."))
					} else {
						b.tgBot.Send(tgbotapi.NewMessage(msg.Chat.ID, "Префикс [TG]/[MAX] выключен."))
					}
				} else {
					b.tgBot.Send(tgbotapi.NewMessage(msg.Chat.ID, "Чат не связан. Сначала выполните /bridge."))
				}
				continue
			}

			// /bridge или /bridge <key>
			if text == "/bridge" || strings.HasPrefix(text, "/bridge ") {
				if isGroup && !isAdmin {
					b.tgBot.Send(tgbotapi.NewMessage(msg.Chat.ID, "Эта команда доступна только админам группы."))
					continue
				}
				key := strings.TrimSpace(strings.TrimPrefix(text, "/bridge"))
				paired, generatedKey, err := b.repo.Register(key, "tg", msg.Chat.ID)
				if err != nil {
					slog.Error("register failed", "err", err)
					continue
				}

				if paired {
					b.tgBot.Send(tgbotapi.NewMessage(msg.Chat.ID, "Связано! Сообщения теперь пересылаются."))
					slog.Info("paired", "platform", "tg", "chat", msg.Chat.ID, "key", key)
				} else if generatedKey != "" {
					b.tgBot.Send(tgbotapi.NewMessage(msg.Chat.ID,
						fmt.Sprintf("Ключ для связки: %s\n\nОтправьте в MAX-чате:\n/bridge %s", generatedKey, generatedKey)))
					slog.Info("pending", "platform", "tg", "chat", msg.Chat.ID, "key", generatedKey)
				} else {
					b.tgBot.Send(tgbotapi.NewMessage(msg.Chat.ID, "Ключ не найден или чат той же платформы."))
				}
				continue
			}

			if text == "/unbridge" {
				if isGroup && !isAdmin {
					b.tgBot.Send(tgbotapi.NewMessage(msg.Chat.ID, "Эта команда доступна только админам группы."))
					continue
				}
				if b.repo.Unpair("tg", msg.Chat.ID) {
					b.tgBot.Send(tgbotapi.NewMessage(msg.Chat.ID, "Связка удалена."))
				} else {
					b.tgBot.Send(tgbotapi.NewMessage(msg.Chat.ID, "Этот чат не связан."))
				}
				continue
			}

			// Пересылка
			maxChatID, linked := b.repo.GetMaxChat(msg.Chat.ID)
			if !linked {
				continue
			}
			if msg.From != nil && msg.From.IsBot {
				continue
			}

			prefix := b.repo.HasPrefix("tg", msg.Chat.ID)
			caption := formatTgCaption(msg, prefix)

			// Проверяем anti-loop
			checkText := msg.Text
			if checkText == "" {
				checkText = msg.Caption
			}
			if strings.HasPrefix(checkText, "[MAX]") || strings.HasPrefix(checkText, "[TG]") {
				continue
			}

			b.forwardTgToMax(ctx, msg, maxChatID, caption)
		}
	}
}

// forwardTgToMax пересылает TG-сообщение (текст/медиа) в MAX-чат.
func (b *Bridge) forwardTgToMax(ctx context.Context, msg *tgbotapi.Message, maxChatID int64, caption string) {
	// Определяем медиа
	var mediaToken string
	var mediaAttType string // "video", "file", "audio"

	if msg.Photo != nil {
		// Фото — через SDK (работает)
		photo := msg.Photo[len(msg.Photo)-1]
		m := maxbot.NewMessage().SetChat(maxChatID).SetText(caption)
		if fileURL, err := b.tgBot.GetFileDirectURL(photo.FileID); err == nil {
			if uploaded, err := b.maxApi.Uploads.UploadPhotoFromUrl(ctx, fileURL); err == nil {
				m.AddPhoto(uploaded)
			} else {
				slog.Error("TG→MAX photo upload failed", "err", err)
			}
		}
		if msg.ReplyToMessage != nil {
			if maxReplyID, ok := b.repo.LookupMaxMsgID(msg.Chat.ID, msg.ReplyToMessage.MessageID); ok {
				m.SetReply(caption, maxReplyID)
			}
		}
		slog.Info("TG→MAX sending photo")
		result, err := b.maxApi.Messages.SendWithResult(ctx, m)
		if err != nil {
			slog.Error("TG→MAX send failed", "err", err)
		} else {
			slog.Info("TG→MAX sent", "mid", result.Body.Mid)
			b.repo.SaveMsg(msg.Chat.ID, msg.MessageID, maxChatID, result.Body.Mid)
		}
		return
	} else if msg.Video != nil {
		if uploaded, err := b.uploadTgMediaToMax(ctx, msg.Video.FileID, maxschemes.VIDEO, "video.mp4"); err == nil {
			mediaToken = uploaded.Token
			mediaAttType = "video"
		} else {
			slog.Error("TG→MAX video upload failed", "err", err)
		}
	} else if msg.Document != nil {
		name := "document"
		if msg.Document.FileName != "" {
			name = msg.Document.FileName
		}
		if uploaded, err := b.uploadTgMediaToMax(ctx, msg.Document.FileID, maxschemes.FILE, name); err == nil {
			mediaToken = uploaded.Token
			mediaAttType = "file"
		} else {
			slog.Error("TG→MAX file upload failed", "err", err)
		}
	} else if msg.Voice != nil {
		if uploaded, err := b.uploadTgMediaToMax(ctx, msg.Voice.FileID, maxschemes.AUDIO, "voice.ogg"); err == nil {
			mediaToken = uploaded.Token
			mediaAttType = "audio"
		} else {
			slog.Error("TG→MAX voice upload failed", "err", err)
		}
	} else if msg.Audio != nil {
		name := "audio.mp3"
		if msg.Audio.FileName != "" {
			name = msg.Audio.FileName
		}
		if uploaded, err := b.uploadTgMediaToMax(ctx, msg.Audio.FileID, maxschemes.FILE, name); err == nil {
			mediaToken = uploaded.Token
			mediaAttType = "file"
		} else {
			slog.Error("TG→MAX audio upload failed", "err", err)
		}
	}

	// Fallback для неудавшейся загрузки медиа
	if mediaAttType == "" && msg.Text == "" {
		mediaType := ""
		switch {
		case msg.Video != nil:
			mediaType = "[Видео]"
		case msg.VideoNote != nil:
			mediaType = "[Кружок]"
		case msg.Document != nil:
			mediaType = "[Файл]"
		case msg.Voice != nil:
			mediaType = "[Голосовое]"
		case msg.Audio != nil:
			mediaType = "[Аудио]"
		case msg.Sticker != nil:
			mediaType = "[Стикер]"
		default:
			return
		}
		caption = caption + mediaType
	}

	// Reply ID
	var replyTo string
	if msg.ReplyToMessage != nil {
		if maxReplyID, ok := b.repo.LookupMaxMsgID(msg.Chat.ID, msg.ReplyToMessage.MessageID); ok {
			replyTo = maxReplyID
		}
	}

	if mediaAttType != "" {
		// Медиа — отправляем напрямую (обход SDK)
		slog.Info("TG→MAX sending direct", "type", mediaAttType)
		mid, err := b.sendMaxDirect(ctx, maxChatID, caption, mediaAttType, mediaToken, replyTo)
		if err != nil {
			slog.Error("TG→MAX send failed", "err", err)
		} else {
			slog.Info("TG→MAX sent", "mid", mid)
			b.repo.SaveMsg(msg.Chat.ID, msg.MessageID, maxChatID, mid)
		}
	} else {
		// Текст — через SDK
		m := maxbot.NewMessage().SetChat(maxChatID).SetText(caption)
		if replyTo != "" {
			m.SetReply(caption, replyTo)
		}
		slog.Info("TG→MAX sending")
		result, err := b.maxApi.Messages.SendWithResult(ctx, m)
		if err != nil {
			slog.Error("TG→MAX send failed", "err", err)
		} else {
			slog.Info("TG→MAX sent", "mid", result.Body.Mid)
			b.repo.SaveMsg(msg.Chat.ID, msg.MessageID, maxChatID, result.Body.Mid)
		}
	}
}

// handleTgChannelPost обрабатывает посты из TG-каналов (только пересылка crosspost).
func (b *Bridge) handleTgChannelPost(ctx context.Context, msg *tgbotapi.Message) {
	// Команды в канале игнорируем — настройка через личку с ботом
	text := strings.TrimSpace(msg.Text)
	if strings.HasPrefix(text, "/") {
		return
	}

	// Пересылка crosspost: TG → MAX
	maxChatID, direction, ok := b.repo.GetCrosspostMaxChat(msg.Chat.ID)
	if !ok {
		return
	}
	if direction == "max>tg" {
		return // только MAX→TG, пропускаем
	}

	// Anti-loop
	checkText := msg.Text
	if checkText == "" {
		checkText = msg.Caption
	}
	if strings.HasPrefix(checkText, "[MAX]") || strings.HasPrefix(checkText, "[TG]") {
		return
	}

	caption := formatTgCrosspostCaption(msg)

	b.forwardTgToMax(ctx, msg, maxChatID, caption)
}

// handleTgCallback обрабатывает нажатия inline-кнопок (crosspost management).
func (b *Bridge) handleTgCallback(ctx context.Context, query *tgbotapi.CallbackQuery) {
	data := query.Data
	chatID := query.Message.Chat.ID
	msgID := query.Message.MessageID

	// cpd:dir:maxChatID — change direction
	if strings.HasPrefix(data, "cpd:") {
		parts := strings.SplitN(data, ":", 3)
		if len(parts) != 3 {
			return
		}
		dir := parts[1]
		maxChatID, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			return
		}
		if dir != "tg>max" && dir != "max>tg" && dir != "both" {
			return
		}
		b.repo.SetCrosspostDirection(maxChatID, dir)

		// Получаем title канала (из текста сообщения)
		title := parseTgCrosspostTitle(query.Message.Text)
		text := tgCrosspostStatusText(title, dir)
		kb := tgCrosspostKeyboard(dir, maxChatID)
		edit := tgbotapi.NewEditMessageTextAndMarkup(chatID, msgID, text, kb)
		b.tgBot.Send(edit)
		b.tgBot.Request(tgbotapi.NewCallback(query.ID, "Готово"))
		return
	}

	// cpu:maxChatID — unlink (show confirmation)
	if strings.HasPrefix(data, "cpu:") {
		maxChatID, err := strconv.ParseInt(strings.TrimPrefix(data, "cpu:"), 10, 64)
		if err != nil {
			return
		}
		kb := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("Да, удалить", fmt.Sprintf("cpuc:%d", maxChatID)),
				tgbotapi.NewInlineKeyboardButtonData("Отмена", fmt.Sprintf("cpux:%d", maxChatID)),
			),
		)
		edit := tgbotapi.NewEditMessageTextAndMarkup(chatID, msgID, "Удалить кросспостинг?", kb)
		b.tgBot.Send(edit)
		b.tgBot.Request(tgbotapi.NewCallback(query.ID, ""))
		return
	}

	// cpuc:maxChatID — unlink confirmed
	if strings.HasPrefix(data, "cpuc:") {
		maxChatID, err := strconv.ParseInt(strings.TrimPrefix(data, "cpuc:"), 10, 64)
		if err != nil {
			return
		}
		b.repo.UnpairCrosspost(maxChatID)
		edit := tgbotapi.NewEditMessageText(chatID, msgID, "Кросспостинг удалён.")
		b.tgBot.Send(edit)
		b.tgBot.Request(tgbotapi.NewCallback(query.ID, "Удалено"))
		return
	}

	// cpux:maxChatID — cancel (return to management keyboard)
	if strings.HasPrefix(data, "cpux:") {
		maxChatID, err := strconv.ParseInt(strings.TrimPrefix(data, "cpux:"), 10, 64)
		if err != nil {
			return
		}
		// Lookup current direction
		_, direction, ok := b.repo.GetCrosspostTgChat(maxChatID)
		if !ok {
			edit := tgbotapi.NewEditMessageText(chatID, msgID, "Кросспостинг не найден.")
			b.tgBot.Send(edit)
			b.tgBot.Request(tgbotapi.NewCallback(query.ID, ""))
			return
		}
		title := parseTgCrosspostTitle(query.Message.Text)
		text := tgCrosspostStatusText(title, direction)
		kb := tgCrosspostKeyboard(direction, maxChatID)
		edit := tgbotapi.NewEditMessageTextAndMarkup(chatID, msgID, text, kb)
		b.tgBot.Send(edit)
		b.tgBot.Request(tgbotapi.NewCallback(query.ID, ""))
		return
	}
}

// tgCrosspostKeyboard строит inline-клавиатуру для управления кросспостингом.
func tgCrosspostKeyboard(direction string, maxChatID int64) tgbotapi.InlineKeyboardMarkup {
	lblTgMax := "TG → MAX"
	lblMaxTg := "MAX → TG"
	lblBoth := "⟷ Оба"
	switch direction {
	case "tg>max":
		lblTgMax = "✓ TG → MAX"
	case "max>tg":
		lblMaxTg = "✓ MAX → TG"
	default: // "both"
		lblBoth = "✓ ⟷ Оба"
	}
	id := strconv.FormatInt(maxChatID, 10)
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(lblTgMax, "cpd:tg>max:"+id),
			tgbotapi.NewInlineKeyboardButtonData(lblMaxTg, "cpd:max>tg:"+id),
			tgbotapi.NewInlineKeyboardButtonData(lblBoth, "cpd:both:"+id),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("❌ Удалить", "cpu:"+id),
		),
	)
}

// tgCrosspostStatusText возвращает текст статуса кросспостинга.
func tgCrosspostStatusText(title, direction string) string {
	dirLabel := "⟷ оба"
	switch direction {
	case "tg>max":
		dirLabel = "TG → MAX"
	case "max>tg":
		dirLabel = "MAX → TG"
	}
	if title != "" {
		return fmt.Sprintf("Кросспостинг «%s»\nНаправление: %s", title, dirLabel)
	}
	return fmt.Sprintf("Кросспостинг\nНаправление: %s", dirLabel)
}

// parseTgCrosspostTitle извлекает название канала из текста сообщения.
func parseTgCrosspostTitle(text string) string {
	// Ищем «...» в тексте
	start := strings.Index(text, "«")
	end := strings.Index(text, "»")
	if start >= 0 && end > start {
		return text[start+len("«") : end]
	}
	return ""
}

// handleTgEditedChannelPost обрабатывает редактирования постов в TG-каналах.
func (b *Bridge) handleTgEditedChannelPost(ctx context.Context, edited *tgbotapi.Message) {
	maxMsgID, ok := b.repo.LookupMaxMsgID(edited.Chat.ID, edited.MessageID)
	if !ok {
		return
	}

	maxChatID, direction, linked := b.repo.GetCrosspostMaxChat(edited.Chat.ID)
	if !linked {
		return
	}
	if direction == "max>tg" {
		return
	}

	text := edited.Text
	if text == "" {
		text = edited.Caption
	}
	if text == "" {
		return
	}

	m := maxbot.NewMessage().SetChat(maxChatID).SetText(text)
	if err := b.maxApi.Messages.EditMessage(ctx, maxMsgID, m); err != nil {
		slog.Error("TG→MAX crosspost edit failed", "err", err)
	} else {
		slog.Info("TG→MAX crosspost edited", "mid", maxMsgID)
	}
}
