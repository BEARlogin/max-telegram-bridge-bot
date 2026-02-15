package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	maxbot "github.com/max-messenger/max-bot-api-client-go"
	maxschemes "github.com/max-messenger/max-bot-api-client-go/schemes"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func (b *Bridge) listenMax(ctx context.Context) {
	var updates <-chan maxschemes.UpdateInterface

	if b.cfg.WebhookURL != "" {
		whPath := b.maxWebhookPath()
		whURL := strings.TrimRight(b.cfg.WebhookURL, "/") + whPath
		ch := make(chan maxschemes.UpdateInterface, 100)
		http.HandleFunc(whPath, b.maxApi.GetHandler(ch))
		if _, err := b.maxApi.Subscriptions.Subscribe(ctx, whURL, nil, ""); err != nil {
			slog.Error("MAX webhook subscribe failed", "err", err)
			return
		}
		updates = ch
		slog.Info("MAX webhook mode")
	} else {
		updates = b.maxApi.GetUpdates(ctx)
		slog.Info("MAX polling mode")
	}

	for {
		select {
		case <-ctx.Done():
			return
		case upd, ok := <-updates:
			if !ok {
				return
			}

			slog.Debug("MAX update", "type", fmt.Sprintf("%T", upd))

			// Обработка удаления
			if delUpd, isDel := upd.(*maxschemes.MessageRemovedUpdate); isDel {
				tgChatID, tgMsgID, ok := b.repo.LookupTgMsgID(delUpd.MessageId)
				if !ok {
					continue
				}
				del := tgbotapi.NewDeleteMessage(tgChatID, tgMsgID)
				if _, err := b.tgBot.Request(del); err != nil {
					slog.Error("MAX→TG delete failed", "err", err)
				} else {
					slog.Info("MAX→TG deleted", "tgMsg", tgMsgID)
				}
				continue
			}

			// Обработка edit
			if editUpd, isEdit := upd.(*maxschemes.MessageEditedUpdate); isEdit {
				if editUpd.Message.Sender.IsBot {
					continue
				}
				mid := editUpd.Message.Body.Mid
				tgChatID, tgMsgID, ok := b.repo.LookupTgMsgID(mid)
				if !ok {
					continue
				}
				prefix := b.repo.HasPrefix("max", editUpd.Message.Recipient.ChatId)
				name := editUpd.Message.Sender.Name
				if name == "" {
					name = editUpd.Message.Sender.Username
				}
				text := editUpd.Message.Body.Text
				if text == "" || strings.HasPrefix(text, "[TG]") || strings.HasPrefix(text, "[MAX]") {
					continue
				}
				var fwd string
				if prefix {
					fwd = fmt.Sprintf("[MAX] %s: %s", name, text)
				} else {
					fwd = fmt.Sprintf("%s: %s", name, text)
				}
				editMsg := tgbotapi.NewEditMessageText(tgChatID, tgMsgID, fwd)
				if _, err := b.tgBot.Send(editMsg); err != nil {
					slog.Error("MAX→TG edit failed", "err", err)
				} else {
					slog.Info("MAX→TG edited", "tgMsg", tgMsgID)
				}
				continue
			}

			// Обработка inline-кнопок (crosspost management)
			if cbUpd, isCb := upd.(*maxschemes.MessageCallbackUpdate); isCb {
				b.handleMaxCallback(ctx, cbUpd)
				continue
			}

			msgUpd, isMsg := upd.(*maxschemes.MessageCreatedUpdate)
			if !isMsg {
				continue
			}

			body := msgUpd.Message.Body
			chatID := msgUpd.Message.Recipient.ChatId
			text := strings.TrimSpace(body.Text)
			isDialog := msgUpd.Message.Recipient.ChatType == "dialog"

			slog.Debug("MAX msg received", "from", msgUpd.Message.Sender.Name, "chat", chatID, "type", msgUpd.Message.Recipient.ChatType)

			if text == "/start" || text == "/help" {
				m := maxbot.NewMessage().SetChat(chatID).SetText(
					"Бот-мост между MAX и Telegram.\n\n" +
						"Команды (группы):\n" +
						"/bridge — создать ключ для связки чатов\n" +
						"/bridge <ключ> — связать этот чат с Telegram-чатом по ключу\n" +
						"/bridge prefix on/off — включить/выключить префикс [TG]/[MAX]\n" +
						"/unbridge — удалить связку\n\n" +
						"Кросспостинг каналов (в личке бота):\n" +
						"/crosspost <TG_ID> — связать MAX-канал с TG-каналом\n" +
						"   (TG ID получить: перешлите пост из TG-канала TG-боту)\n\n" +
						"Как связать каналы:\n" +
						"1. Перешлите пост из TG-канала в личку TG-бота\n" +
						"   TG: " + b.cfg.TgBotURL + "\n" +
						"2. Бот покажет ID канала — скопируйте\n" +
						"3. Здесь в личке напишите: /crosspost <TG_ID>\n" +
						"4. Перешлите пост из MAX-канала сюда → готово!\n\n" +
						"Управление: перешлите пост из связанного канала → кнопки\n\n" +
						"Как связать группы:\n" +
						"1. Добавьте бота в оба чата\n" +
						"   MAX: " + b.cfg.MaxBotURL + "\n" +
						"2. В одном из чатов отправьте /bridge\n" +
						"3. Бот выдаст ключ — отправьте его в другом чате\n" +
						"4. Готово!")
				b.maxApi.Messages.Send(ctx, m)
				continue
			}

			// Проверка прав админа в группах
			isGroup := isMaxGroup(msgUpd.Message.Recipient.ChatType)
			isAdmin := false
			if isGroup && msgUpd.Message.Sender.UserId != 0 {
				admins, err := b.maxApi.Chats.GetChatAdmins(ctx, chatID)
				if err == nil {
					isAdmin = isMaxUserAdmin(admins.Members, msgUpd.Message.Sender.UserId)
				}
			} else if isGroup {
				// В каналах MAX не передаёт sender userId — пропускаем проверку
				isAdmin = true
			}

			// /bridge prefix on/off
			if text == "/bridge prefix on" || text == "/bridge prefix off" {
				if isGroup && !isAdmin {
					m := maxbot.NewMessage().SetChat(chatID).SetText("Эта команда доступна только админам группы.")
					b.maxApi.Messages.Send(ctx, m)
					continue
				}
				on := text == "/bridge prefix on"
				if b.repo.SetPrefix("max", chatID, on) {
					reply := "Префикс [TG]/[MAX] включён."
					if !on {
						reply = "Префикс [TG]/[MAX] выключен."
					}
					m := maxbot.NewMessage().SetChat(chatID).SetText(reply)
					b.maxApi.Messages.Send(ctx, m)
				} else {
					m := maxbot.NewMessage().SetChat(chatID).SetText("Чат не связан. Сначала выполните /bridge.")
					b.maxApi.Messages.Send(ctx, m)
				}
				continue
			}

			// /bridge или /bridge <key>
			if text == "/bridge" || strings.HasPrefix(text, "/bridge ") {
				if isGroup && !isAdmin {
					m := maxbot.NewMessage().SetChat(chatID).SetText("Эта команда доступна только админам группы.")
					b.maxApi.Messages.Send(ctx, m)
					continue
				}
				key := strings.TrimSpace(strings.TrimPrefix(text, "/bridge"))
				paired, generatedKey, err := b.repo.Register(key, "max", chatID)
				if err != nil {
					slog.Error("register failed", "err", err)
					continue
				}

				if paired {
					m := maxbot.NewMessage().SetChat(chatID).SetText("Связано! Сообщения теперь пересылаются.")
					b.maxApi.Messages.Send(ctx, m)
					slog.Info("paired", "platform", "max", "chat", chatID, "key", key)
				} else if generatedKey != "" {
					m := maxbot.NewMessage().SetChat(chatID).
						SetText(fmt.Sprintf("Ключ для связки: %s\n\nОтправьте в Telegram-чате:\n/bridge %s", generatedKey, generatedKey))
					b.maxApi.Messages.Send(ctx, m)
					slog.Info("pending", "platform", "max", "chat", chatID, "key", generatedKey)
				} else {
					m := maxbot.NewMessage().SetChat(chatID).SetText("Ключ не найден или чат той же платформы.")
					b.maxApi.Messages.Send(ctx, m)
				}
				continue
			}

			if text == "/unbridge" {
				if isGroup && !isAdmin {
					m := maxbot.NewMessage().SetChat(chatID).SetText("Эта команда доступна только админам группы.")
					b.maxApi.Messages.Send(ctx, m)
					continue
				}
				if b.repo.Unpair("max", chatID) {
					m := maxbot.NewMessage().SetChat(chatID).SetText("Связка удалена.")
					b.maxApi.Messages.Send(ctx, m)
				} else {
					m := maxbot.NewMessage().SetChat(chatID).SetText("Этот чат не связан.")
					b.maxApi.Messages.Send(ctx, m)
				}
				continue
			}

			// === Crosspost команды (только в личке бота) ===

			// /crosspost <tg_channel_id> — начало настройки (только в личке)
			if isDialog && strings.HasPrefix(text, "/crosspost") {
				arg := strings.TrimSpace(strings.TrimPrefix(text, "/crosspost"))
				if arg == "" {
					links := b.repo.ListCrossposts()
					if len(links) == 0 {
						m := maxbot.NewMessage().SetChat(chatID).SetText(
							"Нет активных связок.\n\n" +
								"Настройка:\n" +
								"1. Перешлите пост из TG-канала в личку TG-бота\n" +
								"   " + b.cfg.TgBotURL + "\n" +
								"2. Бот покажет ID канала\n" +
								"3. Здесь напишите: /crosspost <TG_ID>\n" +
								"4. Перешлите пост из MAX-канала сюда")
						b.maxApi.Messages.Send(ctx, m)
					} else {
						for _, l := range links {
							kb := maxCrosspostKeyboard(b.maxApi, l.Direction, l.MaxChatID)
							m := maxbot.NewMessage().SetChat(chatID).
								SetText(maxCrosspostStatusText(l.TgChatID, l.Direction)).
								AddKeyboard(kb)
							b.maxApi.Messages.Send(ctx, m)
						}
					}
					continue
				}
				tgChannelID, err := strconv.ParseInt(arg, 10, 64)
				if err != nil {
					m := maxbot.NewMessage().SetChat(chatID).SetText("Неверный ID. Пример: /crosspost -1001234567890")
					b.maxApi.Messages.Send(ctx, m)
					continue
				}

				// Сохраняем ожидание: userId → tgChannelID
				b.cpWaitMu.Lock()
				b.cpWait[msgUpd.Message.Sender.UserId] = tgChannelID
				b.cpWaitMu.Unlock()

				m := maxbot.NewMessage().SetChat(chatID).SetText(
					fmt.Sprintf("TG канал ID: %d\n\nТеперь перешлите любой пост из MAX-канала, который хотите связать.", tgChannelID))
				b.maxApi.Messages.Send(ctx, m)
				slog.Info("crosspost waiting for forward", "user", msgUpd.Message.Sender.UserId, "tgChannel", tgChannelID)
				continue
			}

			// Пересланное сообщение в личке → завершение настройки crosspost или показ управления
			if isDialog && msgUpd.Message.Link != nil && msgUpd.Message.Link.Type == maxschemes.FORWARD {
				maxChannelID := msgUpd.Message.Link.ChatId

				userId := msgUpd.Message.Sender.UserId
				b.cpWaitMu.Lock()
				tgChannelID, waiting := b.cpWait[userId]
				if waiting {
					delete(b.cpWait, userId)
				}
				b.cpWaitMu.Unlock()

				if waiting && maxChannelID != 0 {
					// Проверяем, не связан ли уже
					if _, _, ok := b.repo.GetCrosspostTgChat(maxChannelID); ok {
						m := maxbot.NewMessage().SetChat(chatID).SetText("Этот MAX-канал уже связан.")
						b.maxApi.Messages.Send(ctx, m)
						continue
					}

					if err := b.repo.PairCrosspost(tgChannelID, maxChannelID); err != nil {
						slog.Error("crosspost pair failed", "err", err)
						m := maxbot.NewMessage().SetChat(chatID).SetText("Ошибка при создании связки.")
						b.maxApi.Messages.Send(ctx, m)
						continue
					}

					// Показать статус + клавиатуру после паринга
					kb := maxCrosspostKeyboard(b.maxApi, "both", maxChannelID)
					m := maxbot.NewMessage().SetChat(chatID).
						SetText(fmt.Sprintf("Кросспостинг настроен!\nTG: %d ↔ MAX: %d\nНаправление: ⟷ оба", tgChannelID, maxChannelID)).
						AddKeyboard(kb)
					b.maxApi.Messages.Send(ctx, m)
					slog.Info("crosspost paired", "tg", tgChannelID, "max", maxChannelID)
					continue
				}

				// Нет cpWait — проверяем, связан ли канал → показать управление
				if maxChannelID != 0 {
					if tgID, direction, ok := b.repo.GetCrosspostTgChat(maxChannelID); ok {
						kb := maxCrosspostKeyboard(b.maxApi, direction, maxChannelID)
						m := maxbot.NewMessage().SetChat(chatID).
							SetText(maxCrosspostStatusText(tgID, direction)).
							AddKeyboard(kb)
						b.maxApi.Messages.Send(ctx, m)
						continue
					}
				}

				// Канал не связан, cpWait нет — сообщить
				if maxChannelID != 0 {
					m := maxbot.NewMessage().SetChat(chatID).SetText("Этот канал не связан с кросспостингом.\n\nДля настройки:\n/crosspost <TG_ID>")
					b.maxApi.Messages.Send(ctx, m)
				}
				continue
			}

			// Пересылка (bridge)
			tgChatID, linked := b.repo.GetTgChat(chatID)
			if linked && !msgUpd.Message.Sender.IsBot {
				// Anti-loop
				if !strings.HasPrefix(text, "[TG]") && !strings.HasPrefix(text, "[MAX]") {
					prefix := b.repo.HasPrefix("max", chatID)
					caption := formatMaxCaption(msgUpd, prefix)
					b.forwardMaxToTg(ctx, msgUpd, tgChatID, caption)
				}
				continue
			}

			// Пересылка (crosspost fallback)
			if msgUpd.Message.Sender.IsBot {
				continue
			}
			tgChatID, direction, cpLinked := b.repo.GetCrosspostTgChat(chatID)
			if !cpLinked {
				continue
			}
			if direction == "tg>max" {
				continue // только TG→MAX, пропускаем
			}

			// Anti-loop
			if strings.HasPrefix(text, "[TG]") || strings.HasPrefix(text, "[MAX]") {
				continue
			}

			slog.Info("MAX→TG raw", "text", msgUpd.Message.Body.Text, "markups", msgUpd.Message.Body.Markups, "rawAtt", msgUpd.Message.Body.RawAttachments)
			caption := formatMaxCrosspostCaption(msgUpd)
			b.forwardMaxToTg(ctx, msgUpd, tgChatID, caption)
		}
	}
}

