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
)

func (b *Bridge) listenTelegram(ctx context.Context) {
	var updates <-chan TGUpdate

	if b.cfg.TgWebhookURL != "" {
		whPath := b.tgWebhookPath()
		whURL := strings.TrimRight(b.cfg.TgWebhookURL, "/") + whPath
		if err := b.tg.SetWebhook(ctx, whURL); err != nil {
			slog.Error("TG set webhook failed", "err", err)
			return
		}
		updates = b.tg.StartWebhook(ctx, whPath)
		slog.Info("TG webhook mode")
	} else {
		// Удаляем webhook если был, переключаемся на polling
		b.tg.DeleteWebhook(ctx)
		updates = b.tg.StartPolling(ctx)
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
				if b.isSelfTgBot(edited.From) {
					continue
				}
				var maxChatID int64
				var linked bool
				if edited.MessageThreadID != 0 {
					maxChatID, linked = b.repo.GetThreadMaxChat(edited.Chat.ID, edited.MessageThreadID)
				}
				if !linked {
					maxChatID, linked = b.repo.GetMaxChat(edited.Chat.ID)
				}
				if !linked {
					continue
				}

				hasMedia := edited.Photo != nil || edited.Video != nil || edited.Document != nil ||
					edited.Animation != nil || edited.Sticker != nil || edited.Voice != nil || edited.Audio != nil

				maxMsgID, hasMapping := b.repo.LookupMaxMsgID(edited.Chat.ID, edited.MessageID)

				// Если маппинг не найден и есть медиа — отправляем как новое сообщение (fallback)
				if hasMedia && !hasMapping {
					prefix := b.hasPrefix("tg", edited.Chat.ID)
					caption := formatTgCaption(edited, prefix, b.cfg.MessageNewline)
					go b.forwardTgToMax(ctx, edited, maxChatID, caption, false)
					continue
				}

				if !hasMapping {
					continue
				}

				prefix := b.hasPrefix("tg", edited.Chat.ID)

				if hasMedia {
					// Edit с медиа — редактируем сообщение в MAX с новым вложением
					caption := formatTgCaption(edited, prefix, b.cfg.MessageNewline)
					go b.editTgMediaInMax(ctx, edited, maxChatID, maxMsgID, caption)
					continue
				}

				// Текстовый edit — конвертируем entities в markdown
				rawText := edited.Text
				editEntities := edited.Entities
				if rawText == "" {
					rawText = edited.Caption
					editEntities = edited.CaptionEntities
				}
				if rawText == "" {
					continue
				}
				mdText := tgEntitiesToMarkdown(rawText, editEntities)
				name := tgName(edited)
				if prefix {
					name = "[TG] " + name
				}
				fwd := formatAttributionMD(name, mdText, b.cfg.MessageNewline)
				m := maxbot.NewMessage().SetChat(maxChatID).SetText(fwd)
				m.SetFormat("markdown")
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

			// Обработка миграции группы в supergroup — обновляем chat ID в базе
			if msg.MigrateToChatID != 0 {
				slog.Info("TG chat migrated to supergroup", "old", msg.Chat.ID, "new", msg.MigrateToChatID)
				if err := b.repo.MigrateTgChat(msg.Chat.ID, msg.MigrateToChatID); err != nil {
					slog.Error("MigrateTgChat failed", "err", err)
				}
				continue
			}

			text := strings.TrimSpace(msg.Text)
			// Убираем @botname из команд: /bridge@MaxTelegramBridgeBot → /bridge
			if strings.HasPrefix(text, "/") {
				if at := strings.Index(text, "@"); at > 0 {
					rest := text[at:]
					if sp := strings.IndexByte(rest, ' '); sp > 0 {
						text = text[:at] + rest[sp:]
					} else {
						text = text[:at]
					}
				}
			}
			slog.Debug("TG msg received", "uid", tgUserID(msg), "chat", msg.Chat.ID, "type", msg.Chat.Type)

			// Запоминаем юзера при личном сообщении
			if msg.Chat.Type == "private" && msg.From != nil {
				b.repo.TouchUser(msg.From.ID, "tg", msg.From.UserName, msg.From.FirstName)
			}

			// Опциональные аддоны (если подключены build-тегом) первыми получают
			// личные сообщения и форварды из каналов. Если аддон взял сообщение в
			// работу — бридж дальше не обрабатывает. В публичной сборке addon == nil.
			if msg.Chat.Type == "private" && b.addon != nil && msg.From != nil {
				if msg.ForwardOriginChat != nil && msg.ForwardOriginChat.Type == "channel" {
					if b.addon.HandleDMForward(ctx, msg.From.ID, msg.Chat.ID, msg.ForwardOriginChat.ID, msg.ForwardOriginChat.Title) {
						continue
					}
				}
				if b.addon.HandleDMCommand(ctx, msg.From.ID, msg.Chat.ID, text) {
					continue
				}
			}

			if text == "/whoami" {
				b.tg.SendMessage(ctx, msg.Chat.ID,
					"MaxTelegramBridgeBot — мост между Telegram и MAX.\n"+
						"Автор: Andrey Lugovskoy (@BEARlogin)\n"+
						"Исходники: https://github.com/BEARlogin/max-telegram-bridge-bot\n"+
						"Лицензия: CC BY-NC 4.0", &SendOpts{ThreadID: msg.MessageThreadID})
				continue
			}

			if text == "/start" || text == "/help" {
				b.tg.SendMessage(ctx, msg.Chat.ID,
					"Бот-мост между Telegram и MAX.\n\n"+
						"Команды (группы):\n"+
						"/bridge — создать ключ для связки чатов\n"+
						"/bridge <ключ> — связать этот чат с MAX-чатом по ключу\n"+
						"/bridge prefix on/off — включить/выключить префикс [TG]/[MAX]\n"+
						"/unbridge — удалить связку\n"+
						"/thread — направить сообщения из MAX в текущий топик (форум)\n"+
						"/thread_bridge — связать текущий тред с отдельной MAX-группой (форум)\n"+
						"/thread_unbridge — разорвать связку треда\n\n"+
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
						"3. В TG сделайте бота админом, если группа — супергруппа с темами (форум). "+
						"В обычных группах админство не требуется.\n"+
						"4. В одном из чатов отправьте /bridge\n"+
						"5. Бот выдаст ключ — отправьте /bridge <ключ> в другом чате\n"+
						"6. Готово!\n\n"+
						"Поддержка: https://github.com/BEARlogin/max-telegram-bridge-bot/issues", &SendOpts{ThreadID: msg.MessageThreadID})
				continue
			}

			// Обработка ввода замены (если юзер в режиме ожидания)
			if msg.Chat.Type == "private" && msg.From != nil && !strings.HasPrefix(text, "/") {
				if w, ok := b.getReplWait(msg.From.ID); ok {
					b.clearReplWait(msg.From.ID)
					rule, valid := parseReplacementInput(text)
					if !valid {
						b.tg.SendMessage(ctx, msg.Chat.ID, "Неверный формат. Используйте:\n<code>from | to</code>\nили\n<code>/regex/ | to</code>", &SendOpts{ParseMode: "HTML", ThreadID: msg.MessageThreadID})
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
						b.tg.SendMessage(ctx, msg.Chat.ID, "Ошибка сохранения.", &SendOpts{ThreadID: msg.MessageThreadID})
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
					b.tg.SendMessage(ctx, msg.Chat.ID,
						fmt.Sprintf("Замена добавлена (%s, %s):\n<code>%s</code> → <code>%s</code>", dirLabel, ruleType, rule.From, rule.To),
						&SendOpts{ParseMode: "HTML", ThreadID: msg.MessageThreadID})
					continue
				}
			}

			// /crosspost в личке TG — показать список связок
			if msg.Chat.Type == "private" && text == "/crosspost" {
				if !b.checkUserAllowed(ctx, msg.Chat.ID, msg.From.ID, msg.MessageThreadID) {
					continue
				}
				links := b.repo.ListCrossposts(msg.From.ID)
				if len(links) == 0 {
					b.tg.SendMessage(ctx, msg.Chat.ID,
						"Нет активных связок.\n\nНастройка: перешлите пост из TG-канала сюда, затем в MAX-боте /crosspost <ID>", &SendOpts{ThreadID: msg.MessageThreadID})
				} else {
					for _, l := range links {
						kb := tgCrosspostKeyboard(l.Direction, l.MaxChatID, b.repo.GetCrosspostSyncEdits(l.MaxChatID))
						tgTitle := b.tgChatTitle(ctx, l.TgChatID)
						statusText := tgCrosspostStatusText(tgTitle, l.Direction)
						if tgTitle == "" {
							statusText += fmt.Sprintf("\nTG: %d ↔ MAX: %d", l.TgChatID, l.MaxChatID)
						} else {
							statusText += fmt.Sprintf("\nTG: «%s» (%d)\nMAX: %d", tgTitle, l.TgChatID, l.MaxChatID)
						}
						b.tg.SendMessage(ctx, msg.Chat.ID, statusText, &SendOpts{ReplyMarkup: kb, ThreadID: msg.MessageThreadID})
					}
				}
				continue
			}

			// Пересланное сообщение из канала → показать ID или управление (только в личке)
			if msg.Chat.Type == "private" && msg.ForwardOriginChat != nil && msg.ForwardOriginChat.Type == "channel" {
				if !b.checkUserAllowed(ctx, msg.Chat.ID, msg.From.ID, msg.MessageThreadID) {
					continue
				}
				channelID := msg.ForwardOriginChat.ID
				channelTitle := msg.ForwardOriginChat.Title

				// Запоминаем TG user ID для этого канала (для owner при pairing)
				b.cpTgOwnerMu.Lock()
				b.cpTgOwner[channelID] = msg.From.ID
				b.cpTgOwnerMu.Unlock()
				slog.Info("TG crosspost forward", "tgUser", msg.From.ID, "tgChannel", channelID)

				// Проверяем, уже связан ли канал
				if maxChatID, direction, ok := b.repo.GetCrosspostMaxChat(channelID); ok {
					text := tgCrosspostStatusText(channelTitle, direction)
					kb := tgCrosspostKeyboard(direction, maxChatID, b.repo.GetCrosspostSyncEdits(maxChatID))
					b.tg.SendMessage(ctx, msg.Chat.ID, text, &SendOpts{ReplyMarkup: kb, ThreadID: msg.MessageThreadID})
					continue
				}

				b.tg.SendMessage(ctx, msg.Chat.ID,
					fmt.Sprintf("TG-канал «%s»\nID: <code>%d</code>\n\nВ личке MAX-бота напишите:\n<code>/crosspost %d</code>\n\nMAX-бот: %s\n\nЗатем перешлите пост из MAX-канала в личку MAX-бота.", channelTitle, channelID, channelID, b.cfg.MaxBotURL),
					&SendOpts{ParseMode: "HTML", ThreadID: msg.MessageThreadID})
				continue
			}

			// Проверка прав админа в группах
			isGroup := isTgGroup(msg.Chat.Type)
			isAdmin := false
			botNotAdmin := false
			if isGroup {
				if isTgAnonymousAdmin(msg) {
					// Владелец/админ с "Remain anonymous" — шлёт от имени группы
					isAdmin = true
				} else if msg.From != nil {
					status, err := b.tg.GetChatMember(ctx, msg.Chat.ID, msg.From.ID)
					if err != nil {
						slog.Warn("TG getChatMember failed", "err", err, "chat", msg.Chat.ID, "user", msg.From.ID, "chatType", msg.Chat.Type)
						if strings.Contains(err.Error(), "CHAT_ADMIN_REQUIRED") {
							botNotAdmin = true
						}
					} else {
						slog.Debug("TG getChatMember ok", "chat", msg.Chat.ID, "user", msg.From.ID, "status", status)
						isAdmin = isTgAdmin(status)
					}
				}
			}
			adminDeniedText := "Эта команда доступна только админам группы."
			if botNotAdmin {
				adminDeniedText = "Бот не может проверить ваши права — сделайте бота админом группы и повторите команду."
			}

			// /thread — установить/сбросить топик по умолчанию
			if text == "/thread" {
				if !b.checkUserAllowed(ctx, msg.Chat.ID, tgUserID(msg), msg.MessageThreadID) {
					continue
				}
				if isGroup && !isAdmin {
					b.tg.SendMessage(ctx, msg.Chat.ID, adminDeniedText, &SendOpts{ThreadID: msg.MessageThreadID})
					continue
				}
				if _, ok := b.repo.GetMaxChat(msg.Chat.ID); !ok {
					b.tg.SendMessage(ctx, msg.Chat.ID, "Чат не связан. Сначала выполните /bridge.", &SendOpts{ThreadID: msg.MessageThreadID})
					continue
				}
				if msg.MessageThreadID != 0 {
					b.repo.SetTgThreadID(msg.Chat.ID, msg.MessageThreadID)
					b.tg.SendMessage(ctx, msg.Chat.ID,
						fmt.Sprintf("Топик по умолчанию установлен (thread %d). Сообщения из MAX будут приходить сюда.", msg.MessageThreadID),
						&SendOpts{ThreadID: msg.MessageThreadID})
				} else {
					b.repo.SetTgThreadID(msg.Chat.ID, 0)
					b.tg.SendMessage(ctx, msg.Chat.ID, "Топик сброшен. Сообщения из MAX будут приходить в основной чат.", &SendOpts{})
				}
				slog.Info("thread set", "tgChat", msg.Chat.ID, "thread", msg.MessageThreadID, "uid", tgUserID(msg))
				continue
			}

			// /bridge prefix on/off
			if text == "/bridge prefix on" || text == "/bridge prefix off" {
				if !b.checkUserAllowed(ctx, msg.Chat.ID, tgUserID(msg), msg.MessageThreadID) {
					continue
				}
				if isGroup && !isAdmin {
					b.tg.SendMessage(ctx, msg.Chat.ID, adminDeniedText, &SendOpts{ThreadID: msg.MessageThreadID})
					continue
				}
				on := text == "/bridge prefix on"
				if b.repo.SetPrefix("tg", msg.Chat.ID, on) {
					if on {
						b.tg.SendMessage(ctx, msg.Chat.ID, "Префикс [TG]/[MAX] включён.", &SendOpts{ThreadID: msg.MessageThreadID})
					} else {
						b.tg.SendMessage(ctx, msg.Chat.ID, "Префикс [TG]/[MAX] выключен.", &SendOpts{ThreadID: msg.MessageThreadID})
					}
				} else {
					b.tg.SendMessage(ctx, msg.Chat.ID, "Чат не связан. Сначала выполните /bridge.", &SendOpts{ThreadID: msg.MessageThreadID})
				}
				continue
			}

			// /bridge или /bridge <key>
			if text == "/bridge" || strings.HasPrefix(text, "/bridge ") {
				slog.Info("TG /bridge command", "chat", msg.Chat.ID, "chatType", msg.Chat.Type, "chatTitle", msg.Chat.Title, "user", tgUserID(msg), "isGroup", isGroup, "isAdmin", isAdmin, "text", text)
				if !b.checkUserAllowed(ctx, msg.Chat.ID, tgUserID(msg), msg.MessageThreadID) {
					continue
				}
				if isGroup && !isAdmin {
					b.tg.SendMessage(ctx, msg.Chat.ID, adminDeniedText, &SendOpts{ThreadID: msg.MessageThreadID})
					continue
				}
				key := strings.TrimSpace(strings.TrimPrefix(text, "/bridge"))

				// /bridge без ключа — если чат уже связан, не создавать новый ключ
				if key == "" {
					if maxID, linked := b.repo.GetMaxChat(msg.Chat.ID); linked {
						var txt string
						if isGroup {
							txt = fmt.Sprintf("Эта группа уже связана с MAX (ID <code>%d</code>).\n\n/unbridge — удалить связку.", maxID)
						} else {
							txt = fmt.Sprintf("Этот личный чат уже связан с MAX (ID <code>%d</code>).\n\nЧтобы связать <b>группу</b> — добавьте бота в неё и отправьте <code>/bridge</code> <b>внутри группы</b>, не здесь.\n\n/unbridge — удалить связку этого личного чата.", maxID)
						}
						b.tg.SendMessage(ctx, msg.Chat.ID, txt, &SendOpts{ParseMode: "HTML", ThreadID: msg.MessageThreadID})
						continue
					}
					if !isGroup {
						b.tg.SendMessage(ctx, msg.Chat.ID,
							"Чтобы связать группу — добавьте бота в неё и отправьте <code>/bridge</code> <b>внутри группы</b>, не здесь.\n\nЕсли хотите связать этот личный чат с MAX-пользователем — введите ключ от него: <code>/bridge &lt;ключ&gt;</code>.",
							&SendOpts{ParseMode: "HTML"})
						continue
					}
				}
				paired, generatedKey, err := b.repo.Register(key, "tg", msg.Chat.ID)
				if err != nil {
					slog.Error("register failed", "err", err)
					continue
				}

				if paired {
					b.tg.SendMessage(ctx, msg.Chat.ID, "Связано! Сообщения теперь пересылаются.", &SendOpts{ThreadID: msg.MessageThreadID})
					b.repo.SetTgThreadID(msg.Chat.ID, msg.MessageThreadID) // 0 = no topics
					slog.Info("paired", "platform", "tg", "chat", msg.Chat.ID, "key", key)
				} else if generatedKey != "" {
					b.tg.SendMessage(ctx, msg.Chat.ID,
						fmt.Sprintf("Ключ для связки: <code>%s</code>\n\nДобавьте MAX-бота в нужную MAX-группу и отправьте <b>в ней</b> (не в ЛС бота):\n<code>/bridge %s</code>\n\nСсылка на MAX-бота (для добавления в группу): %s", generatedKey, generatedKey, b.cfg.MaxBotURL),
						&SendOpts{ParseMode: "HTML", ThreadID: msg.MessageThreadID})
					slog.Info("pending", "platform", "tg", "chat", msg.Chat.ID, "key", generatedKey)
				} else {
					b.tg.SendMessage(ctx, msg.Chat.ID, "Ключ не найден или чат той же платформы.", &SendOpts{ThreadID: msg.MessageThreadID})
				}
				continue
			}

			if text == "/unbridge" {
				if isGroup && !isAdmin {
					b.tg.SendMessage(ctx, msg.Chat.ID, adminDeniedText, &SendOpts{ThreadID: msg.MessageThreadID})
					continue
				}
				if !b.checkUserAllowed(ctx, msg.Chat.ID, tgUserID(msg), msg.MessageThreadID) {
					continue
				}
				if b.repo.Unpair("tg", msg.Chat.ID) {
					b.tg.SendMessage(ctx, msg.Chat.ID, "Связка удалена.", &SendOpts{ThreadID: msg.MessageThreadID})
				} else {
					b.tg.SendMessage(ctx, msg.Chat.ID, "Этот чат не связан.", &SendOpts{ThreadID: msg.MessageThreadID})
				}
				continue
			}

			// /thread_bridge — связать конкретный тред с отдельным MAX-чатом
			if text == "/thread_bridge" {
				if !b.checkUserAllowed(ctx, msg.Chat.ID, tgUserID(msg), msg.MessageThreadID) {
					continue
				}
				if !isGroup {
					b.tg.SendMessage(ctx, msg.Chat.ID, "Команда работает только в форум-группах.", nil)
					continue
				}
				if !isAdmin {
					b.tg.SendMessage(ctx, msg.Chat.ID, adminDeniedText, &SendOpts{ThreadID: msg.MessageThreadID})
					continue
				}
				if msg.MessageThreadID == 0 {
					b.tg.SendMessage(ctx, msg.Chat.ID,
						"Отправьте команду <b>внутри конкретного треда</b> (не в General). Тред будет связан с отдельным MAX-чатом.",
						&SendOpts{ParseMode: "HTML", ThreadID: msg.MessageThreadID})
					continue
				}
				if maxID, ok := b.repo.GetThreadMaxChat(msg.Chat.ID, msg.MessageThreadID); ok {
					b.tg.SendMessage(ctx, msg.Chat.ID,
						fmt.Sprintf("Этот тред уже связан с MAX-чатом (ID <code>%d</code>).\n\n/thread_unbridge — разорвать связку.", maxID),
						&SendOpts{ParseMode: "HTML", ThreadID: msg.MessageThreadID})
					continue
				}
				key, err := b.repo.StartThreadBridge(msg.Chat.ID, msg.MessageThreadID)
				if err != nil {
					slog.Error("StartThreadBridge failed", "err", err)
					b.tg.SendMessage(ctx, msg.Chat.ID, "Не удалось создать ключ.", &SendOpts{ThreadID: msg.MessageThreadID})
					continue
				}
				_, sendErr := b.tg.SendMessage(ctx, msg.Chat.ID,
					fmt.Sprintf("Ключ для связки этого треда: <code>%s</code>\n\nДобавьте MAX-бота в отдельную MAX-группу (которая будет зеркалом этого треда) и отправьте <b>в ней</b>:\n<code>/thread_bridge %s</code>\n\nСсылка на MAX-бота: %s", key, key, b.cfg.MaxBotURL),
					&SendOpts{ParseMode: "HTML", ThreadID: msg.MessageThreadID})
				if sendErr != nil {
					slog.Error("thread-bridge reply send failed", "err", sendErr, "tgChat", msg.Chat.ID, "thread", msg.MessageThreadID)
				}
				slog.Info("thread-bridge pending", "tgChat", msg.Chat.ID, "thread", msg.MessageThreadID, "key", key)
				continue
			}

			// /thread_unbridge — удалить связку конкретного треда
			if text == "/thread_unbridge" {
				if !b.checkUserAllowed(ctx, msg.Chat.ID, tgUserID(msg), msg.MessageThreadID) {
					continue
				}
				if isGroup && !isAdmin {
					b.tg.SendMessage(ctx, msg.Chat.ID, adminDeniedText, &SendOpts{ThreadID: msg.MessageThreadID})
					continue
				}
				if msg.MessageThreadID == 0 {
					b.tg.SendMessage(ctx, msg.Chat.ID, "Отправьте команду внутри треда.", nil)
					continue
				}
				if b.repo.UnpairThread(msg.Chat.ID, msg.MessageThreadID) {
					b.tg.SendMessage(ctx, msg.Chat.ID, "Связка треда удалена.", &SendOpts{ThreadID: msg.MessageThreadID})
				} else {
					b.tg.SendMessage(ctx, msg.Chat.ID, "Этот тред не связан.", &SendOpts{ThreadID: msg.MessageThreadID})
				}
				continue
			}

			// Пересылка. Приоритет у thread-bridge: если сообщение в связанном треде — шлём в его MAX-чат.
			var maxChatID int64
			var linked bool
			if msg.MessageThreadID != 0 {
				maxChatID, linked = b.repo.GetThreadMaxChat(msg.Chat.ID, msg.MessageThreadID)
			}
			if !linked {
				maxChatID, linked = b.repo.GetMaxChat(msg.Chat.ID)
			}
			if !linked {
				continue
			}
			if b.isSelfTgBot(msg.From) {
				continue
			}

			prefix := b.hasPrefix("tg", msg.Chat.ID)
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

			go b.forwardTgToMax(ctx, msg, maxChatID, caption, false)
		}
	}
}

