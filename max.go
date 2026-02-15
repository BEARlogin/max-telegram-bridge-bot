package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
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

			// Обработка edit
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

			slog.Debug("MAX msg received", "from", msgUpd.Message.Sender.Name, "chat", chatID, "text", text)

			if text == "/start" || text == "/help" {
				m := maxbot.NewMessage().SetChat(chatID).SetText(
					"Бот-мост между MAX и Telegram.\n\n" +
						"Команды:\n" +
						"/bridge — создать ключ для связки чатов\n" +
						"/bridge <ключ> — связать этот чат с Telegram-чатом по ключу\n" +
						"/bridge prefix on/off — включить/выключить префикс [TG]/[MAX]\n" +
						"/unbridge — удалить связку\n\n" +
						"Как связать чаты:\n" +
						"1. Добавьте бота в оба чата\n" +
						"   TG: " + b.cfg.TgBotURL + "\n" +
						"   MAX: " + b.cfg.MaxBotURL + "\n" +
						"2. В одном из чатов отправьте /bridge\n" +
						"3. Бот выдаст ключ — отправьте /bridge <ключ> в другом чате\n" +
						"4. Готово! Сообщения пересылаются в обе стороны.")
				b.maxApi.Messages.Send(ctx, m)
				continue
			}

			// Проверка прав админа в группах
			isGroup := isMaxGroup(msgUpd.Message.Recipient.ChatType)
			isAdmin := false
			if isGroup {
				admins, err := b.maxApi.Chats.GetChatAdmins(ctx, chatID)
				if err == nil {
					isAdmin = isMaxUserAdmin(admins.Members, msgUpd.Message.Sender.UserId)
				}
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

			// Пересылка
			tgChatID, linked := b.repo.GetTgChat(chatID)
			if !linked || msgUpd.Message.Sender.IsBot {
				continue
			}

			// Anti-loop
			if strings.HasPrefix(text, "[TG]") || strings.HasPrefix(text, "[MAX]") {
				continue
			}

			prefix := b.repo.HasPrefix("max", chatID)
			caption := formatMaxCaption(msgUpd, prefix)

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
					continue
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
	}
}
