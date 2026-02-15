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
						"   (TG ID получить: перешлите пост из TG-канала TG-боту)\n" +
						"/crosspost direction tg>max|max>tg|both — направление\n" +
						"/uncrosspost — удалить кросспостинг\n\n" +
						"Как связать каналы:\n" +
						"1. Перешлите пост из TG-канала в личку TG-бота\n" +
						"   TG: " + b.cfg.TgBotURL + "\n" +
						"2. Бот покажет ID канала\n" +
						"3. Здесь в личке напишите: /crosspost <TG_ID>\n" +
						"4. Перешлите пост из MAX-канала сюда → готово!\n\n" +
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

			// /crosspost direction <dir> (в личке)
			if isDialog && strings.HasPrefix(text, "/crosspost direction ") {
				dir := strings.TrimSpace(strings.TrimPrefix(text, "/crosspost direction "))
				if dir != "both" && dir != "tg>max" && dir != "max>tg" {
					m := maxbot.NewMessage().SetChat(chatID).SetText("Направление: both, tg>max или max>tg")
					b.maxApi.Messages.Send(ctx, m)
					continue
				}
				// Ищем crosspost по MAX user → нужен MAX chat ID
				// В диалоге chatID = userId, ищем crosspost где max_chat_id связан с этим юзером
				// Пока поддерживаем direction только для последнего настроенного crosspost
				if b.repo.SetCrosspostDirection("max", chatID, dir) {
					m := maxbot.NewMessage().SetChat(chatID).SetText(fmt.Sprintf("Направление кросспостинга: %s", dir))
					b.maxApi.Messages.Send(ctx, m)
				} else {
					m := maxbot.NewMessage().SetChat(chatID).SetText("Кросспостинг не настроен.")
					b.maxApi.Messages.Send(ctx, m)
				}
				continue
			}

			// /uncrosspost (в личке)
			if isDialog && text == "/uncrosspost" {
				if b.repo.UnpairCrosspost("max", chatID) {
					m := maxbot.NewMessage().SetChat(chatID).SetText("Кросспостинг удалён.")
					b.maxApi.Messages.Send(ctx, m)
				} else {
					m := maxbot.NewMessage().SetChat(chatID).SetText("Кросспостинг не настроен.")
					b.maxApi.Messages.Send(ctx, m)
				}
				continue
			}

			// /crosspost <tg_channel_id> — начало настройки (только в личке)
			if isDialog && strings.HasPrefix(text, "/crosspost") {
				arg := strings.TrimSpace(strings.TrimPrefix(text, "/crosspost"))
				if arg == "" {
					m := maxbot.NewMessage().SetChat(chatID).SetText(
						"Кросспостинг каналов:\n\n" +
							"1. Перешлите пост из TG-канала в личку TG-бота\n" +
							"   " + b.cfg.TgBotURL + "\n" +
							"2. Бот покажет ID канала\n" +
							"3. Здесь напишите: /crosspost <TG_ID>\n" +
							"4. Перешлите пост из MAX-канала сюда")
					b.maxApi.Messages.Send(ctx, m)
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

			// Пересланное сообщение в личке → завершение настройки crosspost
			if isDialog && msgUpd.Message.Link != nil && msgUpd.Message.Link.Type == maxschemes.FORWARD {
				maxChannelID := msgUpd.Message.Link.ChatId

				userId := msgUpd.Message.Sender.UserId
				b.cpWaitMu.Lock()
				tgChannelID, waiting := b.cpWait[userId]
				if waiting {
					delete(b.cpWait, userId)
				}
				b.cpWaitMu.Unlock()

				if !waiting || maxChannelID == 0 {
					continue
				}

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

				m := maxbot.NewMessage().SetChat(chatID).SetText(
					fmt.Sprintf("Кросспостинг настроен!\nTG: %d ↔ MAX: %d\n\nУправление:\n/crosspost direction tg>max|max>tg|both\n/uncrosspost", tgChannelID, maxChannelID))
				b.maxApi.Messages.Send(ctx, m)
				slog.Info("crosspost paired", "tg", tgChannelID, "max", maxChannelID)
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

			caption := formatMaxCrosspostCaption(msgUpd)
			b.forwardMaxToTg(ctx, msgUpd, tgChatID, caption)
		}
	}
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

	for _, att := range body.Attachments {
		switch a := att.(type) {
		case *maxschemes.PhotoAttachment:
			if a.Payload.Url != "" {
				photo := tgbotapi.NewPhoto(tgChatID, tgbotapi.FileURL(a.Payload.Url))
				photo.Caption = caption
				photo.ReplyToMessageID = replyToID
				sent, sendErr = b.tgBot.Send(photo)
				mediaSent = true
			}
		case *maxschemes.VideoAttachment:
			if a.Payload.Url != "" {
				video := tgbotapi.NewVideo(tgChatID, tgbotapi.FileURL(a.Payload.Url))
				video.Caption = caption
				video.ReplyToMessageID = replyToID
				sent, sendErr = b.tgBot.Send(video)
				mediaSent = true
			}
		case *maxschemes.AudioAttachment:
			if a.Payload.Url != "" {
				audio := tgbotapi.NewAudio(tgChatID, tgbotapi.FileURL(a.Payload.Url))
				audio.Caption = caption
				audio.ReplyToMessageID = replyToID
				sent, sendErr = b.tgBot.Send(audio)
				mediaSent = true
			}
		case *maxschemes.FileAttachment:
			if a.Payload.Url != "" {
				doc := tgbotapi.NewDocument(tgChatID, tgbotapi.FileURL(a.Payload.Url))
				doc.Caption = caption
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
		tgMsg := tgbotapi.NewMessage(tgChatID, caption)
		tgMsg.ReplyToMessageID = replyToID
		sent, sendErr = b.tgBot.Send(tgMsg)
	}

	if sendErr != nil {
		slog.Error("MAX→TG send failed", "err", sendErr)
	} else {
		slog.Info("MAX→TG sent", "msgID", sent.MessageID, "media", mediaSent)
		b.repo.SaveMsg(tgChatID, sent.MessageID, chatID, body.Mid)
	}
}