// handleMaxCallback обрабатывает нажатия inline-кнопок (crosspost management).
func (b *Bridge) handleMaxCallback(ctx context.Context, cbUpd *maxschemes.MessageCallbackUpdate) {
	data := cbUpd.Callback.Payload
	callbackID := cbUpd.Callback.CallbackID

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

		tgID, _, _ := b.repo.GetCrosspostTgChat(maxChatID)
		body := maxCrosspostMessageBody(b.maxApi, maxCrosspostStatusText(tgID, dir), dir, maxChatID)
		b.maxApi.Messages.AnswerOnCallback(ctx, callbackID, &maxschemes.CallbackAnswer{
			Message:      body,
			Notification: "Готово",
		})
		return
	}

	// cpu:maxChatID — unlink (show confirmation)
	if strings.HasPrefix(data, "cpu:") {
		maxChatID, err := strconv.ParseInt(strings.TrimPrefix(data, "cpu:"), 10, 64)
		if err != nil {
			return
		}
		kb := b.maxApi.Messages.NewKeyboardBuilder()
		kb.AddRow().
			AddCallback("Да, удалить", maxschemes.NEGATIVE, fmt.Sprintf("cpuc:%d", maxChatID)).
			AddCallback("Отмена", maxschemes.DEFAULT, fmt.Sprintf("cpux:%d", maxChatID))
		body := &maxschemes.NewMessageBody{
			Text:        "Удалить кросспостинг?",
			Attachments: []interface{}{maxschemes.NewInlineKeyboardAttachmentRequest(kb.Build())},
		}
		b.maxApi.Messages.AnswerOnCallback(ctx, callbackID, &maxschemes.CallbackAnswer{
			Message: body,
		})
		return
	}

	// cpuc:maxChatID — unlink confirmed
	if strings.HasPrefix(data, "cpuc:") {
		maxChatID, err := strconv.ParseInt(strings.TrimPrefix(data, "cpuc:"), 10, 64)
		if err != nil {
			return
		}
		b.repo.UnpairCrosspost(maxChatID)
		body := &maxschemes.NewMessageBody{Text: "Кросспостинг удалён."}
		b.maxApi.Messages.AnswerOnCallback(ctx, callbackID, &maxschemes.CallbackAnswer{
			Message:      body,
			Notification: "Удалено",
		})
		return
	}

	// cpux:maxChatID — cancel (return to management keyboard)
	if strings.HasPrefix(data, "cpux:") {
		maxChatID, err := strconv.ParseInt(strings.TrimPrefix(data, "cpux:"), 10, 64)
		if err != nil {
			return
		}
		tgID, direction, ok := b.repo.GetCrosspostTgChat(maxChatID)
		if !ok {
			body := &maxschemes.NewMessageBody{Text: "Кросспостинг не найден."}
			b.maxApi.Messages.AnswerOnCallback(ctx, callbackID, &maxschemes.CallbackAnswer{
				Message: body,
			})
			return
		}
		body := maxCrosspostMessageBody(b.maxApi, maxCrosspostStatusText(tgID, direction), direction, maxChatID)
		b.maxApi.Messages.AnswerOnCallback(ctx, callbackID, &maxschemes.CallbackAnswer{
			Message: body,
		})
		return
	}
}