func tgUserID(msg *TGMessage) int64 {
	if msg.From != nil {
		return msg.From.ID
	}
	return 0
}

// forwardTgToMax пересылает TG-сообщение (текст/медиа) в MAX-чат.
// Если isCrosspost=true, caption используется как финальный текст (с заменами, без атрибуции).
func (b *Bridge) forwardTgToMax(ctx context.Context, msg *TGMessage, maxChatID int64, caption string, isCrosspost bool) {
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
		b.notifyTgUser(ctx, msg, maxChatID, warn, isCrosspost)
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
		// Конвертируем entities в markdown на сыром тексте (до атрибуции, иначе офсеты съезжают)
		var mdCaption string
		if isCrosspost {
			// Кросспостинг: caption уже содержит замены, без атрибуции
			mdCaption = caption
		} else {
			rawText := msg.Caption
			if rawText == "" {
				rawText = msg.Text
			}
			mdText := tgEntitiesToMarkdown(rawText, msg.CaptionEntities)
			name := tgName(msg)
			if b.hasPrefix("tg", msg.Chat.ID) {
				name = "[TG] " + name
			}
			mdCaption = formatAttributionMD(name, mdText, b.cfg.MessageNewline)
		}
		m := maxbot.NewMessage().SetChat(maxChatID).SetText(mdCaption)
		// Caption всегда markdown — и для bridge (с **жирной** атрибуцией),
		// и для crosspost (formatTgCrosspostCaption уже сконвертил entities).
		// При пустом тексте MAX отвергает payload с format — пропускаем.
		if mdCaption != "" {
			m.SetFormat("markdown")
		}
		if b.cfg.TgAPIURL != "" {
			// Custom TG API — MAX не может скачать по URL, скачиваем и загружаем через reader
			if uploaded, err := b.uploadTgPhotoToMax(ctx, photo.FileID); err == nil {
				m.AddPhoto(uploaded)
			} else {
				slog.Error("TG→MAX photo upload failed", "err", err)
				b.notifyTgUser(ctx, msg, maxChatID, uploadErrMsg("Не удалось отправить фото в MAX", err), isCrosspost)
				return
			}
		} else if fileURL, err := b.tgFileURL(ctx, photo.FileID); err == nil {
			if uploaded, err := b.maxApi.Uploads.UploadPhotoFromUrl(ctx, fileURL); err == nil {
				m.AddPhoto(uploaded)
			} else {
				slog.Error("TG→MAX photo upload failed", "err", err)
				b.notifyTgUser(ctx, msg, maxChatID, uploadErrMsg("Не удалось отправить фото в MAX", err), isCrosspost)
				return
			}
		} else {
			slog.Error("TG→MAX photo upload failed", "err", err)
			b.notifyTgUser(ctx, msg, maxChatID, uploadErrMsg("Не удалось отправить фото в MAX", err), isCrosspost)
			return
		}
		if msg.ReplyToMessage != nil {
			if maxReplyID, ok := b.repo.LookupMaxMsgID(msg.Chat.ID, msg.ReplyToMessage.MessageID); ok {
				m.SetReply(mdCaption, maxReplyID)
			}
		}
		slog.Info("TG→MAX sending photo", "uid", uid, "tgChat", msg.Chat.ID, "maxChat", maxChatID)
		result, err := b.maxApi.Messages.SendWithResult(ctx, m)
		if err != nil {
			slog.Error("TG→MAX send failed", "err", err, "uid", uid, "tgChat", msg.Chat.ID, "maxChat", maxChatID)
			if b.cbFail(maxChatID) {
				b.notifyTgUser(ctx, msg, maxChatID,
					fmt.Sprintf("Не удалось переслать в MAX. Пересылка приостановлена на %d мин. Проверьте, что бот добавлен в MAX-чат и является админом.", int(cbCooldown.Minutes())), isCrosspost)
			}
		} else {
			b.cbSuccess(maxChatID)
			slog.Info("TG→MAX sent", "mid", result.Body.Mid)
			b.repo.SaveMsg(msg.Chat.ID, msg.MessageID, maxChatID, result.Body.Mid, msg.MessageThreadID)
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
			b.notifyTgUser(ctx, msg, maxChatID, uploadErrMsg(fmt.Sprintf("Не удалось отправить GIF \"%s\" в MAX", name), err), isCrosspost)
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
				b.notifyTgUser(ctx, msg, maxChatID, uploadErrMsg("Не удалось отправить стикер в MAX", err), isCrosspost)
				return
			}
		} else {
			// Обычный стикер WebP → отправляем как фото
			if fileURL, err := b.tgFileURL(ctx, msg.Sticker.FileID); err == nil {
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
						b.notifyTgUser(ctx, msg, maxChatID, uploadErrMsg("Не удалось отправить стикер в MAX", err), isCrosspost)
					} else {
						slog.Info("TG→MAX sent", "mid", result.Body.Mid)
						b.repo.SaveMsg(msg.Chat.ID, msg.MessageID, maxChatID, result.Body.Mid, msg.MessageThreadID)
					}
					return
				} else {
					slog.Error("TG→MAX sticker photo upload failed", "err", err)
					b.notifyTgUser(ctx, msg, maxChatID, uploadErrMsg("Не удалось отправить стикер в MAX", err), isCrosspost)
					return
				}
			} else {
				slog.Error("TG→MAX sticker getFileURL failed", "err", err)
				b.notifyTgUser(ctx, msg, maxChatID, uploadErrMsg("Не удалось отправить стикер в MAX", err), isCrosspost)
				return
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
			slog.Error("TG→MAX video upload failed", "err", err, "fileSizeBytes", msg.Video.FileSize, "fileSizeMB", msg.Video.FileSize/1048576, "name", name)
			b.notifyTgUser(ctx, msg, maxChatID, uploadErrMsg(fmt.Sprintf("Не удалось отправить видео \"%s\" в MAX", name), err), isCrosspost)
			// Видео не залилось — не блокируем отправку текста/подписи, fallback ниже подмешает [Видео].
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
			b.notifyTgUser(ctx, msg, maxChatID, uploadErrMsg("Не удалось отправить кружок в MAX", err), isCrosspost)
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
				b.notifyTgUser(ctx, msg, maxChatID, fmt.Sprintf("Файл \"%s\" не поддерживается в MAX (расширение .%s не разрешено).", name, ext), isCrosspost)
				return
			}
		}
		if uploaded, err := b.uploadTgMediaToMax(ctx, msg.Document.FileID, uploadType, name); err == nil {
			mediaToken = uploaded.Token
			mediaAttType = attType
		} else {
			var e *ErrForbiddenExtension
			if errors.As(err, &e) {
				b.notifyTgUser(ctx, msg, maxChatID, fmt.Sprintf("Файл \"%s\" не поддерживается в MAX (запрещённое расширение).", name), isCrosspost)
				return
			}
			slog.Error("TG→MAX file upload failed", "err", err)
			b.notifyTgUser(ctx, msg, maxChatID,
				uploadErrMsg(fmt.Sprintf("Не удалось отправить файл \"%s\" в MAX", name), err), isCrosspost)
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
				b.notifyTgUser(ctx, msg, maxChatID, fmt.Sprintf("Файл \"%s\" не поддерживается в MAX (запрещённое расширение).", e.Name), isCrosspost)
				return
			}
			slog.Error("TG→MAX voice upload failed", "err", err)
			b.notifyTgUser(ctx, msg, maxChatID, uploadErrMsg("Не удалось отправить голосовое сообщение в MAX", err), isCrosspost)
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
				b.notifyTgUser(ctx, msg, maxChatID, fmt.Sprintf("Файл \"%s\" не поддерживается в MAX (расширение .%s не разрешено).", name, ext), isCrosspost)
				return
			}
		}
		if uploaded, err := b.uploadTgMediaToMax(ctx, msg.Audio.FileID, maxschemes.FILE, name); err == nil {
			mediaToken = uploaded.Token
			mediaAttType = "file"
		} else {
			var e *ErrForbiddenExtension
			if errors.As(err, &e) {
				b.notifyTgUser(ctx, msg, maxChatID, fmt.Sprintf("Файл \"%s\" не поддерживается в MAX (запрещённое расширение).", name), isCrosspost)
				return
			}
			slog.Error("TG→MAX audio upload failed", "err", err)
			b.notifyTgUser(ctx, msg, maxChatID, uploadErrMsg(fmt.Sprintf("Не удалось отправить аудио \"%s\" в MAX", name), err), isCrosspost)
			return
		}
	}

	// Формируем текст: для crosspost берём caption как есть (с заменами, без атрибуции),
	// для bridge — конвертируем entities на сыром тексте, потом добавляем атрибуцию
	var mdText string
	if isCrosspost {
		mdText = caption
	} else {
		rawText := msg.Text
		entities := msg.Entities
		if rawText == "" {
			rawText = msg.Caption
			entities = msg.CaptionEntities
		}
		mdText = tgEntitiesToMarkdown(rawText, entities)
	}

	// Fallback: медиа было, но не загрузилось (либо не хватило места, либо Bot API
	// не смог его скачать). Подмешиваем маркер типа, чтобы хотя бы текст прошёл.
	hasMedia := msg.Video != nil || msg.VideoNote != nil || msg.Document != nil ||
		msg.Voice != nil || msg.Audio != nil || msg.Sticker != nil || msg.Animation != nil || msg.Photo != nil
	if mediaAttType == "" && hasMedia {
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
		case msg.Animation != nil:
			mediaType = "[GIF]"
		case msg.Photo != nil:
			mediaType = "[Фото]"
		}
		if mdText != "" {
			mdText = mdText + "\n" + mediaType
		} else {
			mdText = mediaType
		}
	}

	// Reply ID
	var replyTo string
	if msg.ReplyToMessage != nil {
		if maxReplyID, ok := b.repo.LookupMaxMsgID(msg.Chat.ID, msg.ReplyToMessage.MessageID); ok {
			replyTo = maxReplyID
		}
	}

	var mdCaption string
	if isCrosspost {
		mdCaption = mdText
	} else {
		name := tgName(msg)
		if b.hasPrefix("tg", msg.Chat.ID) {
			name = "[TG] " + name
		}
		mdCaption = formatAttributionMD(name, mdText, b.cfg.MessageNewline)
	}

	// Если для этого чата уже есть сообщения в очереди — не отправляем напрямую,
	// чтобы не нарушить порядок. Сразу ставим в очередь.
	// Caption для crosspost уже содержит markdown (formatTgCrosspostCaption),
	// так что формат одинаков для обоих режимов.
	format := "markdown"

	if b.hasPendingForChat("tg2max", maxChatID) {
		slog.Info("TG→MAX queued (pending exists)", "uid", uid, "tgChat", msg.Chat.ID, "maxChat", maxChatID)
		b.enqueueTg2Max(msg.Chat.ID, msg.MessageID, maxChatID, mdCaption, mediaAttType, mediaToken, replyTo, format)
		return
	}

	var mid string
	var sendErr error

	if mediaAttType != "" {
		slog.Info("TG→MAX sending direct", "type", mediaAttType, "uid", uid, "tgChat", msg.Chat.ID, "maxChat", maxChatID)
		mid, sendErr = b.sendMaxDirectFormatted(ctx, maxChatID, mdCaption, mediaAttType, mediaToken, replyTo, format)
	} else {
		slog.Info("TG→MAX sending", "uid", uid, "tgChat", msg.Chat.ID, "maxChat", maxChatID)
		mid, sendErr = b.sendMaxDirectFormatted(ctx, maxChatID, mdCaption, "", "", replyTo, format)
	}

	if sendErr != nil {
		errStr := sendErr.Error()
		slog.Error("TG→MAX send failed", "err", errStr, "uid", uid, "tgChat", msg.Chat.ID, "maxChat", maxChatID)
		// 403/404 — permanent error, не ретраим
		if !strings.Contains(errStr, "403") && !strings.Contains(errStr, "404") && !strings.Contains(errStr, "chat.denied") {
			b.enqueueTg2Max(msg.Chat.ID, msg.MessageID, maxChatID, mdCaption, mediaAttType, mediaToken, replyTo, format)
		}
		if b.cbFail(maxChatID) {
			b.notifyTgUser(ctx, msg, maxChatID,
				"MAX API недоступен. Сообщения в очереди, будут доставлены автоматически.", isCrosspost)
		}
	} else {
		b.cbSuccess(maxChatID)
		slog.Info("TG→MAX sent", "mid", mid, "uid", uid, "tgChat", msg.Chat.ID, "maxChat", maxChatID)
		b.repo.SaveMsg(msg.Chat.ID, msg.MessageID, maxChatID, mid, msg.MessageThreadID)
	}
}

