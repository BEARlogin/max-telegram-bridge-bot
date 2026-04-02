package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
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
				maxChatID, linked := b.repo.GetMaxChat(edited.Chat.ID)
				if !linked {
					continue
				}

				// Если edit содержит медиа — отправляем как новое сообщение
				hasMedia := edited.Photo != nil || edited.Video != nil || edited.Document != nil ||
					edited.Animation != nil || edited.Sticker != nil || edited.Voice != nil || edited.Audio != nil
				if hasMedia {
					prefix := b.repo.HasPrefix("tg", edited.Chat.ID)
					caption := formatTgCaption(edited, prefix, b.cfg.MessageNewline)
					go b.forwardTgToMax(ctx, edited, maxChatID, caption)
					continue
				}

				// Текстовый edit
				maxMsgID, ok := b.repo.LookupMaxMsgID(edited.Chat.ID, edited.MessageID)
				if !ok {
					continue
				}
				prefix := b.repo.HasPrefix("tg", edited.Chat.ID)
				fwd := formatTgMessage(edited, prefix, b.cfg.MessageNewline)
				if fwd == "" {
					continue
				}
				m := maxbot.NewMessage().SetChat(maxChatID).SetText(fwd)
				if err := b.maxApi.Messages.EditMessage(ctx, maxMsgID, m); err != nil {
					slog.Error("TG→MAX edit failed", "err", err, "uid", tgUserID(edited), "tgChat", edited.Chat.ID)
				} else {
					slog.Info("TG→MAX edited", "mid", maxMsgID, "uid", tgUserID(edited), "tgChat", edited.Chat.ID)
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
			slog.Debug("TG msg received", "uid", tgUserID(msg), "chat", msg.Chat.ID, "type", msg.Chat.Type)

			// Запоминаем юзера при личном сообщении
			if msg.Chat.Type == "private" && msg.From != nil {
				b.repo.TouchUser(msg.From.ID, "tg", msg.From.UserName, msg.From.FirstName)
			}

			if text == "/whoami" {
				b.tgBot.Send(tgbotapi.NewMessage(msg.Chat.ID,
					"MaxTelegramBridgeBot — мост между Telegram и MAX.\n"+
						"Автор: Andrey Lugovskoy (@BEARlogin)\n"+
						"Исходники: https://github.com/BEARlogin/max-telegram-bridge-bot\n"+
						"Лицензия: CC BY-NC 4.0"))
				continue
			}

			if text == "/start" || text == "/help" {
				b.tgBot.Send(tgbotapi.NewMessage(msg.Chat.ID,
					"Бот-мост между Telegram и MAX.\n\n"+
						"Команды (группы):\n"+
						"/bridge — создать ключ для связки чатов\n"+
						"/bridge <ключ> — связать этот чат с MAX-чатом по ключу\n"+
						"/bridge prefix on/off — включить/выключить префикс [TG]/[MAX]\n"+
						"/unbridge — удалить связку\n\n"+
						"Кросспостинг каналов:\n"+
						"1. Добавьте бота админом в оба канала (с правом постинга)\n"+
						"2. Перешлите пост из TG-канала в личку TG-бота\n"+
						"3. Бот покажет ID — скопируйте\n"+
						"4. В личке MAX-бота: /crosspost <TG_ID>\n"+
						"5. Перешлите пост из MAX-канала → готово!\n\n"+
						"/crosspost — список всех связок с кнопками управления\n"+
						"Управление: перешлите пост из связанного канала → кнопки\n\n"+
						"Автозамены в кросспостинге:\n"+
						"В настройках связки (кнопка 🔄) можно добавить замены текста.\n"+
						"Формат: текст | замена  или  /regex/ | замена\n"+
						"Можно заменять только в ссылках или во всём тексте.\n\n"+
						"Как связать группы:\n"+
						"1. Добавьте бота в оба чата\n"+
						"   TG: "+b.cfg.TgBotURL+"\n"+
						"   MAX: "+b.cfg.MaxBotURL+"\n"+
						"2. В MAX сделайте бота админом группы\n"+
						"3. В одном из чатов отправьте /bridge\n"+
						"4. Бот выдаст ключ — отправьте /bridge <ключ> в другом чате\n"+
						"5. Готово!\n\n"+
						"Поддержка: https://github.com/BEARlogin/max-telegram-bridge-bot/issues"))
				continue
			}

			// Обработка ввода замены (если юзер в режиме ожидания)
			if msg.Chat.Type == "private" && msg.From != nil && !strings.HasPrefix(text, "/") {
				if w, ok := b.getReplWait(msg.From.ID); ok {
					b.clearReplWait(msg.From.ID)
					rule, valid := parseReplacementInput(text)
					if !valid {
						b.tgBot.Send(tgbotapi.NewMessage(msg.Chat.ID, "Неверный формат. Используйте:\n<code>from | to</code>\nили\n<code>/regex/ | to</code>"))
						continue
					}
					rule.Target = w.target
					repl := b.repo.GetCrosspostReplacements(w.maxChatID)
					if w.direction == "tg>max" {
						repl.TgToMax = append(repl.TgToMax, rule)
					} else {
						repl.MaxToTg = append(repl.MaxToTg, rule)
					}
					if err := b.repo.SetCrosspostReplacements(w.maxChatID, repl); err != nil {
						slog.Error("save replacements failed", "err", err)
						b.tgBot.Send(tgbotapi.NewMessage(msg.Chat.ID, "Ошибка сохранения."))
						continue
					}
					ruleType := "строка"
					if rule.Regex {
						ruleType = "regex"
					}
					dirLabel := "TG → MAX"
					if w.direction == "max>tg" {
						dirLabel = "MAX → TG"
					}
					m := tgbotapi.NewMessage(msg.Chat.ID,
						fmt.Sprintf("Замена добавлена (%s, %s):\n<code>%s</code> → <code>%s</code>", dirLabel, ruleType, rule.From, rule.To))
					m.ParseMode = "HTML"
					b.tgBot.Send(m)
					continue
				}
			}

			// /crosspost в личке TG — показать список связок
			if msg.Chat.Type == "private" && text == "/crosspost" {
				if !b.checkUserAllowed(msg.Chat.ID, msg.From.ID) {
					continue
				}
				links := b.repo.ListCrossposts(msg.From.ID)
				if len(links) == 0 {
					b.tgBot.Send(tgbotapi.NewMessage(msg.Chat.ID,
						"Нет активных связок.\n\nНастройка: перешлите пост из TG-канала сюда, затем в MAX-боте /crosspost <ID>"))
				} else {
					for _, l := range links {
						kb := tgCrosspostKeyboard(l.Direction, l.MaxChatID)
						tgTitle := b.tgChatTitle(l.TgChatID)
						statusText := tgCrosspostStatusText(tgTitle, l.Direction)
						if tgTitle == "" {
							statusText += fmt.Sprintf("\nTG: %d ↔ MAX: %d", l.TgChatID, l.MaxChatID)
						} else {
							statusText += fmt.Sprintf("\nTG: «%s» (%d)\nMAX: %d", tgTitle, l.TgChatID, l.MaxChatID)
						}
						m := tgbotapi.NewMessage(msg.Chat.ID, statusText)
						m.ReplyMarkup = kb
						b.tgBot.Send(m)
					}
				}
				continue
			}

			// Пересланное сообщение из канала → показать ID или управление (только в личке)
			if msg.Chat.Type == "private" && msg.ForwardFromChat != nil && msg.ForwardFromChat.Type == "channel" {
				if !b.checkUserAllowed(msg.Chat.ID, msg.From.ID) {
					continue
				}
				channelID := msg.ForwardFromChat.ID
				channelTitle := msg.ForwardFromChat.Title

				// Запоминаем TG user ID для этого канала (для owner при pairing)
				b.cpTgOwnerMu.Lock()
				b.cpTgOwner[channelID] = msg.From.ID
				b.cpTgOwnerMu.Unlock()
				slog.Info("TG crosspost forward", "tgUser", msg.From.ID, "tgChannel", channelID)

				// Проверяем, уже связан ли канал
				if maxChatID, direction, ok := b.repo.GetCrosspostMaxChat(channelID); ok {
					text := tgCrosspostStatusText(channelTitle, direction)
					kb := tgCrosspostKeyboard(direction, maxChatID)
					m := tgbotapi.NewMessage(msg.Chat.ID, text)
					m.ReplyMarkup = kb
					b.tgBot.Send(m)
					continue
				}

				cpMsg := tgbotapi.NewMessage(msg.Chat.ID,
					fmt.Sprintf("TG-канал «%s»\nID: <code>%d</code>\n\nВ личке MAX-бота напишите:\n<code>/crosspost %d</code>\n\nMAX-бот: %s\n\nЗатем перешлите пост из MAX-канала в личку MAX-бота.", channelTitle, channelID, channelID, b.cfg.MaxBotURL))
				cpMsg.ParseMode = "HTML"
				b.tgBot.Send(cpMsg)
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
				if !b.checkUserAllowed(msg.Chat.ID, tgUserID(msg)) {
					continue
				}
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
				if !b.checkUserAllowed(msg.Chat.ID, tgUserID(msg)) {
					continue
				}
				if isGroup && !isAdmin {
					b.tgBot.Send(tgbotapi.NewMessage(msg.Chat.ID, "Эта команда доступна только админам группы."))
					continue
				}
				key := strings.TrimSpace(strings.TrimPrefix(text, "/bridge"))
				paired, generatedKey, err := b.repo.Register(key, "tg", msg.Chat.ID, 0)
				if err != nil {
					slog.Error("register failed", "err", err)
					continue
				}

				if paired {
					b.tgBot.Send(tgbotapi.NewMessage(msg.Chat.ID, "Связано! Сообщения теперь пересылаются."))
					slog.Info("paired", "platform", "tg", "chat", msg.Chat.ID, "key", key)
				} else if generatedKey != "" {
					keyMsg := tgbotapi.NewMessage(msg.Chat.ID,
						fmt.Sprintf("Ключ для связки: <code>%s</code>\n\nОтправьте в MAX-чате:\n<code>/bridge %s</code>\n\nMAX-бот: %s", generatedKey, generatedKey, b.cfg.MaxBotURL))
					keyMsg.ParseMode = "HTML"
					b.tgBot.Send(keyMsg)
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
				if !b.checkUserAllowed(msg.Chat.ID, tgUserID(msg)) {
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
			caption := formatTgCaption(msg, prefix, b.cfg.MessageNewline)

			// Проверяем anti-loop
			checkText := msg.Text
			if checkText == "" {
				checkText = msg.Caption
			}
			if strings.HasPrefix(checkText, "[MAX]") || strings.HasPrefix(checkText, "[TG]") {
				continue
			}

			// Media group (альбом) — буферизуем и отправляем вместе
			if msg.MediaGroupID != "" {
				videoID := ""
				if msg.Video != nil {
					videoID = msg.Video.FileID
				}
				go b.bufferMediaGroup(ctx, msg.MediaGroupID, mediaGroupItem{
					photoSizes:  msg.Photo,
					videoFileID: videoID,
					caption:     caption,
					replyToMsg:  msg.ReplyToMessage,
					entities:    msg.CaptionEntities,
					msg:         msg,
				})
				continue
			}

			go b.forwardTgToMax(ctx, msg, maxChatID, caption)
		}
	}
}

func tgUserID(msg *tgbotapi.Message) int64 {
	if msg.From != nil {
		return msg.From.ID
	}
	return 0
}

// forwardTgToMax пересылает TG-сообщение (текст/медиа) в MAX-чат.
func (b *Bridge) forwardTgToMax(ctx context.Context, msg *tgbotapi.Message, maxChatID int64, caption string) {
	if b.cbBlocked(maxChatID) {
		return
	}

	uid := tgUserID(msg)

	// checkSize returns true and sends warning if file exceeds TG_MAX_FILE_SIZE_MB limit.
	// fileSize=0 means the size is unknown (old TG messages may omit it) — we skip the check.
	checkSize := func(fileSize int, fileName string) bool {
		limit := b.cfg.TgMaxFileSizeMB
		if limit <= 0 || fileSize <= 0 || fileSize <= limit*1024*1024 {
			return false
		}
		warn := fmt.Sprintf("⚠️ Файл слишком большой для пересылки (%s). Максимальный размер файла %d МБ.",
			formatFileSize(fileSize), limit)
		if fileName != "" {
			warn = fmt.Sprintf("⚠️ Файл \"%s\" слишком большой для пересылки (%s). Максимальный размер файла %d МБ.",
				fileName, formatFileSize(fileSize), limit)
		}
		b.tgBot.Send(tgbotapi.NewMessage(msg.Chat.ID, warn))
		return true
	}

	// Определяем медиа
	var mediaToken string
	var mediaAttType string // "video", "file", "audio"

	if msg.Photo != nil {
		photo := msg.Photo[len(msg.Photo)-1]
		if checkSize(photo.FileSize, "") {
			return
		}
		m := maxbot.NewMessage().SetChat(maxChatID).SetText(caption)
		if b.cfg.TgAPIURL != "" {
			// Custom TG API — MAX не может скачать по URL, скачиваем и загружаем через reader
			if uploaded, err := b.uploadTgPhotoToMax(ctx, photo.FileID); err == nil {
				m.AddPhoto(uploaded)
			} else {
				slog.Error("TG→MAX photo upload failed", "err", err)
				b.tgBot.Send(tgbotapi.NewMessage(msg.Chat.ID, "Не удалось отправить фото в MAX."))
				return
			}
		} else if fileURL, err := b.tgFileURL(photo.FileID); err == nil {
			if uploaded, err := b.maxApi.Uploads.UploadPhotoFromUrl(ctx, fileURL); err == nil {
				m.AddPhoto(uploaded)
			} else {
				slog.Error("TG→MAX photo upload failed", "err", err)
				b.tgBot.Send(tgbotapi.NewMessage(msg.Chat.ID, "Не удалось отправить фото в MAX."))
				return
			}
		}
		if msg.ReplyToMessage != nil {
			if maxReplyID, ok := b.repo.LookupMaxMsgID(msg.Chat.ID, msg.ReplyToMessage.MessageID); ok {
				m.SetReply(caption, maxReplyID)
			}
		}
		slog.Info("TG→MAX sending photo", "uid", uid, "tgChat", msg.Chat.ID, "maxChat", maxChatID)
		result, err := b.maxApi.Messages.SendWithResult(ctx, m)
		if err != nil {
			slog.Error("TG→MAX send failed", "err", err, "uid", uid, "tgChat", msg.Chat.ID, "maxChat", maxChatID)
			if b.cbFail(maxChatID) {
				b.tgBot.Send(tgbotapi.NewMessage(msg.Chat.ID,
					fmt.Sprintf("Не удалось переслать в MAX. Пересылка приостановлена на %d мин. Проверьте, что бот добавлен в MAX-чат и является админом.", int(cbCooldown.Minutes()))))
			}
		} else {
			b.cbSuccess(maxChatID)
			slog.Info("TG→MAX sent", "mid", result.Body.Mid)
			b.repo.SaveMsg(msg.Chat.ID, msg.MessageID, maxChatID, result.Body.Mid)
		}
		return
	} else if msg.Animation != nil {
		// GIF в Telegram — это mp4 в поле Animation
		name := "animation.mp4"
		if msg.Animation.FileName != "" {
			name = msg.Animation.FileName
		}
		if checkSize(msg.Animation.FileSize, name) {
			return
		}
		if uploaded, err := b.uploadTgMediaToMax(ctx, msg.Animation.FileID, maxschemes.VIDEO, name); err == nil {
			mediaToken = uploaded.Token
			mediaAttType = "video"
		} else {
			slog.Error("TG→MAX gif upload failed", "err", err)
			b.tgBot.Send(tgbotapi.NewMessage(msg.Chat.ID, fmt.Sprintf("Не удалось отправить GIF \"%s\" в MAX.", name)))
			return
		}
	} else if msg.Sticker != nil {
		// Стикеры: обычные — WebP (фото), анимированные — TGS/WEBM
		if msg.Sticker.IsAnimated {
			if checkSize(msg.Sticker.FileSize, "sticker.webm") {
				return
			}
			if uploaded, err := b.uploadTgMediaToMax(ctx, msg.Sticker.FileID, maxschemes.FILE, "sticker.webm"); err == nil {
				mediaToken = uploaded.Token
				mediaAttType = "video"
			} else {
				slog.Error("TG→MAX sticker upload failed", "err", err)
				b.tgBot.Send(tgbotapi.NewMessage(msg.Chat.ID, "Не удалось отправить стикер в MAX."))
				return
			}
		} else {
			// Обычный стикер WebP → отправляем как фото
			if fileURL, err := b.tgFileURL(msg.Sticker.FileID); err == nil {
				if uploaded, err := b.maxApi.Uploads.UploadPhotoFromUrl(ctx, fileURL); err == nil {
					m := maxbot.NewMessage().SetChat(maxChatID).SetText(caption)
					m.AddPhoto(uploaded)
					if msg.ReplyToMessage != nil {
						if maxReplyID, ok := b.repo.LookupMaxMsgID(msg.Chat.ID, msg.ReplyToMessage.MessageID); ok {
							m.SetReply(caption, maxReplyID)
						}
					}
					slog.Info("TG→MAX sending sticker as photo", "uid", uid, "tgChat", msg.Chat.ID)
					result, err := b.maxApi.Messages.SendWithResult(ctx, m)
					if err != nil {
						slog.Error("TG→MAX sticker send failed", "err", err)
						b.tgBot.Send(tgbotapi.NewMessage(msg.Chat.ID, "Не удалось отправить стикер в MAX."))
					} else {
						slog.Info("TG→MAX sent", "mid", result.Body.Mid)
						b.repo.SaveMsg(msg.Chat.ID, msg.MessageID, maxChatID, result.Body.Mid)
					}
					return
				} else {
					slog.Error("TG→MAX sticker photo upload failed", "err", err)
					b.tgBot.Send(tgbotapi.NewMessage(msg.Chat.ID, "Не удалось отправить стикер в MAX."))
					return
				}
			}
		}
	} else if msg.Video != nil {
		name := "video.mp4"
		if msg.Video.FileName != "" {
			name = msg.Video.FileName
		}
		if checkSize(msg.Video.FileSize, name) {
			return
		}
		if uploaded, err := b.uploadTgMediaToMax(ctx, msg.Video.FileID, maxschemes.VIDEO, name); err == nil {
			mediaToken = uploaded.Token
			mediaAttType = "video"
		} else {
			slog.Error("TG→MAX video upload failed", "err", err)
			b.tgBot.Send(tgbotapi.NewMessage(msg.Chat.ID, fmt.Sprintf("Не удалось отправить видео \"%s\" в MAX.", name)))
			return
		}
	} else if msg.VideoNote != nil {
		if checkSize(msg.VideoNote.FileSize, "circle.mp4") {
			return
		}
		if uploaded, err := b.uploadTgMediaToMax(ctx, msg.VideoNote.FileID, maxschemes.VIDEO, "circle.mp4"); err == nil {
			mediaToken = uploaded.Token
			mediaAttType = "video"
		} else {
			slog.Error("TG→MAX video note upload failed", "err", err)
			b.tgBot.Send(tgbotapi.NewMessage(msg.Chat.ID, "Не удалось отправить кружок в MAX."))
			return
		}
	} else if msg.Document != nil {
		name := msg.Document.FileName
		uploadType := maxschemes.FILE
		attType := "file"
		// Документ с video MIME → загружаем как видео
		if strings.HasPrefix(msg.Document.MimeType, "video/") {
			uploadType = maxschemes.VIDEO
			attType = "video"
			if name == "" {
				name = mimeToFilename("video", msg.Document.MimeType)
			}
		}
		if name == "" {
			name = mimeToFilename("document", msg.Document.MimeType)
		}
		if checkSize(msg.Document.FileSize, name) {
			return
		}
		// Pre-check расширения до отправки на CDN (если whitelist задан)
		if b.cfg.MaxAllowedExts != nil && attType == "file" {
			ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))
			if _, ok := b.cfg.MaxAllowedExts[ext]; !ok {
				b.tgBot.Send(tgbotapi.NewMessage(msg.Chat.ID,
					fmt.Sprintf("Файл \"%s\" не поддерживается в MAX (расширение .%s не разрешено).", name, ext)))
				return
			}
		}
		if uploaded, err := b.uploadTgMediaToMax(ctx, msg.Document.FileID, uploadType, name); err == nil {
			mediaToken = uploaded.Token
			mediaAttType = attType
		} else {
			var e *ErrForbiddenExtension
			if errors.As(err, &e) {
				b.tgBot.Send(tgbotapi.NewMessage(msg.Chat.ID,
					fmt.Sprintf("Файл \"%s\" не поддерживается в MAX (запрещённое расширение).", name)))
				return
			}
			slog.Error("TG→MAX file upload failed", "err", err)
			b.tgBot.Send(tgbotapi.NewMessage(msg.Chat.ID,
				fmt.Sprintf("Не удалось отправить файл \"%s\" в MAX.", name)))
			return
		}
	} else if msg.Voice != nil {
		if checkSize(msg.Voice.FileSize, "voice.ogg") {
			return
		}
		if uploaded, err := b.uploadTgMediaToMax(ctx, msg.Voice.FileID, maxschemes.AUDIO, "voice.ogg"); err == nil {
			mediaToken = uploaded.Token
			mediaAttType = "audio"
		} else {
			var e *ErrForbiddenExtension
			if errors.As(err, &e) {
				b.tgBot.Send(tgbotapi.NewMessage(msg.Chat.ID,
					fmt.Sprintf("Файл \"%s\" не поддерживается в MAX (запрещённое расширение).", e.Name)))
				return
			}
			slog.Error("TG→MAX voice upload failed", "err", err)
			b.tgBot.Send(tgbotapi.NewMessage(msg.Chat.ID, "Не удалось отправить голосовое сообщение в MAX."))
			return
		}
	} else if msg.Audio != nil {
		name := "audio.mp3"
		if msg.Audio.FileName != "" {
			name = msg.Audio.FileName
		}
		if checkSize(msg.Audio.FileSize, name) {
			return
		}
		// Pre-check расширения до отправки на CDN (если whitelist задан)
		if b.cfg.MaxAllowedExts != nil {
			ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))
			if _, ok := b.cfg.MaxAllowedExts[ext]; !ok {
				b.tgBot.Send(tgbotapi.NewMessage(msg.Chat.ID,
					fmt.Sprintf("Файл \"%s\" не поддерживается в MAX (расширение .%s не разрешено).", name, ext)))
				return
			}
		}
		if uploaded, err := b.uploadTgMediaToMax(ctx, msg.Audio.FileID, maxschemes.FILE, name); err == nil {
			mediaToken = uploaded.Token
			mediaAttType = "file"
		} else {
			var e *ErrForbiddenExtension
			if errors.As(err, &e) {
				b.tgBot.Send(tgbotapi.NewMessage(msg.Chat.ID,
					fmt.Sprintf("Файл \"%s\" не поддерживается в MAX (запрещённое расширение).", name)))
				return
			}
			slog.Error("TG→MAX audio upload failed", "err", err)
			b.tgBot.Send(tgbotapi.NewMessage(msg.Chat.ID, fmt.Sprintf("Не удалось отправить аудио \"%s\" в MAX.", name)))
			return
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

	// Конвертируем TG entities в markdown для MAX
	entities := msg.Entities
	if entities == nil {
		entities = msg.CaptionEntities
	}
	mdCaption := tgEntitiesToMarkdown(caption, entities)
	hasFormatting := mdCaption != caption

	var mid string
	var sendErr error

	if mediaAttType != "" {
		slog.Info("TG→MAX sending direct", "type", mediaAttType, "uid", uid, "tgChat", msg.Chat.ID, "maxChat", maxChatID)
		var format string
		if hasFormatting {
			format = "markdown"
		}
		mid, sendErr = b.sendMaxDirectFormatted(ctx, maxChatID, mdCaption, mediaAttType, mediaToken, replyTo, format)
	} else {
		var format string
		if hasFormatting {
			format = "markdown"
		}
		slog.Info("TG→MAX sending", "uid", uid, "tgChat", msg.Chat.ID, "maxChat", maxChatID)
		mid, sendErr = b.sendMaxDirectFormatted(ctx, maxChatID, mdCaption, "", "", replyTo, format)
	}

	if sendErr != nil {
		errStr := sendErr.Error()
		slog.Error("TG→MAX send failed", "err", errStr, "uid", uid, "tgChat", msg.Chat.ID, "maxChat", maxChatID)
		// 403/404 — permanent error, не ретраим
		if !strings.Contains(errStr, "403") && !strings.Contains(errStr, "404") && !strings.Contains(errStr, "chat.denied") {
			var format string
			if hasFormatting {
				format = "markdown"
			}
			b.enqueueTg2Max(msg.Chat.ID, msg.MessageID, maxChatID, mdCaption, mediaAttType, mediaToken, replyTo, format)
		}
		if b.cbFail(maxChatID) {
			b.tgBot.Send(tgbotapi.NewMessage(msg.Chat.ID,
				"MAX API недоступен. Сообщения в очереди, будут доставлены автоматически."))
		}
	} else {
		b.cbSuccess(maxChatID)
		slog.Info("TG→MAX sent", "mid", mid, "uid", uid, "tgChat", msg.Chat.ID, "maxChat", maxChatID)
		b.repo.SaveMsg(msg.Chat.ID, msg.MessageID, maxChatID, mid)
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

	// Применяем замены для TG→MAX
	repl := b.repo.GetCrosspostReplacements(maxChatID)
	if len(repl.TgToMax) > 0 {
		caption = applyReplacements(caption, repl.TgToMax)
	}

	// Media group (альбом) — буферизуем и отправляем вместе
	if msg.MediaGroupID != "" {
		videoID := ""
		if msg.Video != nil {
			videoID = msg.Video.FileID
		}
		go b.bufferMediaGroup(ctx, msg.MediaGroupID, mediaGroupItem{
			photoSizes:  msg.Photo,
			videoFileID: videoID,
			caption:     caption,
			replyToMsg:  msg.ReplyToMessage,
			entities:    msg.CaptionEntities,
			msg:         msg,
			maxChatID:   maxChatID,
			crosspost:   true,
		})
		return
	}

	go b.forwardTgToMax(ctx, msg, maxChatID, caption)
}

// handleTgCallback обрабатывает нажатия inline-кнопок (crosspost management).
func (b *Bridge) handleTgCallback(ctx context.Context, query *tgbotapi.CallbackQuery) {
	if query.Message == nil || query.From == nil {
		return
	}
	data := query.Data
	chatID := query.Message.Chat.ID
	msgID := query.Message.MessageID

	fromID := query.From.ID

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
		if !b.isCrosspostOwner(maxChatID, fromID) {
			b.tgBot.Request(tgbotapi.NewCallback(query.ID, "Только владелец связки может изменять настройки."))
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
		if !b.isCrosspostOwner(maxChatID, fromID) {
			b.tgBot.Request(tgbotapi.NewCallback(query.ID, "Только владелец связки может удалять."))
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

	// cpr:maxChatID — show replacements
	if strings.HasPrefix(data, "cpr:") {
		maxChatID, err := strconv.ParseInt(strings.TrimPrefix(data, "cpr:"), 10, 64)
		if err != nil {
			return
		}
		repl := b.repo.GetCrosspostReplacements(maxChatID)
		id := strconv.FormatInt(maxChatID, 10)
		// Удаляем сообщение со связкой
		b.tgBot.Request(tgbotapi.NewDeleteMessage(chatID, msgID))
		// Заголовок с кнопками добавления
		kb := tgReplacementsKeyboard(maxChatID)
		m := tgbotapi.NewMessage(chatID, formatReplacementsHeader(repl))
		m.ReplyMarkup = kb
		b.tgBot.Send(m)
		// Каждая замена — отдельное сообщение с кнопкой удаления
		for i, r := range repl.TgToMax {
			m := tgbotapi.NewMessage(chatID, formatReplacementItem(r, "tg>max"))
			m.ParseMode = "HTML"
			m.ReplyMarkup = tgReplItemKeyboard("tg>max", i, id, r.Target)
			b.tgBot.Send(m)
		}
		for i, r := range repl.MaxToTg {
			m := tgbotapi.NewMessage(chatID, formatReplacementItem(r, "max>tg"))
			m.ParseMode = "HTML"
			m.ReplyMarkup = tgReplItemKeyboard("max>tg", i, id, r.Target)
			b.tgBot.Send(m)
		}
		b.tgBot.Request(tgbotapi.NewCallback(query.ID, ""))
		return
	}

	// cprt:dir:index:target:maxChatID — toggle replacement target
	if strings.HasPrefix(data, "cprt:") {
		parts := strings.SplitN(strings.TrimPrefix(data, "cprt:"), ":", 4)
		if len(parts) != 4 {
			return
		}
		dir := parts[0]
		idx, err := strconv.Atoi(parts[1])
		if err != nil {
			return
		}
		newTarget := parts[2]
		maxChatID, err := strconv.ParseInt(parts[3], 10, 64)
		if err != nil {
			return
		}
		repl := b.repo.GetCrosspostReplacements(maxChatID)
		id := strconv.FormatInt(maxChatID, 10)
		var r *Replacement
		if dir == "tg>max" && idx < len(repl.TgToMax) {
			r = &repl.TgToMax[idx]
		} else if dir == "max>tg" && idx < len(repl.MaxToTg) {
			r = &repl.MaxToTg[idx]
		}
		if r == nil {
			return
		}
		r.Target = newTarget
		b.repo.SetCrosspostReplacements(maxChatID, repl)
		// Обновляем сообщение
		newText := formatReplacementItem(*r, dir)
		kb := tgReplItemKeyboard(dir, idx, id, r.Target)
		edit := tgbotapi.NewEditMessageTextAndMarkup(chatID, msgID, newText, kb)
		edit.ParseMode = "HTML"
		b.tgBot.Send(edit)
		label := "весь текст"
		if newTarget == "links" {
			label = "только ссылки"
		}
		b.tgBot.Request(tgbotapi.NewCallback(query.ID, "Тип: "+label))
		return
	}

	// cprd:dir:index:maxChatID — delete single replacement
	if strings.HasPrefix(data, "cprd:") {
		parts := strings.SplitN(strings.TrimPrefix(data, "cprd:"), ":", 3)
		if len(parts) != 3 {
			return
		}
		dir := parts[0]
		idx, err := strconv.Atoi(parts[1])
		if err != nil {
			return
		}
		maxChatID, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			return
		}
		repl := b.repo.GetCrosspostReplacements(maxChatID)
		if dir == "tg>max" && idx < len(repl.TgToMax) {
			repl.TgToMax = append(repl.TgToMax[:idx], repl.TgToMax[idx+1:]...)
		} else if dir == "max>tg" && idx < len(repl.MaxToTg) {
			repl.MaxToTg = append(repl.MaxToTg[:idx], repl.MaxToTg[idx+1:]...)
		}
		b.repo.SetCrosspostReplacements(maxChatID, repl)
		edit := tgbotapi.NewEditMessageText(chatID, msgID, "Замена удалена.")
		b.tgBot.Send(edit)
		b.tgBot.Request(tgbotapi.NewCallback(query.ID, "Удалено"))
		return
	}

	// cpra:dir:maxChatID — choose target (all or links)
	if strings.HasPrefix(data, "cpra:") {
		parts := strings.SplitN(strings.TrimPrefix(data, "cpra:"), ":", 2)
		if len(parts) != 2 {
			return
		}
		dir := parts[0]
		id := parts[1]
		dirLabel := "TG → MAX"
		if dir == "max>tg" {
			dirLabel = "MAX → TG"
		}
		kb := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("📝 Весь текст", "cprat:"+dir+":all:"+id),
				tgbotapi.NewInlineKeyboardButtonData("🔗 Только ссылки", "cprat:"+dir+":links:"+id),
			),
		)
		edit := tgbotapi.NewEditMessageTextAndMarkup(chatID, msgID,
			fmt.Sprintf("Добавление замены для %s.\nГде применять замену?", dirLabel), kb)
		b.tgBot.Send(edit)
		b.tgBot.Request(tgbotapi.NewCallback(query.ID, ""))
		return
	}

	// cprat:dir:target:maxChatID — set wait state with target
	if strings.HasPrefix(data, "cprat:") {
		parts := strings.SplitN(strings.TrimPrefix(data, "cprat:"), ":", 3)
		if len(parts) != 3 {
			return
		}
		dir := parts[0]
		target := parts[1]
		maxChatID, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			return
		}
		b.setReplWait(fromID, maxChatID, dir, target)
		edit := tgbotapi.NewEditMessageText(chatID, msgID,
			fmt.Sprintf("Отправьте правило замены:\n<code>from | to</code>\n\nДля регулярного выражения:\n<code>/regex/ | to</code>\n\nНапример:\n<code>utm_source=tg | utm_source=max</code>"))
		edit.ParseMode = "HTML"
		b.tgBot.Send(edit)
		b.tgBot.Request(tgbotapi.NewCallback(query.ID, ""))
		return
	}

	// cprc:maxChatID — clear all replacements
	if strings.HasPrefix(data, "cprc:") {
		maxChatID, err := strconv.ParseInt(strings.TrimPrefix(data, "cprc:"), 10, 64)
		if err != nil {
			return
		}
		b.repo.SetCrosspostReplacements(maxChatID, CrosspostReplacements{})
		repl := b.repo.GetCrosspostReplacements(maxChatID)
		kb := tgReplacementsKeyboard(maxChatID)
		edit := tgbotapi.NewEditMessageTextAndMarkup(chatID, msgID, formatReplacementsHeader(repl), kb)
		b.tgBot.Send(edit)
		b.tgBot.Request(tgbotapi.NewCallback(query.ID, "Очищено"))
		return
	}

	// cprb:maxChatID — back to crosspost management
	if strings.HasPrefix(data, "cprb:") {
		maxChatID, err := strconv.ParseInt(strings.TrimPrefix(data, "cprb:"), 10, 64)
		if err != nil {
			return
		}
		_, direction, ok := b.repo.GetCrosspostTgChat(maxChatID)
		if !ok {
			return
		}
		title := parseTgCrosspostTitle(query.Message.Text)
		text := tgCrosspostStatusText(title, direction) + fmt.Sprintf("\nTG: ↔ MAX: %d", maxChatID)
		kb := tgCrosspostKeyboard(direction, maxChatID)
		edit := tgbotapi.NewEditMessageTextAndMarkup(chatID, msgID, text, kb)
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
		if !b.isCrosspostOwner(maxChatID, fromID) {
			b.tgBot.Request(tgbotapi.NewCallback(query.ID, "Только владелец связки может удалять."))
			return
		}
		slog.Info("TG crosspost unlink", "maxChatID", maxChatID, "by", fromID)
		b.repo.UnpairCrosspost(maxChatID, fromID)
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
			tgbotapi.NewInlineKeyboardButtonData("🔄 Замены", "cpr:"+id),
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