// maxCrosspostMessageBody строит NewMessageBody с текстом и inline-клавиатурой.
func maxCrosspostMessageBody(api *maxbot.Api, text, direction string, maxChatID int64) *maxschemes.NewMessageBody {
	kb := maxCrosspostKeyboard(api, direction, maxChatID)
	return &maxschemes.NewMessageBody{
		Text:        text,
		Attachments: []interface{}{maxschemes.NewInlineKeyboardAttachmentRequest(kb.Build())},
	}
}

// maxCrosspostKeyboard строит inline-клавиатуру для управления кросспостингом в MAX.
func maxCrosspostKeyboard(api *maxbot.Api, direction string, maxChatID int64) *maxbot.Keyboard {
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
	kb := api.Messages.NewKeyboardBuilder()
	kb.AddRow().
		AddCallback(lblTgMax, maxschemes.DEFAULT, "cpd:tg>max:"+id).
		AddCallback(lblMaxTg, maxschemes.DEFAULT, "cpd:max>tg:"+id).
		AddCallback(lblBoth, maxschemes.DEFAULT, "cpd:both:"+id)
	kb.AddRow().
		AddCallback("❌ Удалить", maxschemes.NEGATIVE, "cpu:"+id)
	return kb
}

// maxCrosspostStatusText возвращает текст статуса кросспостинга для MAX.
func maxCrosspostStatusText(tgChatID int64, direction string) string {
	dirLabel := "⟷ оба"
	switch direction {
	case "tg>max":
		dirLabel = "TG → MAX"
	case "max>tg":
		dirLabel = "MAX → TG"
	}
	return fmt.Sprintf("Кросспостинг настроен\nTG: %d ↔ MAX\nНаправление: %s", tgChatID, dirLabel)
}