// editTgMediaInMax редактирует сообщение с медиа в MAX (TG→MAX edit с вложением).
func (b *Bridge) editTgMediaInMax(ctx context.Context, msg *TGMessage, maxChatID int64, maxMsgID string, caption string) {
	uid := tgUserID(msg)
	m := maxbot.NewMessage().SetChat(maxChatID)

	// Конвертируем entities в markdown на сыром тексте (до атрибуции)
	rawText := msg.Caption
	editEntities := msg.CaptionEntities
	if rawText == "" {
		rawText = msg.Text
		editEntities = msg.Entities
	}
	mdText := tgEntitiesToMarkdown(rawText, editEntities)
	name := tgName(msg)
	if b.hasPrefix("tg", msg.Chat.ID) {
		name = "[TG] " + name
	}
	mdCaption := formatAttributionMD(name, mdText, b.cfg.MessageNewline)
	m.SetText(mdCaption)
	m.SetFormat("markdown")

	if msg.Photo != nil {
		photo := msg.Photo[len(msg.Photo)-1]
		if b.cfg.TgAPIURL != "" {
			if uploaded, err := b.uploadTgPhotoToMax(ctx, photo.FileID); err == nil {
				m.AddPhoto(uploaded)
			} else {
				slog.Error("TG→MAX edit photo upload failed", "err", err)
				b.tg.SendMessage(ctx, msg.Chat.ID, uploadErrMsg("Не удалось обновить фото в MAX", err), nil)
				return
			}
		} else if fileURL, err := b.tgFileURL(ctx, photo.FileID); err == nil {
			if uploaded, err := b.maxApi.Uploads.UploadPhotoFromUrl(ctx, fileURL); err == nil {
				m.AddPhoto(uploaded)
			} else {
				slog.Error("TG→MAX edit photo upload failed", "err", err)
				b.tg.SendMessage(ctx, msg.Chat.ID, uploadErrMsg("Не удалось обновить фото в MAX", err), nil)
				return
			}
		} else {
			slog.Error("TG→MAX edit photo upload failed", "err", err)
			b.tg.SendMessage(ctx, msg.Chat.ID, uploadErrMsg("Не удалось обновить фото в MAX", err), nil)
			return
		}
	} else if msg.Video != nil {
		// MAX SDK EditMessage без вложений стирает медиа на стороне MAX. Перезаливаем видео.
		name := "video.mp4"
		if msg.Video.FileName != "" {
			name = msg.Video.FileName
		}
		uploaded, err := b.uploadTgMediaToMax(ctx, msg.Video.FileID, maxschemes.VIDEO, name)
		if err != nil {
			slog.Error("TG→MAX edit video upload failed", "err", err)
			b.tg.SendMessage(ctx, msg.Chat.ID, uploadErrMsg(fmt.Sprintf("Не удалось обновить видео \"%s\" в MAX", name), err), nil)
			return
		}
		m.AddVideo(uploaded)
	} else if msg.Animation != nil {
		name := "animation.mp4"
		if msg.Animation.FileName != "" {
			name = msg.Animation.FileName
		}
		uploaded, err := b.uploadTgMediaToMax(ctx, msg.Animation.FileID, maxschemes.VIDEO, name)
		if err != nil {
			slog.Error("TG→MAX edit gif upload failed", "err", err)
			b.tg.SendMessage(ctx, msg.Chat.ID, uploadErrMsg(fmt.Sprintf("Не удалось обновить GIF \"%s\" в MAX", name), err), nil)
			return
		}
		m.AddVideo(uploaded)
	} else if msg.Document != nil {
		name := msg.Document.FileName
		uploadType := maxschemes.FILE
		isVideo := strings.HasPrefix(msg.Document.MimeType, "video/")
		if isVideo {
			uploadType = maxschemes.VIDEO
			if name == "" {
				name = mimeToFilename("video", msg.Document.MimeType)
			}
		}
		if name == "" {
			name = mimeToFilename("document", msg.Document.MimeType)
		}
		uploaded, err := b.uploadTgMediaToMax(ctx, msg.Document.FileID, uploadType, name)
		if err != nil {
			slog.Error("TG→MAX edit document upload failed", "err", err)
			b.tg.SendMessage(ctx, msg.Chat.ID, uploadErrMsg(fmt.Sprintf("Не удалось обновить файл \"%s\" в MAX", name), err), nil)
			return
		}
		if isVideo {
			m.AddVideo(uploaded)
		} else {
			m.AddFile(uploaded)
		}
	} else if msg.Audio != nil {
		name := "audio.mp3"
		if msg.Audio.FileName != "" {
			name = msg.Audio.FileName
		}
		uploaded, err := b.uploadTgMediaToMax(ctx, msg.Audio.FileID, maxschemes.FILE, name)
		if err != nil {
			slog.Error("TG→MAX edit audio upload failed", "err", err)
			b.tg.SendMessage(ctx, msg.Chat.ID, uploadErrMsg(fmt.Sprintf("Не удалось обновить аудио \"%s\" в MAX", name), err), nil)
			return
		}
		m.AddFile(uploaded)
	} else if msg.Voice != nil {
		uploaded, err := b.uploadTgMediaToMax(ctx, msg.Voice.FileID, maxschemes.AUDIO, "voice.ogg")
		if err != nil {
			slog.Error("TG→MAX edit voice upload failed", "err", err)
			b.tg.SendMessage(ctx, msg.Chat.ID, uploadErrMsg("Не удалось обновить голосовое в MAX", err), nil)
			return
		}
		m.AddAudio(uploaded)
	}
	// Стикеры/VideoNote редактирование подписи не поддерживают со стороны TG, поэтому пропускаем.

	if err := b.maxApi.Messages.EditMessage(ctx, maxMsgID, m); err != nil {
		slog.Error("TG→MAX edit media failed", "err", err, "uid", uid, "tgChat", msg.Chat.ID, "maxMsgID", maxMsgID)
	} else {
		slog.Info("TG→MAX edited media", "mid", maxMsgID, "uid", uid, "tgChat", msg.Chat.ID)
	}
}

