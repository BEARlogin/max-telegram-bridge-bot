package main

import (
	"context"
	"errors"
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
		updateTypes := []string{
			"message_created", "message_edited", "message_removed",
			"message_callback", "bot_added", "bot_removed",
			"user_added", "user_removed", "chat_title_changed",
		}
		if _, err := b.maxApi.Subscriptions.Subscribe(ctx, whURL, updateTypes, ""); err != nil {
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
					slog.Error("MAX→TG delete failed", "err", err, "maxMid", delUpd.MessageId, "tgChat", tgChatID)
				} else {
					slog.Info("MAX→TG deleted", "tgMsg", tgMsgID, "tgChat", tgChatID)
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
				if strings.HasPrefix(text, "[TG]") || strings.HasPrefix(text, "[MAX]") {
					continue
				}
				var fwd string
				if prefix {
					fwd = formatAttribution("[MAX] "+name, text, b.cfg.MessageNewline)
				} else {
					fwd = formatAttribution(name, text, b.cfg.MessageNewline)
				}

				// Проверяем вложения в edit — если есть медиа, используем editMessageMedia
				var mediaURL, mediaType string
				for _, att := range editUpd.Message.Body.Attachments {
					switch a := att.(type) {
					case *maxschemes.PhotoAttachment:
						if a.Payload.Url != "" {
							mediaURL, mediaType = a.Payload.Url, "photo"
						}
					case *maxschemes.VideoAttachment:
						if a.Payload.Url != "" {
							mediaURL, mediaType = a.Payload.Url, "video"
						}
					case *maxschemes.FileAttachment:
						if a.Payload.Url != "" {
							mediaURL, mediaType = a.Payload.Url, "document"
						}
					}
					if mediaURL != "" {
						break
					}
				}

				if mediaURL != "" {
					// Скачиваем медиа и отправляем editMessageMedia
					data, name, dlErr := b.downloadURLWithLimit(mediaURL, b.cfg.maxMaxFileBytes())
					if dlErr != nil {
						slog.Error("MAX→TG edit media download failed", "err", dlErr)
					} else {
						fb := tgbotapi.FileBytes{Name: name, Bytes: data}
						var media interface{}
						switch mediaType {
						case "photo":
							p := tgbotapi.NewInputMediaPhoto(fb)
							p.Caption = fwd
							media = p
						case "video":
							v := tgbotapi.NewInputMediaVideo(fb)
							v.Caption = fwd
							media = v
						case "document":
							d := tgbotapi.NewInputMediaDocument(fb)
							d.Caption = fwd
							media = d
						}
						editMedia := tgbotapi.EditMessageMediaConfig{
							BaseEdit: tgbotapi.BaseEdit{
								ChatID:    tgChatID,
								MessageID: tgMsgID,
							},
							Media: media,
						}
						if _, err := b.tgBot.Send(editMedia); err != nil {
							slog.Error("MAX→TG edit media failed", "err", err, "uid", editUpd.Message.Sender.UserId)
							// Fallback — отправляем как новое сообщение
							go b.sendTgMediaFromURL(tgChatID, mediaURL, mediaType, fwd, "", 0, b.cfg.maxMaxFileBytes())
						} else {
							slog.Info("MAX→TG edited media", "tgMsg", tgMsgID, "type", mediaType, "uid", editUpd.Message.Sender.UserId)
						}
						continue
					}
				}

				if text == "" {
					continue
				}
				editMsg := tgbotapi.NewEditMessageText(tgChatID, tgMsgID, fwd)
				if _, err := b.tgBot.Send(editMsg); err != nil {
					slog.Error("MAX→TG edit failed", "err", err, "uid", editUpd.Message.Sender.UserId, "maxChat", editUpd.Message.Recipient.ChatId)
				} else {
					slog.Info("MAX→TG edited", "tgMsg", tgMsgID, "uid", editUpd.Message.Sender.UserId, "maxChat", editUpd.Message.Recipient.ChatId)
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

			slog.Debug("MAX msg received", "uid", msgUpd.Message.Sender.UserId, "chat", chatID, "type", msgUpd.Message.Recipient.ChatType)

			// Запоминаем юзера при личном сообщении
			if isDialog && msgUpd.Message.Sender.UserId != 0 {
				b.repo.TouchUser(msgUpd.Message.Sender.UserId, "max", msgUpd.Message.Sender.Username, msgUpd.Message.Sender.Name)
			}

			if text == "/whoami" {
				m := maxbot.NewMessage().SetChat(chatID).SetText(
					"MaxTelegramBridgeBot — мост между Telegram и MAX.\n" +
						"Автор: Andrey Lugovskoy (@BEARlogin)\n" +
						"Исходники: https://github.com/BEARlogin/max-telegram-bridge-bot\n" +
						"Лицензия: CC BY-NC 4.0")
				b.maxApi.Messages.Send(ctx, m)
				continue
			}

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
						"1. Добавьте бота админом в оба канала (с правом постинга)\n" +
						"   TG: " + b.cfg.TgBotURL + "\n" +
						"2. Перешлите пост из TG-канала в личку TG-бота\n" +
						"3. Бот покажет ID канала — скопируйте\n" +
						"4. Здесь в личке напишите: /crosspost <TG_ID>\n" +
						"5. Перешлите пост из MAX-канала сюда → готово!\n\n" +
						"/crosspost — список всех связок с кнопками управления\n" +
						"Управление: перешлите пост из связанного канала → кнопки\n\n" +
						"Автозамены в кросспостинге:\n" +
						"В настройках связки (кнопка 🔄) можно добавить замены текста.\n" +
						"Формат: текст | замена  или  /regex/ | замена\n" +
						"Можно заменять только в ссылках или во всём тексте.\n\n" +
						"Как связать группы:\n" +
						"1. Добавьте бота в оба чата\n" +
						"   MAX: " + b.cfg.MaxBotURL + "\n" +
						"2. В одном из чатов отправьте /bridge\n" +
						"3. Бот выдаст ключ — отправьте его в другом чате\n" +
						"4. Готово!\n\n" +
						"Поддержка: https://github.com/BEARlogin/max-telegram-bridge-bot/issues")
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
				paired, generatedKey, err := b.repo.Register(key, "max", chatID, 0)
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
						SetText(fmt.Sprintf("Ключ для связки: %s\n\nОтправьте в Telegram-чате:\n/bridge %s\n\nTG-бот: %s", generatedKey, generatedKey, b.cfg.TgBotURL))
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

			// Обработка ввода замены (если юзер в режиме ожидания)
			if isDialog && !strings.HasPrefix(text, "/") && msgUpd.Message.Sender.UserId != 0 {
				if w, ok := b.getReplWait(msgUpd.Message.Sender.UserId); ok {
					b.clearReplWait(msgUpd.Message.Sender.UserId)
					rule, valid := parseReplacementInput(text)
					if !valid {
						m := maxbot.NewMessage().SetChat(chatID).SetText("Неверный формат. Используйте:\nfrom | to\nили\n/regex/ | to")
						b.maxApi.Messages.Send(ctx, m)
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
						m := maxbot.NewMessage().SetChat(chatID).SetText("Ошибка сохранения.")
						b.maxApi.Messages.Send(ctx, m)
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
					m := maxbot.NewMessage().SetChat(chatID).SetText(
						fmt.Sprintf("Замена добавлена (%s, %s):\n%s → %s", dirLabel, ruleType, rule.From, rule.To))
					b.maxApi.Messages.Send(ctx, m)
					continue
				}
			}

			// === Crosspost команды (только в личке бота) ===

			// /crosspost <tg_channel_id> — начало настройки (только в личке)
			if isDialog && strings.HasPrefix(text, "/crosspost") {
				arg := strings.TrimSpace(strings.TrimPrefix(text, "/crosspost"))
				if arg == "" {
					links := b.repo.ListCrossposts(msgUpd.Message.Sender.UserId)
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
							tgTitle := b.tgChatTitle(l.TgChatID)
							statusText := maxCrosspostStatusText(l.TgChatID, l.Direction)
							if tgTitle != "" {
								statusText = fmt.Sprintf("TG: «%s» (%d)\n", tgTitle, l.TgChatID) + statusText
							}
							m := maxbot.NewMessage().SetChat(chatID).
								SetText(statusText).
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

					// Достаём TG owner ID (кто переслал пост из TG-канала в TG-бот)
				b.cpTgOwnerMu.Lock()
				tgOwnerID := b.cpTgOwner[tgChannelID]
				b.cpTgOwnerMu.Unlock()

				if err := b.repo.PairCrosspost(tgChannelID, maxChannelID, msgUpd.Message.Sender.UserId, tgOwnerID); err != nil {
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
					slog.Info("crosspost paired", "tg", tgChannelID, "max", maxChannelID, "maxOwner", msgUpd.Message.Sender.UserId, "tgOwner", tgOwnerID)
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
					caption := formatMaxCaption(msgUpd, prefix, b.cfg.MessageNewline)
					go b.forwardMaxToTg(ctx, msgUpd, tgChatID, caption)
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

			// Применяем замены для MAX→TG
			repl := b.repo.GetCrosspostReplacements(chatID)
			if len(repl.MaxToTg) > 0 {
				caption = applyReplacements(caption, repl.MaxToTg)
			}

			go b.forwardMaxToTg(ctx, msgUpd, tgChatID, caption)
		}
	}
}

// handleMaxCallback обрабатывает нажатия inline-кнопок (crosspost management).
func (b *Bridge) handleMaxCallback(ctx context.Context, cbUpd *maxschemes.MessageCallbackUpdate) {
	data := cbUpd.Callback.Payload
	callbackID := cbUpd.Callback.CallbackID
	userID := cbUpd.Callback.User.UserId

	slog.Debug("MAX callback", "uid", userID, "data", data)

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
		if !b.isCrosspostOwner(maxChatID, userID) {
			b.maxApi.Messages.AnswerOnCallback(ctx, callbackID, &maxschemes.CallbackAnswer{
				Notification: "Только владелец связки может изменять настройки.",
			})
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
		if !b.isCrosspostOwner(maxChatID, userID) {
			b.maxApi.Messages.AnswerOnCallback(ctx, callbackID, &maxschemes.CallbackAnswer{
				Notification: "Только владелец связки может удалять.",
			})
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
		if !b.isCrosspostOwner(maxChatID, userID) {
			b.maxApi.Messages.AnswerOnCallback(ctx, callbackID, &maxschemes.CallbackAnswer{
				Notification: "Только владелец связки может удалять.",
			})
			return
		}
		slog.Info("MAX crosspost unlink", "maxChatID", maxChatID, "by", userID)
		b.repo.UnpairCrosspost(maxChatID, userID)
		body := &maxschemes.NewMessageBody{Text: "Кросспостинг удалён."}
		b.maxApi.Messages.AnswerOnCallback(ctx, callbackID, &maxschemes.CallbackAnswer{
			Message:      body,
			Notification: "Удалено",
		})
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
		// Заголовок с кнопками добавления
		kb := maxReplacementsKeyboard(b.maxApi, maxChatID)
		body := &maxschemes.NewMessageBody{
			Text:        formatReplacementsHeader(repl),
			Attachments: []interface{}{maxschemes.NewInlineKeyboardAttachmentRequest(kb.Build())},
		}
		b.maxApi.Messages.AnswerOnCallback(ctx, callbackID, &maxschemes.CallbackAnswer{Message: body})
		// Каждая замена — отдельное сообщение с кнопками
		for i, r := range repl.TgToMax {
			dkb := maxReplItemKeyboard(b.maxApi, "tg>max", i, id, r.Target)
			m := maxbot.NewMessage().SetChat(cbUpd.Callback.User.UserId).
				SetText(formatReplacementItem(r, "tg>max")).
				AddKeyboard(dkb)
			b.maxApi.Messages.Send(ctx, m)
		}
		for i, r := range repl.MaxToTg {
			dkb := maxReplItemKeyboard(b.maxApi, "max>tg", i, id, r.Target)
			m := maxbot.NewMessage().SetChat(cbUpd.Callback.User.UserId).
				SetText(formatReplacementItem(r, "max>tg")).
				AddKeyboard(dkb)
			b.maxApi.Messages.Send(ctx, m)
		}
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
		newText := formatReplacementItem(*r, dir)
		dkb := maxReplItemKeyboard(b.maxApi, dir, idx, id, r.Target)
		body := &maxschemes.NewMessageBody{
			Text:        newText,
			Attachments: []interface{}{maxschemes.NewInlineKeyboardAttachmentRequest(dkb.Build())},
		}
		label := "весь текст"
		if newTarget == "links" {
			label = "только ссылки"
		}
		b.maxApi.Messages.AnswerOnCallback(ctx, callbackID, &maxschemes.CallbackAnswer{
			Message:      body,
			Notification: "Тип: " + label,
		})
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
		body := &maxschemes.NewMessageBody{Text: "Замена удалена."}
		b.maxApi.Messages.AnswerOnCallback(ctx, callbackID, &maxschemes.CallbackAnswer{
			Message:      body,
			Notification: "Удалено",
		})
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
		kb := b.maxApi.Messages.NewKeyboardBuilder()
		kb.AddRow().
			AddCallback("📝 Весь текст", maxschemes.DEFAULT, "cprat:"+dir+":all:"+id).
			AddCallback("🔗 Только ссылки", maxschemes.DEFAULT, "cprat:"+dir+":links:"+id)
		body := &maxschemes.NewMessageBody{
			Text:        fmt.Sprintf("Добавление замены для %s.\nГде применять замену?", dirLabel),
			Attachments: []interface{}{maxschemes.NewInlineKeyboardAttachmentRequest(kb.Build())},
		}
		b.maxApi.Messages.AnswerOnCallback(ctx, callbackID, &maxschemes.CallbackAnswer{Message: body})
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
		b.setReplWait(userID, maxChatID, dir, target)
		body := &maxschemes.NewMessageBody{
			Text: "Отправьте правило замены:\nfrom | to\n\nДля регулярного выражения:\n/regex/ | to\n\nНапример:\nutm_source=tg | utm_source=max",
		}
		b.maxApi.Messages.AnswerOnCallback(ctx, callbackID, &maxschemes.CallbackAnswer{Message: body})
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
		kb := maxReplacementsKeyboard(b.maxApi, maxChatID)
		body := &maxschemes.NewMessageBody{
			Text:        formatReplacementsHeader(repl),
			Attachments: []interface{}{maxschemes.NewInlineKeyboardAttachmentRequest(kb.Build())},
		}
		b.maxApi.Messages.AnswerOnCallback(ctx, callbackID, &maxschemes.CallbackAnswer{
			Message:      body,
			Notification: "Очищено",
		})
		return
	}

	// cprb:maxChatID — back to crosspost management
	if strings.HasPrefix(data, "cprb:") {
		maxChatID, err := strconv.ParseInt(strings.TrimPrefix(data, "cprb:"), 10, 64)
		if err != nil {
			return
		}
		tgID, direction, ok := b.repo.GetCrosspostTgChat(maxChatID)
		if !ok {
			return
		}
		body := maxCrosspostMessageBody(b.maxApi, maxCrosspostStatusText(tgID, direction), direction, maxChatID)
		b.maxApi.Messages.AnswerOnCallback(ctx, callbackID, &maxschemes.CallbackAnswer{Message: body})
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
		AddCallback("🔄 Замены", maxschemes.DEFAULT, "cpr:"+id).
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
	if b.cbBlocked(tgChatID) {
		return
	}

	threadID := b.repo.GetTgThreadID(tgChatID)
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
	var qAttType, qAttURL string // для очереди при ошибке

	// Определяем HTML caption если есть markups (для кросспостинга)
	htmlCaption := caption
	useHTML := len(body.Markups) > 0 && caption == text
	if useHTML {
		htmlCaption = maxMarkupsToHTML(text, body.Markups)
	}

	// Собираем вложения: фото/видео → albumMedia (отправляем вместе), остальные → soloMedia
	var albumMedia []interface{}
	var soloMedia []struct {
		url     string
		attType string
		name    string
	}
	pm := ""
	if useHTML {
		pm = "HTML"
	}

	for _, att := range body.Attachments {
		switch a := att.(type) {
		case *maxschemes.PhotoAttachment:
			if a.Payload.Url != "" {
				if len(albumMedia) == 0 {
					qAttType, qAttURL = "photo", a.Payload.Url
				}
				p := tgbotapi.NewInputMediaPhoto(tgbotapi.FileURL(a.Payload.Url))
				albumMedia = append(albumMedia, p)
			}
		case *maxschemes.VideoAttachment:
			if a.Payload.Url != "" {
				if len(albumMedia) == 0 {
					qAttType, qAttURL = "video", a.Payload.Url
				}
				v := tgbotapi.NewInputMediaVideo(tgbotapi.FileURL(a.Payload.Url))
				albumMedia = append(albumMedia, v)
			}
		case *maxschemes.AudioAttachment:
			if a.Payload.Url != "" {
				if qAttType == "" {
					qAttType, qAttURL = "audio", a.Payload.Url
				}
				soloMedia = append(soloMedia, struct {
					url     string
					attType string
					name    string
				}{a.Payload.Url, "audio", ""})
			}
		case *maxschemes.FileAttachment:
			if a.Payload.Url != "" {
				if qAttType == "" {
					qAttType, qAttURL = "file", a.Payload.Url
				}
				soloMedia = append(soloMedia, struct {
					url     string
					attType string
					name    string
				}{a.Payload.Url, "file", a.Filename})
			}
		case *maxschemes.StickerAttachment:
			if a.Payload.Url != "" {
				if qAttType == "" {
					qAttType, qAttURL = "sticker", a.Payload.Url
				}
				soloMedia = append(soloMedia, struct {
					url     string
					attType string
					name    string
				}{a.Payload.Url, "sticker", ""})
			}
		}
	}

	// Отправляем фото/видео как альбом (если их несколько — grouped, иначе — single)
	if len(albumMedia) > 0 {
		mediaSent = true
		// Caption и reply только к первому элементу
		if htmlCaption != "" || replyToID != 0 {
			switch first := albumMedia[0].(type) {
			case tgbotapi.InputMediaPhoto:
				first.Caption = htmlCaption
				if pm != "" {
					first.ParseMode = pm
				}
				albumMedia[0] = first
			case tgbotapi.InputMediaVideo:
				first.Caption = htmlCaption
				if pm != "" {
					first.ParseMode = pm
				}
				albumMedia[0] = first
			}
		}

		if len(albumMedia) == 1 {
			// Одно вложение — отправляем обычным сообщением (альбом из 1 элемента не имеет reply)
			sent, sendErr = b.sendTgMediaFromURL(tgChatID, qAttURL, qAttType, htmlCaption, pm, replyToID, b.cfg.maxMaxFileBytes())
			var e *ErrFileTooLarge
			if errors.As(sendErr, &e) {
				slog.Warn("MAX→TG media too big", "name", e.Name, "size", e.Size)
				m := maxbot.NewMessage().SetChat(chatID).SetText(
					fmt.Sprintf("⚠️ Файл \"%s\" слишком большой для пересылки (%s). Максимальный размер файла %d МБ.",
						e.Name, formatFileSize(int(e.Size)), b.cfg.MaxMaxFileSizeMB))
				b.maxApi.Messages.Send(ctx, m)
			}
		} else {
			// Несколько — отправляем как media group (альбом)
			cfg := tgbotapi.NewMediaGroup(tgChatID, albumMedia)
			if replyToID != 0 {
				cfg.ReplyToMessageID = replyToID
			}
			msgs, err := b.tgBot.SendMediaGroup(cfg)
			if err != nil {
				slog.Error("MAX→TG album send failed", "err", err)
				sendErr = err
				m := maxbot.NewMessage().SetChat(chatID).SetText("Не удалось отправить медиаальбом в Telegram.")
				b.maxApi.Messages.Send(ctx, m)
			} else if len(msgs) > 0 {
				sent = msgs[0]
			}
		}
	}

	// Отправляем остальные вложения (аудио, файлы, стикеры) по одному
	// Если фото/видео не отправлялось, caption добавляем к первому вложению
	firstSolo := true
	for _, sm := range soloMedia {
		smCaption := ""
		smReplyTo := 0
		if firstSolo && !mediaSent {
			smCaption = htmlCaption
			smReplyTo = replyToID
		}
		firstSolo = false
		s, err := b.sendTgMediaFromURL(tgChatID, sm.url, sm.attType, smCaption, pm, smReplyTo, b.cfg.maxMaxFileBytes(), sm.name)
		if err != nil {
			var e *ErrFileTooLarge
			if errors.As(err, &e) {
				slog.Warn("MAX→TG solo media too big", "name", e.Name, "size", e.Size)
				m := maxbot.NewMessage().SetChat(chatID).SetText(
					fmt.Sprintf("⚠️ Файл \"%s\" слишком большой для пересылки (%s). Максимальный размер файла %d МБ.",
						e.Name, formatFileSize(int(e.Size)), b.cfg.MaxMaxFileSizeMB))
				b.maxApi.Messages.Send(ctx, m)
			} else {
				slog.Error("MAX→TG solo media send failed", "type", sm.attType, "err", err)
				m := maxbot.NewMessage().SetChat(chatID).SetText(
					fmt.Sprintf("Не удалось отправить файл \"%s\" в Telegram.", sm.name))
				b.maxApi.Messages.Send(ctx, m)
			}
			if sendErr == nil {
				sendErr = err
			}
		} else if !mediaSent {
			sent = s
			mediaSent = true
		}
	}

	// Текст без медиа
	if !mediaSent {
		if text == "" {
			return
		}
		if len(body.Markups) > 0 && caption == text {
			htmlText := maxMarkupsToHTML(text, body.Markups)
			sent, sendErr = b.tgSendText(tgChatID, htmlText, "HTML", replyToID, threadID)
		} else {
			sent, sendErr = b.tgSendText(tgChatID, caption, "", replyToID, threadID)
		}
	}

	if sendErr != nil {
		errStr := sendErr.Error()
		slog.Error("MAX→TG send failed", "err", errStr, "uid", msgUpd.Message.Sender.UserId, "maxChat", chatID, "tgChat", tgChatID)

		// TOPIC_CLOSED — General топик закрыт, уведомляем и не ретраим
		if strings.Contains(errStr, "TOPIC_CLOSED") {
			m := maxbot.NewMessage().SetChat(chatID).SetText(
				"Не удалось переслать в Telegram: основной топик (General) закрыт.\nОткройте General в настройках TG-группы или сделайте бота админом.")
			b.maxApi.Messages.Send(ctx, m)
			return
		}

		parseMode := ""
		if useHTML {
			parseMode = "HTML"
		}
		var eTooLarge *ErrFileTooLarge
		if !errors.As(sendErr, &eTooLarge) {
			notifyText := "Не удалось переслать сообщение в Telegram. Попробуем ещё раз автоматически."
			if b.cbBlocked(tgChatID) {
				notifyText = "TG API недоступен. Сообщения в очереди, будут доставлены автоматически."
			}
			m := maxbot.NewMessage().SetChat(chatID).SetText(notifyText)
			b.maxApi.Messages.Send(ctx, m)
		}
		b.enqueueMax2Tg(chatID, tgChatID, body.Mid, htmlCaption, qAttType, qAttURL, parseMode)
		b.cbFail(tgChatID)
	} else {
		b.cbSuccess(tgChatID)
		slog.Info("MAX→TG sent", "msgID", sent.MessageID, "media", mediaSent, "uid", msgUpd.Message.Sender.UserId, "maxChat", chatID, "tgChat", tgChatID)
		b.repo.SaveMsg(tgChatID, sent.MessageID, chatID, body.Mid)
	}
}