// forwardMaxToTg пересылает MAX-сообщение (текст/медиа) в TG-чат.
func (b *Bridge) forwardMaxToTg(ctx context.Context, msgUpd *maxschemes.MessageCreatedUpdate, tgChatID int64, caption string) {
	body := msgUpd.Message.Body
	chatID := msgUpd.Message.Recipient.ChatId
	text := strings.TrimSpace(body.Text)

	// Reply ID
	var replyToID int
	if body.ReplyTo != "" {
		if _, rid, ok := b.repo.LookupTgMsgID(body.ReplyTo); ok {
			replyToID = rid
		}
	} else if msgUpd.Message.Link != nil {
		mid := msgUpd.Message.Link.Message.Mid
		if mid != "" {
			if _, rid, ok := b.repo.LookupTgMsgID(mid); ok {
				replyToID = rid
			}
		}
	}

	// Проверяем вложения
	var sent tgbotapi.Message
	var sendErr error
	mediaSent := false

	// Определяем HTML caption если есть markups (для кросспостинга)
	htmlCaption := caption
	useHTML := len(body.Markups) > 0 && caption == text
	if useHTML {
		htmlCaption = maxMarkupsToHTML(text, body.Markups)
	}

	for _, att := range body.Attachments {
		switch a := att.(type) {
		case *maxschemes.PhotoAttachment:
			if a.Payload.Url != "" {
				photo := tgbotapi.NewPhoto(tgChatID, tgbotapi.FileURL(a.Payload.Url))
				photo.Caption = htmlCaption
				if useHTML {
					photo.ParseMode = "HTML"
				}
				photo.ReplyToMessageID = replyToID
				sent, sendErr = b.tgBot.Send(photo)
				mediaSent = true
			}
		case *maxschemes.VideoAttachment:
			if a.Payload.Url != "" {
				video := tgbotapi.NewVideo(tgChatID, tgbotapi.FileURL(a.Payload.Url))
				video.Caption = htmlCaption
				if useHTML {
					video.ParseMode = "HTML"
				}
				video.ReplyToMessageID = replyToID
				sent, sendErr = b.tgBot.Send(video)
				mediaSent = true
			}
		case *maxschemes.AudioAttachment:
			if a.Payload.Url != "" {
				audio := tgbotapi.NewAudio(tgChatID, tgbotapi.FileURL(a.Payload.Url))
				audio.Caption = htmlCaption
				if useHTML {
					audio.ParseMode = "HTML"
				}
				audio.ReplyToMessageID = replyToID
				sent, sendErr = b.tgBot.Send(audio)
				mediaSent = true
			}
		case *maxschemes.FileAttachment:
			if a.Payload.Url != "" {
				doc := tgbotapi.NewDocument(tgChatID, tgbotapi.FileURL(a.Payload.Url))
				doc.Caption = htmlCaption
				if useHTML {
					doc.ParseMode = "HTML"
				}
				doc.ReplyToMessageID = replyToID
				sent, sendErr = b.tgBot.Send(doc)
				mediaSent = true
			}
		}
		if mediaSent {
			break
		}
	}

	// Текст без медиа
	if !mediaSent {
		if text == "" {
			return
		}
		// Если есть markups и caption = оригинальный текст (кросспостинг), конвертируем в HTML
		if len(body.Markups) > 0 && caption == text {
			htmlText := maxMarkupsToHTML(text, body.Markups)
			tgMsg := tgbotapi.NewMessage(tgChatID, htmlText)
			tgMsg.ParseMode = "HTML"
			tgMsg.ReplyToMessageID = replyToID
			sent, sendErr = b.tgBot.Send(tgMsg)
		} else {
			tgMsg := tgbotapi.NewMessage(tgChatID, caption)
			tgMsg.ReplyToMessageID = replyToID
			sent, sendErr = b.tgBot.Send(tgMsg)
		}
	}

	if sendErr != nil {
		slog.Error("MAX→TG send failed", "err", sendErr)
	} else {
		slog.Info("MAX→TG sent", "msgID", sent.MessageID, "media", mediaSent)
		b.repo.SaveMsg(tgChatID, sent.MessageID, chatID, body.Mid)
	}
}
