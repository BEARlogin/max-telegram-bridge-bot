package main

import (
	"context"
	"fmt"
	"log/slog"
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

			// Обработка channel posts (crosspost)
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
						"Команды (каналы):\n"+
						"/crosspost — связать каналы для кросспостинга\n"+
						"/crosspost <ключ> — связать по ключу\n"+
						"/crosspost direction tg>max|max>tg|both — направление\n"+
						"/uncrosspost — удалить кросспостинг\n\n"+
						"Как связать чаты:\n"+
						"1. Добавьте бота в оба чата\n"+
						"   TG: "+b.cfg.TgBotURL+"\n"+
						"   MAX: "+b.cfg.MaxBotURL+"\n"+
						"2. В MAX сделайте бота админом группы\n"+
						"3. В одном из чатов отправьте /bridge (или /crosspost для каналов)\n"+
						"4. Бот выдаст ключ — отправьте его в другом чате\n"+
						"5. Готово! Сообщения пересылаются."))
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

// handleTgChannelPost обрабатывает посты из TG-каналов (crosspost).
func (b *Bridge) handleTgChannelPost(ctx context.Context, msg *tgbotapi.Message) {
	text := strings.TrimSpace(msg.Text)

	if text == "/start" || text == "/help" {
		b.tgBot.Send(tgbotapi.NewMessage(msg.Chat.ID,
			"Кросспостинг каналов TG ↔ MAX.\n\n"+
				"Команды:\n"+
				"/crosspost — создать ключ для связки\n"+
				"/crosspost <ключ> — связать по ключу\n"+
				"/crosspost direction tg>max|max>tg|both — направление\n"+
				"/uncrosspost — удалить кросспостинг\n\n"+
				"Как связать:\n"+
				"1. Добавьте бота в TG-канал и MAX-чат как админа\n"+
				"2. В TG-канале отправьте /crosspost\n"+
				"3. Бот выдаст ключ — отправьте /crosspost <ключ> в MAX-чате\n"+
				"4. Готово!"))
		return
	}

	// /crosspost direction <dir>
	if strings.HasPrefix(text, "/crosspost direction ") {
		dir := strings.TrimSpace(strings.TrimPrefix(text, "/crosspost direction "))
		if dir != "both" && dir != "tg>max" && dir != "max>tg" {
			b.tgBot.Send(tgbotapi.NewMessage(msg.Chat.ID, "Направление: both, tg>max или max>tg"))
			return
		}
		if b.repo.SetCrosspostDirection("tg", msg.Chat.ID, dir) {
			b.tgBot.Send(tgbotapi.NewMessage(msg.Chat.ID, fmt.Sprintf("Направление кросспостинга: %s", dir)))
		} else {
			b.tgBot.Send(tgbotapi.NewMessage(msg.Chat.ID, "Канал не связан. Сначала выполните /crosspost."))
		}
		return
	}

	// /uncrosspost
	if text == "/uncrosspost" {
		if b.repo.UnpairCrosspost("tg", msg.Chat.ID) {
			b.tgBot.Send(tgbotapi.NewMessage(msg.Chat.ID, "Кросспостинг удалён."))
		} else {
			b.tgBot.Send(tgbotapi.NewMessage(msg.Chat.ID, "Этот канал не связан."))
		}
		return
	}

	// /crosspost или /crosspost <key>
	if text == "/crosspost" || strings.HasPrefix(text, "/crosspost ") {
		key := strings.TrimSpace(strings.TrimPrefix(text, "/crosspost"))
		paired, generatedKey, err := b.repo.RegisterCrosspost(key, "tg", msg.Chat.ID)
		if err != nil {
			slog.Error("crosspost register failed", "err", err)
			return
		}

		if paired {
			b.tgBot.Send(tgbotapi.NewMessage(msg.Chat.ID, "Кросспостинг настроен! Посты пересылаются."))
			slog.Info("crosspost paired", "platform", "tg", "chat", msg.Chat.ID, "key", key)
		} else if generatedKey != "" {
			b.tgBot.Send(tgbotapi.NewMessage(msg.Chat.ID,
				fmt.Sprintf("Ключ для кросспостинга: %s\n\nОтправьте в MAX-чате:\n/crosspost %s", generatedKey, generatedKey)))
			slog.Info("crosspost pending", "platform", "tg", "chat", msg.Chat.ID, "key", generatedKey)
		} else {
			b.tgBot.Send(tgbotapi.NewMessage(msg.Chat.ID, "Ключ не найден или чат той же платформы."))
		}
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