// handleTgChannelPost обрабатывает посты из TG-каналов (только пересылка crosspost).
func (b *Bridge) handleTgChannelPost(ctx context.Context, msg *TGMessage) {
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

	go b.forwardTgToMax(ctx, msg, maxChatID, caption, true)
}

// handleTgCallback обрабатывает нажатия inline-кнопок (crosspost management).
func (b *Bridge) handleTgCallback(ctx context.Context, query *TGCallback) {
	if query.Message == nil || query.From == nil {
		return
	}
	data := query.Data
	chatID := query.Message.Chat.ID
	msgID := query.Message.MessageID

	fromID := query.From.ID

	// Опциональный аддон первым получает callback — если это его кнопка, обработает.
	if b.addon != nil {
		if b.addon.HandleCallback(ctx, fromID, chatID, query.ID, data) {
			return
		}
	}

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
			b.tg.AnswerCallback(ctx, query.ID, "Только владелец связки может изменять настройки.")
			return
		}
		b.repo.SetCrosspostDirection(maxChatID, dir)

		// Получаем title канала (из текста сообщения)
		title := parseTgCrosspostTitle(query.Message.Text)
		text := tgCrosspostStatusText(title, dir)
		kb := tgCrosspostKeyboard(dir, maxChatID, b.repo.GetCrosspostSyncEdits(maxChatID))
		b.tg.EditMessageText(ctx, chatID, msgID, text, &SendOpts{ReplyMarkup: kb})
		b.tg.AnswerCallback(ctx, query.ID, "Готово")
		return
	}

	// cps:maxChatID — toggle sync edits
	if strings.HasPrefix(data, "cps:") {
		maxChatID, err := strconv.ParseInt(strings.TrimPrefix(data, "cps:"), 10, 64)
		if err != nil {
			return
		}
		if !b.isCrosspostOwner(maxChatID, fromID) {
			b.tg.AnswerCallback(ctx, query.ID, "Только владелец связки может изменять настройки.")
			return
		}
		cur := b.repo.GetCrosspostSyncEdits(maxChatID)
		b.repo.SetCrosspostSyncEdits(maxChatID, !cur)
		title := parseTgCrosspostTitle(query.Message.Text)
		_, direction, _ := b.repo.GetCrosspostTgChat(maxChatID)
		text := tgCrosspostStatusText(title, direction)
		kb := tgCrosspostKeyboard(direction, maxChatID, !cur)
		b.tg.EditMessageText(ctx, chatID, msgID, text, &SendOpts{ReplyMarkup: kb})
		if !cur {
			b.tg.AnswerCallback(ctx, query.ID, "Синхронизация правок включена")
		} else {
			b.tg.AnswerCallback(ctx, query.ID, "Синхронизация правок выключена")
		}
		return
	}

	// cpu:maxChatID — unlink (show confirmation)
	if strings.HasPrefix(data, "cpu:") {
		maxChatID, err := strconv.ParseInt(strings.TrimPrefix(data, "cpu:"), 10, 64)
		if err != nil {
			return
		}
		if !b.isCrosspostOwner(maxChatID, fromID) {
			b.tg.AnswerCallback(ctx, query.ID, "Только владелец связки может удалять.")
			return
		}
		kb := NewInlineKeyboard(
			NewInlineRow(
				NewInlineButton("Да, удалить", fmt.Sprintf("cpuc:%d", maxChatID)),
				NewInlineButton("Отмена", fmt.Sprintf("cpux:%d", maxChatID)),
			),
		)
		b.tg.EditMessageText(ctx, chatID, msgID, "Удалить кросспостинг?", &SendOpts{ReplyMarkup: kb})
		b.tg.AnswerCallback(ctx, query.ID, "")
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
		b.tg.DeleteMessage(ctx, chatID, msgID)
		// Заголовок с кнопками добавления
		kb := tgReplacementsKeyboard(maxChatID)
		b.tg.SendMessage(ctx, chatID, formatReplacementsHeader(repl), &SendOpts{ReplyMarkup: kb})
		// Каждая замена — отдельное сообщение с кнопкой удаления
		for i, r := range repl.TgToMax {
			b.tg.SendMessage(ctx, chatID, formatReplacementItem(r, "tg>max"), &SendOpts{ParseMode: "HTML", ReplyMarkup: tgReplItemKeyboard("tg>max", i, id, r.Target)})
		}
		for i, r := range repl.MaxToTg {
			b.tg.SendMessage(ctx, chatID, formatReplacementItem(r, "max>tg"), &SendOpts{ParseMode: "HTML", ReplyMarkup: tgReplItemKeyboard("max>tg", i, id, r.Target)})
		}
		b.tg.AnswerCallback(ctx, query.ID, "")
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
		b.tg.EditMessageText(ctx, chatID, msgID, newText, &SendOpts{ParseMode: "HTML", ReplyMarkup: kb})
		label := "весь текст"
		if newTarget == "links" {
			label = "только ссылки"
		}
		b.tg.AnswerCallback(ctx, query.ID, "Тип: "+label)
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
		b.tg.EditMessageText(ctx, chatID, msgID, "Замена удалена.", nil)
		b.tg.AnswerCallback(ctx, query.ID, "Удалено")
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
		kb := NewInlineKeyboard(
			NewInlineRow(
				NewInlineButton("📝 Весь текст", "cprat:"+dir+":all:"+id),
				NewInlineButton("🔗 Только ссылки", "cprat:"+dir+":links:"+id),
			),
		)
		b.tg.EditMessageText(ctx, chatID, msgID,
			fmt.Sprintf("Добавление замены для %s.\nГде применять замену?", dirLabel), &SendOpts{ReplyMarkup: kb})
		b.tg.AnswerCallback(ctx, query.ID, "")
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
		b.tg.EditMessageText(ctx, chatID, msgID,
			fmt.Sprintf("Отправьте правило замены:\n<code>from | to</code>\n\nДля регулярного выражения:\n<code>/regex/ | to</code>\n\nНапример:\n<code>utm_source=tg | utm_source=max</code>"),
			&SendOpts{ParseMode: "HTML"})
		b.tg.AnswerCallback(ctx, query.ID, "")
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
		b.tg.EditMessageText(ctx, chatID, msgID, formatReplacementsHeader(repl), &SendOpts{ReplyMarkup: kb})
		b.tg.AnswerCallback(ctx, query.ID, "Очищено")
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
		kb := tgCrosspostKeyboard(direction, maxChatID, b.repo.GetCrosspostSyncEdits(maxChatID))
		b.tg.EditMessageText(ctx, chatID, msgID, text, &SendOpts{ReplyMarkup: kb})
		b.tg.AnswerCallback(ctx, query.ID, "")
		return
	}

	// cpuc:maxChatID — unlink confirmed
	if strings.HasPrefix(data, "cpuc:") {
		maxChatID, err := strconv.ParseInt(strings.TrimPrefix(data, "cpuc:"), 10, 64)
		if err != nil {
			return
		}
		if !b.isCrosspostOwner(maxChatID, fromID) {
			b.tg.AnswerCallback(ctx, query.ID, "Только владелец связки может удалять.")
			return
		}
		slog.Info("TG crosspost unlink", "maxChatID", maxChatID, "by", fromID)
		b.repo.UnpairCrosspost(maxChatID, fromID)
		b.tg.EditMessageText(ctx, chatID, msgID, "Кросспостинг удалён.", nil)
		b.tg.AnswerCallback(ctx, query.ID, "Удалено")
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
			b.tg.EditMessageText(ctx, chatID, msgID, "Кросспостинг не найден.", nil)
			b.tg.AnswerCallback(ctx, query.ID, "")
			return
		}
		title := parseTgCrosspostTitle(query.Message.Text)
		text := tgCrosspostStatusText(title, direction)
		kb := tgCrosspostKeyboard(direction, maxChatID, b.repo.GetCrosspostSyncEdits(maxChatID))
		b.tg.EditMessageText(ctx, chatID, msgID, text, &SendOpts{ReplyMarkup: kb})
		b.tg.AnswerCallback(ctx, query.ID, "")
		return
	}
}

// tgCrosspostKeyboard строит inline-клавиатуру для управления кросспостингом.
func tgCrosspostKeyboard(direction string, maxChatID int64, syncEdits bool) *InlineKeyboardMarkup {
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
	lblSync := "✏️ Синк правок"
	if syncEdits {
		lblSync = "✓ ✏️ Синк правок"
	}
	return NewInlineKeyboard(
		NewInlineRow(
			NewInlineButton(lblTgMax, "cpd:tg>max:"+id),
			NewInlineButton(lblMaxTg, "cpd:max>tg:"+id),
			NewInlineButton(lblBoth, "cpd:both:"+id),
		),
		NewInlineRow(
			NewInlineButton(lblSync, "cps:"+id),
			NewInlineButton("🔄 Замены", "cpr:"+id),
			NewInlineButton("❌ Удалить", "cpu:"+id),
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
func (b *Bridge) handleTgEditedChannelPost(ctx context.Context, edited *TGMessage) {
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
	if !b.repo.GetCrosspostSyncEdits(maxChatID) {
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
