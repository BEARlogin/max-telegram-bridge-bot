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
		wh, err := tgbotapi.NewWebhook(b.cfg.WebhookURL)
		if err != nil {
			slog.Error("TG webhook config error", "err", err)
			return
		}
		if _, err := b.tgBot.Request(wh); err != nil {
			slog.Error("TG set webhook failed", "err", err)
			return
		}
		updates = b.tgBot.ListenForWebhook("/tg-webhook")
		slog.Info("TG webhook mode", "url", b.cfg.WebhookURL)
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
			slog.Debug("TG msg received", "from", msg.From.FirstName, "chat", msg.Chat.ID, "text", text)

			if text == "/start" || text == "/help" {
				b.tgBot.Send(tgbotapi.NewMessage(msg.Chat.ID,
					"Бот-мост между Telegram и MAX.\n\n"+
						"Команды:\n"+
						"/bridge — создать ключ для связки чатов\n"+
						"/bridge <ключ> — связать этот чат с MAX-чатом по ключу\n"+
						"/bridge prefix on/off — включить/выключить префикс [TG]/[MAX]\n"+
						"/unbridge — удалить связку\n\n"+
						"Как связать чаты:\n"+
						"1. Добавьте бота в оба чата\n"+
						"   TG: "+b.cfg.TgBotURL+"\n"+
						"   MAX: "+b.cfg.MaxBotURL+"\n"+
						"2. В MAX сделайте бота админом группы\n"+
						"3. В одном из чатов отправьте /bridge\n"+
						"4. Бот выдаст ключ — отправьте /bridge <ключ> в другом чате\n"+
						"5. Готово! Сообщения пересылаются в обе стороны."))
				continue
			}

			// /bridge prefix on/off
			if text == "/bridge prefix on" || text == "/bridge prefix off" {
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
				slog.Info("TG→MAX sending photo", "caption", caption)
				result, err := b.maxApi.Messages.SendWithResult(ctx, m)
				if err != nil {
					slog.Error("TG→MAX send failed", "err", err)
				} else {
					slog.Info("TG→MAX sent", "mid", result.Body.Mid)
					b.repo.SaveMsg(msg.Chat.ID, msg.MessageID, maxChatID, result.Body.Mid)
				}
				continue
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
					continue
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
				slog.Info("TG→MAX sending direct", "caption", caption, "type", mediaAttType)
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
				slog.Info("TG→MAX sending", "caption", caption)
				result, err := b.maxApi.Messages.SendWithResult(ctx, m)
				if err != nil {
					slog.Error("TG→MAX send failed", "err", err)
				} else {
					slog.Info("TG→MAX sent", "mid", result.Body.Mid)
					b.repo.SaveMsg(msg.Chat.ID, msg.MessageID, maxChatID, result.Body.Mid)
				}
			}
		}
	}
}
