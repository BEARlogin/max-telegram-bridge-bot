package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"time"

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

			if update.BusinessConnection != nil {
				slog.Info("TG business connection parsed", "user", update.BusinessConnection.UserID, "enabled", update.BusinessConnection.IsEnabled, "canReply", update.BusinessConnection.CanReply)
				b.addonTgBusinessConnection(ctx, update.BusinessConnection)
				continue
			}
			if update.EditedBusinessMessage != nil {
				b.addonTgBusinessMessage(ctx, update.EditedBusinessMessage, true)
				continue
			}
			if update.BusinessMessage != nil {
				b.addonTgBusinessMessage(ctx, update.BusinessMessage, false)
				continue
			}
			if update.DeletedBusinessMessages != nil {
				b.addonTgBusinessDeleted(ctx, update.DeletedBusinessMessages)
				continue
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
				// Модерация правок: спамер может опубликовать чистый текст (пройдёт фильтр),
				// затем изменить содержимое. Прогоняем edit через внешнюю проверку так же, как
				// новое сообщение — ДО bridge-зеркалирования и независимо от наличия связки
				// (иначе в standalone-группе ниже стоит `if !linked { continue }`).
				if isTgGroup(edited.Chat.Type) && edited.From != nil && !edited.IsService {
					isAdmin := false
					if isTgAnonymousAdmin(edited) {
						isAdmin = true
					} else if status, err := b.tg.GetChatMember(ctx, edited.Chat.ID, edited.From.ID); err == nil {
						isAdmin = isTgAdmin(status)
					}
					mtext := edited.Text
					if mtext == "" {
						mtext = edited.Caption
					}
					if edited.HasExternalReply {
						mtext = strings.TrimSpace(mtext + " " + edited.ExternalReplyText)
					}
					if b.moderateGroupMessage(ctx, GroupMessage{
						Platform: "tg", ChatID: edited.Chat.ID, UserID: edited.From.ID, UserName: edited.From.FirstName,
						TgMsgID: edited.MessageID, Text: mtext, ViaBot: edited.BotForward, HasLink: tgMsgHasLink(edited), IsAdmin: isAdmin,
						ExtReply: edited.HasExternalReply,
					}) {
						continue
					}
				}
				var maxChatID int64
				var linked bool
				maxChatID, linked = b.repo.GetThreadMaxChat(edited.Chat.ID, edited.MessageThreadID)
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
					go b.forwardTgToMax(ctx, edited, maxChatID, caption, false, false)
					continue
				}

				if !hasMapping {
					continue
				}

				prefix := b.hasPrefix("tg", edited.Chat.ID)

				// Правка АЛЬБОМА: в TG редактируется ОДНО сообщение группы, а MAX-альбом — это
				// одно сообщение с несколькими фото. EditMessage с единственным вложением затирает
				// остальные (в MAX остаётся 1 фото). Поэтому обновляем только текст, вообще не
				// отправляя attachments: MAX сохранит старые фото/видео.
				if edited.MediaGroupID != "" && hasMedia {
					rawText := edited.Caption
					editEntities := edited.CaptionEntities
					if rawText == "" {
						rawText = edited.Text
						editEntities = edited.Entities
					}
					if rawText == "" {
						continue
					}
					mdText := tgEntitiesToHTML(rawText, editEntities)
					name := tgName(edited)
					if prefix {
						name = "[TG] " + name
					}
					fwd := formatAttributionHTML(name, mdText, b.cfg.MessageNewline)
					if err := b.editMaxTextOnly(ctx, maxChatID, maxMsgID, fwd, "html"); err != nil {
						slog.Error("TG→MAX album caption edit failed", "err", err, "uid", tgUserID(edited), "tgChat", edited.Chat.ID, "maxMsgID", maxMsgID)
					} else {
						slog.Info("TG→MAX edited album caption", "mid", maxMsgID, "uid", tgUserID(edited), "tgChat", edited.Chat.ID)
					}
					continue
				}

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
				mdText := tgEntitiesToHTML(rawText, editEntities)
				name := tgName(edited)
				if prefix {
					name = "[TG] " + name
				}
				fwd := formatAttributionHTML(name, mdText, b.cfg.MessageNewline)
				m := maxbot.NewMessage().SetChat(maxChatID).SetText(fwd)
				m.SetFormat("html")
				if err := b.maxClientFor(ctx, maxChatID).Messages.EditMessage(ctx, maxMsgID, m); err != nil {
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
				b.observePrivateUser(ctx, "tg", msg.From.ID,
					strings.TrimSpace(msg.From.FirstName+" "+msg.From.LastName), msg.From.UserName)
			}

			// Опциональные аддоны (если подключены build-тегом) первыми получают
			// личные сообщения и форварды из каналов. Если аддон взял сообщение в
			// работу — бридж дальше не обрабатывает. В публичной сборке addon == nil.
			if msg.Chat.Type == "private" && b.addon != nil && msg.From != nil {
				// Пользователь выбрал группу нативной кнопкой (chat_shared) — запоминаем
				// её и отдаём расширению.
				if msg.ChatShared != nil {
					b.noteBotChat("tg", msg.ChatShared.ChatID, msg.ChatShared.Title, "supergroup")
					if b.chatShared(ctx, msg.From.ID, msg.ChatShared.RequestID, msg.ChatShared.ChatID, msg.ChatShared.Title) {
						continue
					}
				}
				if msg.ForwardOriginChat != nil && msg.ForwardOriginChat.Type == "channel" {
					if b.addon.HandleDMForward(ctx, msg.From.ID, msg.Chat.ID, msg.ForwardOriginChat.ID, msg.ForwardOriginChat.Title, msg.ForwardOriginMsgID) {
						continue
					}
				}
				if b.addon.HandleDMCommand(ctx, msg.From.ID, msg.Chat.ID, text) {
					continue
				}
				// Свободный текст в личке → AI-саппорт (только если включён режим /support).
				if b.addon.HandleDMText(ctx, msg.From.ID, msg.Chat.ID, text) {
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
				intro, kb := b.tgStartMenu()
				b.tg.SendMessage(ctx, msg.Chat.ID, intro,
					&SendOpts{ParseMode: "HTML", ThreadID: msg.MessageThreadID, ReplyMarkup: kb})
				continue
			}

			if text == "/doctor" {
				if msg.Chat.Type != "private" || msg.From == nil {
					b.tg.SendMessage(ctx, msg.Chat.ID,
						"Отчёт /doctor доступен только в личном диалоге с ботом.",
						&SendOpts{ThreadID: msg.MessageThreadID})
					continue
				}
				if !b.checkUserAllowed(ctx, msg.Chat.ID, msg.From.ID, msg.MessageThreadID) {
					continue
				}
				if !b.doctorTakeRate("tg", msg.From.ID, time.Now()) {
					b.tg.SendMessage(ctx, msg.Chat.ID,
						"Отчёт уже собирался. Повторите через 10 секунд.",
						&SendOpts{ThreadID: msg.MessageThreadID})
					continue
				}
				b.sendDoctorTG(ctx, msg.Chat.ID, msg.From.ID, msg.MessageThreadID)
				continue
			}

			// Обработка ввода замены (если юзер в режиме ожидания)
			if msg.Chat.Type == "private" && msg.From != nil && !strings.HasPrefix(text, "/") {
				if w, ok := b.getReplWait(msg.From.ID); ok {
					b.clearReplWait(msg.From.ID)
					rule, valid := parseReplacementInput(text)
					if !valid {
						b.tg.SendMessage(ctx, msg.Chat.ID, "Не получилось разобрать. Нужна одна строка с вертикальной чертой «|»:\n<code>что заменить | на что</code>\n\nНапример: <code>наш Телеграм | наш канал в MAX</code>\n\nПопробуйте ещё раз через кнопку «🔄 Замены».", &SendOpts{ParseMode: "HTML", ThreadID: msg.MessageThreadID})
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

			// /link <код> — привязать MAX-аккаунт к этому TG-аккаунту (код из MAX-кабинета)
			if msg.Chat.Type == "private" && strings.HasPrefix(text, "/link") && msg.From != nil {
				code := strings.TrimSpace(strings.TrimPrefix(text, "/link"))
				if code == "" {
					b.tg.SendMessage(ctx, msg.Chat.ID, "Чтобы связать аккаунты: в <b>личке MAX-бота</b> отправьте <code>/link</code> — он выдаст код. Затем пришлите его сюда: <code>/link КОД</code>\n\nMAX-бот: "+b.cfg.MaxBotURL, &SendOpts{ParseMode: "HTML"})
				} else if b.redeemLinkCode(ctx, code, msg.From.ID) {
					b.tg.SendMessage(ctx, msg.Chat.ID, "✅ Аккаунты связаны. Теперь это один аккаунт в боте — и из MAX, и из Telegram.", nil)
				} else {
					b.tg.SendMessage(ctx, msg.Chat.ID, "❌ Код неверный или истёк (живёт 10 минут). Сгенерируйте новый в кабинете.", nil)
				}
				continue
			}

			// /crosspost в личке TG — показать список связок
			if msg.Chat.Type == "private" && text == "/crosspost" {
				if !b.checkUserAllowed(ctx, msg.Chat.ID, msg.From.ID, msg.MessageThreadID) {
					continue
				}
				links := b.repo.ListCrossposts(msg.From.ID)
				if len(links) == 0 {
					b.tg.SendMessage(ctx, msg.Chat.ID,
						"Нет активных связок.\n\nНастройка: перешлите пост из TG-канала сюда, затем в MAX-боте /crosspost <ID>\n\nСвязывали канал раньше, но его нет в списке? Перешлите пост из этого канала сюда — бот обновит связку.", &SendOpts{ThreadID: msg.MessageThreadID})
				} else {
					for _, l := range links {
						kb := tgCrosspostKeyboard(l.Direction, l.MaxChatID, b.repo.GetCrosspostSyncEdits(l.MaxChatID), b.repo.CrosspostPaused(l.MaxChatID))
						tgTitle := b.tgChatTitle(ctx, l.TgChatID)
						statusText := tgCrosspostStatusText(tgTitle, l.Direction)
						if tgTitle == "" {
							statusText += fmt.Sprintf("\nTG: %d ↔ MAX: %d", l.TgChatID, l.MaxChatID)
						} else {
							statusText += fmt.Sprintf("\nTG: «%s» (%d)\nMAX: %d", tgTitle, l.TgChatID, l.MaxChatID)
						}
						b.tg.SendMessage(ctx, msg.Chat.ID, statusText, &SendOpts{ReplyMarkup: kb, ThreadID: msg.MessageThreadID})
					}
					b.tg.SendMessage(ctx, msg.Chat.ID,
						"Связывали канал раньше, но его нет в списке? Перешлите пост из этого канала сюда — бот обновит связку.\n\nНовая связка: перешлите пост из TG-канала сюда, затем в MAX-боте /crosspost <ID>", &SendOpts{ThreadID: msg.MessageThreadID})
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
					// Клейм владельца для старых связок без tg_owner_id (аналог /bridge_update
					// для каналов): форвард поста админом канала проставляет владельца,
					// чтобы связка появилась в /crosspost и кабинете.
					if _, tgOwner := b.repo.GetCrosspostOwner(maxChatID); tgOwner == 0 {
						if status, err := b.tg.GetChatMember(ctx, channelID, msg.From.ID); err == nil && isTgAdmin(status) {
							if b.repo.SetCrosspostOwner("tg", channelID, msg.From.ID) {
								text += "\n\n✅ Связка обновлена — канал теперь виден в списке /crosspost и в кабинете."
								slog.Info("crosspost owner claimed", "platform", "tg", "channel", channelID, "user", msg.From.ID)
							}
						}
					}
					kb := tgCrosspostKeyboard(direction, maxChatID, b.repo.GetCrosspostSyncEdits(maxChatID), b.repo.CrosspostPaused(maxChatID))
					b.tg.SendMessage(ctx, msg.Chat.ID, text, &SendOpts{ReplyMarkup: kb, ThreadID: msg.MessageThreadID})
					continue
				}

				b.tg.SendMessage(ctx, msg.Chat.ID,
					fmt.Sprintf("TG-канал «%s»\nID: <code>%d</code>\n\nВ личке MAX-бота напишите:\n<code>/crosspost %d</code>\n\nMAX-бот: %s\n\nЗатем перешлите пост из MAX-канала в личку MAX-бота.%s", channelTitle, channelID, channelID, b.cfg.MaxBotURL, b.reserveBotHint()),
					&SendOpts{ParseMode: "HTML", ThreadID: msg.MessageThreadID})
				continue
			}

			// Проверка прав админа в группах
			isGroup := isTgGroup(msg.Chat.Type)
			if isGroup {
				b.noteBotChat("tg", msg.Chat.ID, msg.Chat.Title, "group")
			}
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

			// Расширение получает групповые команды до встроенного роутинга.
			if isGroup && b.addon != nil && strings.HasPrefix(text, "/") {
				if b.addon.HandleTgGroupCommand(ctx, tgUserID(msg), msg.Chat.ID, msg.Chat.Type, text) {
					continue
				}
			}

			// Быстрые модер-команды: reply + /ban|/mute|/unban|/unmute (или с @user/id).
			if isGroup && b.addon != nil {
				if cmd, arg, ok := parseModCommand(text); ok {
					if !isAdmin {
						b.tg.SendMessage(ctx, msg.Chat.ID, adminDeniedText, &SendOpts{ThreadID: msg.MessageThreadID})
						continue
					}
					var targetID int64
					targetMsgID := 0
					// Цель из reply — если это живой юзер: не наш бот (зеркалированное из MAX
					// банить нечего на TG-стороне) и не служебный отправитель (пост канала,
					// анонимный админ — GroupAnonymousBot/Telegram/Channel_Bot).
					if rt := msg.ReplyToMessage; rt != nil && rt.From != nil &&
						!b.isSelfTgBot(rt.From) && !isTgServiceSender(rt.From.ID) {
						targetID = rt.From.ID
						targetMsgID = rt.MessageID
					}
					// Иначе цель из аргумента: @username (по таблице users) или числовой id.
					if targetID == 0 {
						if f := strings.Fields(arg); len(f) > 0 {
							tok := f[0]
							if strings.HasPrefix(tok, "@") {
								if id, found := b.repo.FindUserByUsername("tg", strings.TrimPrefix(tok, "@")); found {
									targetID = id
									arg = strings.TrimSpace(strings.TrimPrefix(arg, tok))
								}
							} else if id, err := strconv.ParseInt(tok, 10, 64); err == nil && id > 0 {
								targetID = id
								arg = strings.TrimSpace(strings.TrimPrefix(arg, tok))
							}
						}
					}
					if b.addon.HandleModCommand(ctx, "tg", msg.Chat.ID, tgUserID(msg), targetID, targetMsgID, msg.MessageID, cmd, arg) {
						continue
					}
				}
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

			// /bridge direction [tg>max|max>tg|both] — направление обычного bridge.
			if text == "/bridge direction" || strings.HasPrefix(text, "/bridge direction ") {
				if !b.checkUserAllowed(ctx, msg.Chat.ID, tgUserID(msg), msg.MessageThreadID) {
					continue
				}
				if isGroup && !isAdmin {
					b.tg.SendMessage(ctx, msg.Chat.ID, adminDeniedText, &SendOpts{ThreadID: msg.MessageThreadID})
					continue
				}
				maxChatID, linked := b.repo.GetMaxChat(msg.Chat.ID)
				if !linked {
					b.tg.SendMessage(ctx, msg.Chat.ID, "Чат не связан. Сначала выполните /bridge.", &SendOpts{ThreadID: msg.MessageThreadID})
					continue
				}
				arg := strings.TrimSpace(strings.TrimPrefix(text, "/bridge direction"))
				dir, ok := parsePairDirectionArg(arg)
				if !ok {
					b.tg.SendMessage(ctx, msg.Chat.ID, pairDirectionHelp(), &SendOpts{ThreadID: msg.MessageThreadID})
					continue
				}
				if dir == "" {
					cur := b.pairDirection(ctx, msg.Chat.ID, maxChatID)
					b.tg.SendMessage(ctx, msg.Chat.ID, "Текущее направление bridge: "+pairDirectionLabel(cur)+".\n"+pairDirectionHelp(), &SendOpts{ThreadID: msg.MessageThreadID})
					continue
				}
				if ok, reason := b.setPairDirection(ctx, tgUserID(msg), msg.Chat.ID, maxChatID, dir); !ok {
					b.tg.SendMessage(ctx, msg.Chat.ID, reason, &SendOpts{ThreadID: msg.MessageThreadID})
					continue
				}
				b.tg.SendMessage(ctx, msg.Chat.ID, "Готово. Направление bridge: "+pairDirectionLabel(dir)+".", &SendOpts{ThreadID: msg.MessageThreadID})
				continue
			}

			// /bridge_update — записать себя владельцем уже связанной группы,
			// чтобы она появилась в веб-кабинете (для старых связок без владельца).
			if text == "/bridge_update" {
				if isGroup && !isAdmin {
					b.tg.SendMessage(ctx, msg.Chat.ID, adminDeniedText, &SendOpts{ThreadID: msg.MessageThreadID})
					continue
				}
				if _, linked := b.repo.GetMaxChat(msg.Chat.ID); !linked {
					b.tg.SendMessage(ctx, msg.Chat.ID, "Эта группа не связана с MAX. Сначала свяжите её через /bridge.", &SendOpts{ThreadID: msg.MessageThreadID})
					continue
				}
				// Анонимный админ: From — общий GroupAnonymousBot, владельцем писать нельзя.
				if isTgServiceSender(tgUserID(msg)) {
					b.tg.SendMessage(ctx, msg.Chat.ID, "Вы пишете как анонимный админ — не могу определить ваш аккаунт. Отключите «Оставаться анонимным» в правах админа и повторите /bridge_update.", &SendOpts{ThreadID: msg.MessageThreadID})
					continue
				}
				if b.repo.SetPairOwner("tg", msg.Chat.ID, tgUserID(msg)) {
					b.tg.SendMessage(ctx, msg.Chat.ID, "Готово ✅ Связка обновлена — теперь группа доступна в веб-кабинете.", &SendOpts{ThreadID: msg.MessageThreadID})
					slog.Info("bridge_update owner set", "platform", "tg", "chat", msg.Chat.ID, "user", tgUserID(msg))
				} else {
					b.tg.SendMessage(ctx, msg.Chat.ID, "Не удалось обновить связку.", &SendOpts{ThreadID: msg.MessageThreadID})
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
				var bridgeUserID int64
				if msg.From != nil {
					bridgeUserID = msg.From.ID
				}
				// Анонимный админ/служебный отправитель: From = GroupAnonymousBot и т.п. —
				// ОБЩИЙ id всех анонимных админов. Владельца не определить (на 1087968824
				// накопилась 51 чужая связка → ложные отказы по лимиту) — просим отключить
				// анонимность и не создаём связку.
				if isGroup && isTgServiceSender(bridgeUserID) {
					b.tg.SendMessage(ctx, msg.Chat.ID,
						"Вы отправили /bridge как анонимный админ — бот не может определить ваш аккаунт для привязки связки.\n\nОтключите «Оставаться анонимным» в своих правах админа, повторите /bridge — после связки анонимность можно вернуть.",
						&SendOpts{ThreadID: msg.MessageThreadID})
					continue
				}
				// Гейт free-лимита связок (аддон, фича-флаг PAIR_FREE_LIMIT). ДО паринга:
				// при потреблении ключа знаем владельцев ОБЕИХ сторон (peek pending),
				// при генерации ключа — только свою (вторая проверится при потреблении).
				var pairMaxOwner int64
				if key != "" {
					if peerPlatform, _, peerUser, ok := b.repo.PeekBridgeKey(key); ok && peerPlatform == "max" {
						pairMaxOwner = peerUser
					}
				}
				if allowed, reason := b.pairAllowed(ctx, pairMaxOwner, bridgeUserID); !allowed {
					b.tg.SendMessage(ctx, msg.Chat.ID, reason, &SendOpts{ThreadID: msg.MessageThreadID})
					continue
				}
				paired, generatedKey, err := b.repo.Register(key, "tg", msg.Chat.ID, bridgeUserID)
				if err != nil {
					slog.Error("register failed", "err", err)
					continue
				}

				if paired {
					b.tg.SendMessage(ctx, msg.Chat.ID, "Связано! Сообщения теперь пересылаются.", &SendOpts{ThreadID: msg.MessageThreadID})
					b.repo.SetTgThreadID(msg.Chat.ID, msg.MessageThreadID) // 0 = no topics
					slog.Info("paired", "platform", "tg", "chat", msg.Chat.ID, "key", key)
					// Оба владельца известны (TG-сторона + пировавший MAX-юзер) — автопривязка аккаунтов.
					go b.autoLinkAccounts(ctx, pairMaxOwner, bridgeUserID)
				} else if generatedKey != "" {
					b.tg.SendMessage(ctx, msg.Chat.ID,
						fmt.Sprintf("Ключ для связки: <code>%s</code>\n\nДобавьте MAX-бота в нужную MAX-группу, сделайте его <b>администратором с правом «Доступ к сообщениям»</b> (читать все сообщения — иначе бот не увидит команду), и отправьте <b>в ней</b> (не в ЛС бота):\n<code>/bridge %s</code>\n\nСсылка на MAX-бота (для добавления в группу): %s%s", generatedKey, generatedKey, b.cfg.MaxBotURL, b.reserveBotHint()),
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

			// Пауза/возобновление связки групп (не удаляя её).
			if text == "/pause" || text == "/unpause" {
				if isGroup && !isAdmin {
					b.tg.SendMessage(ctx, msg.Chat.ID, adminDeniedText, &SendOpts{ThreadID: msg.MessageThreadID})
					continue
				}
				if !b.checkUserAllowed(ctx, msg.Chat.ID, tgUserID(msg), msg.MessageThreadID) {
					continue
				}
				maxChatID, linked := b.repo.GetMaxChat(msg.Chat.ID)
				if !linked {
					reply := "Этот чат не связан (пауза — для связки групп)."
					if msg.Chat.Type == "private" {
						reply = "Для управления паузой кросспостинга откройте /crosspost и нажмите кнопку под нужной связкой каналов."
					}
					b.tg.SendMessage(ctx, msg.Chat.ID, reply, &SendOpts{ThreadID: msg.MessageThreadID})
					continue
				}
				pause := text == "/pause"
				if err := b.repo.SetPairPaused(msg.Chat.ID, maxChatID, pause); err != nil {
					b.tg.SendMessage(ctx, msg.Chat.ID, "Не удалось изменить паузу.", &SendOpts{ThreadID: msg.MessageThreadID})
					continue
				}
				if pause {
					b.tg.SendMessage(ctx, msg.Chat.ID, "⏸ Связка на паузе — сообщения не зеркалятся (в обе стороны). Возобновить: /unpause", &SendOpts{ThreadID: msg.MessageThreadID})
				} else {
					b.tg.SendMessage(ctx, msg.Chat.ID, "▶️ Связка возобновлена — пересылка снова работает.", &SendOpts{ThreadID: msg.MessageThreadID})
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
				// General-топик форума имеет thread_id=0 — это валидный тред, связываем как
				// и любой другой (id=0). Маршрутизация ниже проверяет thread-bridge для всех id.
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
					fmt.Sprintf("Ключ для связки этого треда: <code>%s</code>\n\nДобавьте MAX-бота в отдельную MAX-группу (которая будет зеркалом этого треда) и отправьте <b>в ней</b>:\n<code>/thread_bridge %s</code>\n\nСсылка на MAX-бота: %s%s", key, key, b.cfg.MaxBotURL, b.reserveBotHint()),
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
				if b.repo.UnpairThread(msg.Chat.ID, msg.MessageThreadID) {
					b.tg.SendMessage(ctx, msg.Chat.ID, "Связка треда удалена.", &SendOpts{ThreadID: msg.MessageThreadID})
				} else {
					b.tg.SendMessage(ctx, msg.Chat.ID, "Этот тред не связан.", &SendOpts{ThreadID: msg.MessageThreadID})
				}
				continue
			}

			// Группа обсуждения канала: ловим авто-форвард поста (строим маппинг
			// пост↔тред) и передаём сообщения обсуждения расширению.
			if isGroup {
				// Вступления — капча/анти-рейд (аддон сам решает по конфигу).
				if len(msg.NewChatMembers) > 0 {
					for _, nm := range msg.NewChatMembers {
						if !b.isSelfTgBot(&nm) {
							b.memberJoined(ctx, "tg", msg.Chat.ID, nm.ID, strings.TrimSpace(nm.FirstName+" "+nm.LastName), nm.UserName, nm.IsBot)
						}
					}
					// Удаление служебного «вошёл», если включено владельцем (после капчи/рейда).
					if b.delServiceMsgs("tg", msg.Chat.ID) {
						b.tg.DeleteMessage(ctx, msg.Chat.ID, msg.MessageID)
					}
					continue // служебное сообщение о вступлении — дальше не обрабатываем
				}
				// Прочие служебные сообщения (вышел/смена названия/фото/закреп). Удаляем,
				// если включено — здесь, внутри isGroup, чтобы работало и в standalone-группах
				// (ниже стоит `if !linked continue`, который их бы не пропустил к общему хендлеру).
				if msg.IsService {
					// Вышел/удалён участник — сразу убираем его pending-капчу (иначе висит до
					// 90с-свипа, а пользователя уже нет в чате).
					if msg.LeftChatMember != nil {
						b.memberLeft(ctx, "tg", msg.Chat.ID, msg.LeftChatMember.ID)
					}
					if b.delServiceMsgs("tg", msg.Chat.ID) {
						b.tg.DeleteMessage(ctx, msg.Chat.ID, msg.MessageID)
					}
					continue
				}
				if msg.IsAutomaticForward {
					// Авто-форвард поста канала в группу обсуждения (корень треда) — НИКОГДА
					// не модерируем и не зеркалим. Маппинг пост↔тред сохраняем, только если
					// есть данные origin (иначе просто пропускаем — это контент канала, не спам).
					if msg.ForwardOriginChat != nil && msg.ForwardOriginMsgID != 0 {
						if err := b.repo.SaveDiscussionMessage(msg.ForwardOriginChat.ID, msg.ForwardOriginMsgID, msg.Chat.ID, msg.MessageID); err != nil {
							slog.Warn("save discussion map failed", "err", err)
						}
					}
					continue
				}
				// Внешняя обработка выполняется до передачи сообщения обсуждения.
				// SenderChat != nil — сообщение «от имени канала» (пост связанного канала,
				// репост канала): это контент канала, а не юзер-спам — не модерируем.
				if msg.From != nil && !msg.IsService && msg.SenderChat == nil {
					mtext := msg.Text
					if mtext == "" {
						mtext = msg.Caption
					}
					hasLink := tgMsgHasLink(msg)
					// Спам через цитату чужого канала (external_reply): чистый текст —
					// наживка, пейлоад в цитате. Подмешиваем текст цитаты в анализ; сам
					// факт external_reply — отдельный сильный сигнал (ExtReply).
					if msg.HasExternalReply {
						mtext = strings.TrimSpace(mtext + " " + msg.ExternalReplyText)
					}
					if b.moderateGroupMessage(ctx, GroupMessage{
						Platform: "tg", ChatID: msg.Chat.ID, UserID: msg.From.ID, UserName: msg.From.FirstName,
						TgMsgID: msg.MessageID, Text: mtext, ViaBot: msg.BotForward, HasLink: hasLink, IsAdmin: isAdmin,
						ExtReply: msg.HasExternalReply,
					}) {
						continue
					}
					// Качаем изображение только когда оно требуется расширению.
					// в чате включён (иначе не трогаем фото впустую).
					if len(msg.Photo) > 0 && b.wantImageCheck("tg", msg.Chat.ID) {
						if data, mime, err := b.tgPhotoBytes(ctx, msg.Photo); err == nil && len(data) > 0 {
							if b.moderateImage(ctx, "tg", msg.Chat.ID, msg.From.ID, msg.MessageID, "", data, mime, isAdmin) {
								continue
							}
						} else if err != nil {
							slog.Warn("antispam: photo download failed", "err", err, "chat", msg.Chat.ID)
						}
					}
				}
				go b.addonTgVKMessage(ctx, msg)
				if msg.MessageThreadID != 0 && msg.From != nil && !b.isSelfTgBot(msg.From) && !msg.IsService {
					chCh, chMsg, ok := b.repo.LookupChannelByDiscussion(msg.Chat.ID, msg.MessageThreadID)
					slog.Info("discussion reply seen", "discChat", msg.Chat.ID, "thread", msg.MessageThreadID, "mapped", ok, "channelPost", fmt.Sprintf("%d_%d", chCh, chMsg))
					if ok {
						dtext := msg.Text
						if dtext == "" {
							dtext = msg.Caption
						}
						if dtext != "" {
							// reply на конкретный коммент (а не на сам пост) — если
							// reply_to не равен корню треда (авто-форварду поста).
							replyToTg := 0
							if msg.ReplyToMessage != nil && msg.ReplyToMessage.MessageID != msg.MessageThreadID {
								replyToTg = msg.ReplyToMessage.MessageID
							}
							b.ingestDiscussionComment(ctx, chCh, chMsg, msg.From, dtext, msg.MessageID, replyToTg)
						}
						// Самостоятельные группы обсуждения не дублируем в MAX: комментарий
						// уже ушёл в commenter. Но явная bridge/thread-bridge связка означает,
						// что владелец хочет видеть весь чат в MAX, включая комментарии.
						_, threadLinked := b.repo.GetThreadMaxChat(msg.Chat.ID, msg.MessageThreadID)
						_, groupLinked := b.repo.GetMaxChat(msg.Chat.ID)
						if shouldSkipDiscussionRelay(threadLinked, groupLinked) {
							continue
						}
					}
				}
			}

			// Внешняя обработка уже выполнена выше.

			// Пересылка. Приоритет у thread-bridge (вкл. General, thread_id=0): если этот
			// тред связан отдельно — шлём в его MAX-чат, иначе фоллбэк на групповую связку.
			var maxChatID int64
			var linked, threadLinked bool
			maxChatID, linked = b.repo.GetThreadMaxChat(msg.Chat.ID, msg.MessageThreadID)
			threadLinked = linked
			if !linked {
				maxChatID, linked = b.repo.GetMaxChat(msg.Chat.ID)
			}
			if !linked {
				continue
			}
			if b.isSelfTgBot(msg.From) {
				continue
			}
			// Служебные сообщения (вступил/вышел/смена названия/закреп) не зеркалим —
			// иначе в MAX летит пустое сообщение.
			if msg.IsService {
				continue
			}
			// Нечего пересылать (сторис/опрос/кубик и прочие неподдерживаемые типы без
			// текста) — иначе в MAX уходит голый префикс «Имя:».
			if !tgHasContent(msg) {
				slog.Debug("skip TG msg without sendable content", "tgChat", msg.Chat.ID, "msgID", msg.MessageID)
				continue
			}
			if !threadLinked && !b.pairDirectionAllows(ctx, msg.Chat.ID, maxChatID, "tg>max") {
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
				documentID, documentName := tgMediaGroupDocument(msg)
				go b.bufferMediaGroup(ctx, msg.MediaGroupID, mediaGroupItem{
					photoSizes:     msg.Photo,
					videoFileID:    videoID,
					documentFileID: documentID,
					documentName:   documentName,
					caption:        caption,
					replyToMsg:     msg.ReplyToMessage,
					entities:       msg.CaptionEntities,
					msg:            msg,
					// Передаём уже резолвнутый maxChatID (в т.ч. thread-bridge): иначе flush
					// перерезолвит через GetMaxChat (вся группа) и дропнет альбом для связок,
					// где связан ТОЛЬКО тред (pairs пуст, есть только thread_pairs).
					maxChatID: maxChatID,
				})
				continue
			}

			go b.forwardTgToMax(ctx, msg, maxChatID, caption, false, false)
		}
	}
}

func shouldSkipDiscussionRelay(threadLinked, groupLinked bool) bool {
	return !threadLinked && !groupLinked
}

func tgUserID(msg *TGMessage) int64 {
	if msg.From != nil {
		return msg.From.ID
	}
	return 0
}

// forwardTgToMax пересылает TG-сообщение (текст/медиа) в MAX-чат.
// Если isCrosspost=true, caption используется как финальный текст (с заменами, без атрибуции).
// tgMsgHasLink — есть ли в сообщении ссылка/упоминание (по entities). Для правила
// внешней проверки сообщений.
func tgMsgHasLink(msg *TGMessage) bool {
	for _, e := range append(append([]Entity{}, msg.Entities...), msg.CaptionEntities...) {
		switch e.Type {
		case "url", "text_link", "mention", "text_mention":
			return true
		}
	}
	return false
}

// alreadyDeliveredToMax — для этого TG-сообщения уже есть сохранённый MAX-маппинг,
// т.е. оно уже доставлено. Защита от дублей при повторной обработке (вебхук-
// редоставка, реплей очереди после рестарта). Персистентно — переживает рестарт.
func (b *Bridge) alreadyDeliveredToMax(tgChatID int64, tgMsgID int) bool {
	if tgMsgID == 0 {
		return false
	}
	mid, ok := b.repo.LookupMaxMsgID(tgChatID, tgMsgID)
	return ok && mid != ""
}

func (b *Bridge) forwardTgToMax(ctx context.Context, msg *TGMessage, maxChatID int64, caption string, isCrosspost bool, bypassScreen bool) {
	// Дедуп: если это сообщение уже доставлено в MAX — не отправляем повторно.
	if b.alreadyDeliveredToMax(msg.Chat.ID, msg.MessageID) {
		slog.Info("skip duplicate TG→MAX", "tgChat", msg.Chat.ID, "tgMsg", msg.MessageID, "crosspost", isCrosspost)
		return
	}
	if b.cbBlocked(maxChatID) {
		return
	}
	// Пауза связки — временно не пересылаем (связку не удаляем). Возобновить: /unpause.
	if isCrosspost {
		if b.repo.CrosspostPaused(maxChatID) {
			return
		}
	} else if b.repo.PairPaused(msg.Chat.ID, maxChatID) {
		return
	}

	// Дуал-бот: выбираем бота для этого чата один раз и пробрасываем токен в ctx —
	// все аплоады/отправки этого релея пойдут одним и тем же ботом (тем, кто в чате).
	ctx = b.withMaxToken(ctx, b.maxTokenFor(ctx, maxChatID))
	mc := b.maxClientFor(ctx, maxChatID) // SDK-клиент бота этого чата (дуал)

	// Relay-гейт ТОЛЬКО для бридж-ГРУПП: там бот постит сообщение от своего имени и
	// получает страйк за спам/запрещёнку. Каналы (даже связанные через /bridge) и
	// кросспосты НЕ трогаем — там бот не отправитель. Проверка синхронная, до доставки.
	if !isCrosspost && !bypassScreen && isTgGroup(msg.Chat.Type) {
		// Скриним ТОЛЬКО тело сообщения, без форматированного caption (он содержит имя
		// автора). Вычурные ники (псевдо-кириллица, эмодзи) травили mixed-script/emoji-junk
		// и блокировали легит-сообщения — имя автора к контенту отношения не имеет.
		screenText := msg.Text
		if msg.Caption != "" {
			if screenText != "" {
				screenText += "\n"
			}
			screenText += msg.Caption
		}
		if block, reason := b.screenRelay(ctx, "tg", msg.Chat.ID, tgUserID(msg), screenText); block {
			slog.Warn("relay blocked TG→MAX (group): prohibited/spam",
				"tgChat", msg.Chat.ID, "tgMsg", msg.MessageID, "maxChat", maxChatID, "reason", reason)
			// Не дропаем тихо: кладём в очередь модерации (карточка с кнопками в чате).
			// payload — сериализованное сообщение для ре-релея на «Пропустить» (file_id внутри).
			payload, _ := json.Marshal(msg)
			b.holdForModeration(ctx, "tg", msg.Chat.ID, strconv.Itoa(msg.MessageID), maxChatID, tgUserID(msg), tgName(msg), screenText, caption, reason, string(payload))
			return
		}
	}

	uid := tgUserID(msg)

	// Опциональную кнопку мини-аппа запрашиваем у расширения.
	// Прикрепляется к самому сообщению при отправке (не отдельным сообщением).
	var openApp *maxOpenApp
	if isCrosspost {
		openApp = b.crosspostOpenApp(ctx, msg.Chat.ID, msg.MessageID, maxChatID)
	}

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
			mdText := tgEntitiesToHTML(rawText, msg.CaptionEntities)
			if fl := tgForwardLine(msg); fl != "" {
				mdText = fl + mdText
			}
			name := tgName(msg)
			if b.hasPrefix("tg", msg.Chat.ID) {
				name = "[TG] " + name
			}
			mdCaption = formatAttributionHTML(name, mdText, b.cfg.MessageNewline)
		}
		m := maxbot.NewMessage().SetChat(maxChatID).SetText(mdCaption)
		// Caption всегда markdown — и для bridge (с **жирной** атрибуцией),
		// и для crosspost (formatTgCrosspostCaption уже сконвертил entities).
		// При пустом тексте MAX отвергает payload с format — пропускаем.
		if mdCaption != "" {
			m.SetFormat("html")
		}
		if kb := b.openAppKeyboard(openApp); kb != nil {
			m.AddKeyboard(kb)
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
			if uploaded, err := mc.Uploads.UploadPhotoFromUrl(ctx, fileURL); err == nil {
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
		result, err := mc.Messages.SendWithResult(ctx, m)
		if err != nil {
			slog.Error("TG→MAX send failed", "err", err, "uid", uid, "tgChat", msg.Chat.ID, "maxChat", maxChatID)
			if isChatUnavailable(err.Error()) {
				b.cbPermanentFail(ctx, maxChatID) // бот недоступен в чате → связка на паузу + DM
			} else if b.cbFail(maxChatID) {
				b.notifyTgUser(ctx, msg, maxChatID,
					fmt.Sprintf("Не удалось переслать в MAX. Пересылка приостановлена на %d мин. Проверьте, что бот добавлен в MAX-чат и является админом.", int(cbCooldown.Minutes())), isCrosspost)
			}
		} else {
			b.cbSuccess(maxChatID)
			slog.Info("TG→MAX sent", "mid", result.Body.Mid)
			b.repo.SaveMsgOrigin(msg.Chat.ID, msg.MessageID, maxChatID, result.Body.Mid, msg.MessageThreadID, "tg")
			b.saveTgMediaState(msg)
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
				if uploaded, err := mc.Uploads.UploadPhotoFromUrl(ctx, fileURL); err == nil {
					m := maxbot.NewMessage().SetChat(maxChatID).SetText(caption)
					m.AddPhoto(uploaded)
					if msg.ReplyToMessage != nil {
						if maxReplyID, ok := b.repo.LookupMaxMsgID(msg.Chat.ID, msg.ReplyToMessage.MessageID); ok {
							m.SetReply(caption, maxReplyID)
						}
					}
					if kb := b.openAppKeyboard(openApp); kb != nil {
						m.AddKeyboard(kb)
					}
					slog.Info("TG→MAX sending sticker as photo", "uid", uid, "tgChat", msg.Chat.ID)
					result, err := mc.Messages.SendWithResult(ctx, m)
					if err != nil {
						slog.Error("TG→MAX sticker send failed", "err", err)
						b.notifyTgUser(ctx, msg, maxChatID, uploadErrMsg("Не удалось отправить стикер в MAX", err), isCrosspost)
					} else {
						slog.Info("TG→MAX sent", "mid", result.Body.Mid)
						b.repo.SaveMsgOrigin(msg.Chat.ID, msg.MessageID, maxChatID, result.Body.Mid, msg.MessageThreadID, "tg")
						b.saveTgMediaState(msg)
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
		mdText = tgEntitiesToHTML(rawText, entities)
	}

	// Контакт (sharing телефона) — текста нет, иначе зеркало пустое.
	if mdText == "" && msg.Contact != nil {
		mdText = tgContactText(msg)
	}
	// Метка репоста: «↪️ Переслано из X» перед телом.
	if fl := tgForwardLine(msg); fl != "" {
		mdText = fl + mdText
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
		mdCaption = formatAttributionHTML(name, mdText, b.cfg.MessageNewline)
	}

	// Если для этого чата уже есть сообщения в очереди — не отправляем напрямую,
	// чтобы не нарушить порядок. Сразу ставим в очередь.
	// Caption для crosspost уже содержит markdown (formatTgCrosspostCaption),
	// так что формат одинаков для обоих режимов.
	format := "html"

	if b.hasPendingForChat("tg2max", maxChatID) {
		slog.Info("TG→MAX queued (pending exists)", "uid", uid, "tgChat", msg.Chat.ID, "maxChat", maxChatID)
		b.enqueueTg2Max(msg.Chat.ID, msg.MessageID, maxChatID, mdCaption, mediaAttType, mediaToken, replyTo, format)
		return
	}

	var mid string
	var sendErr error

	if mediaAttType != "" {
		slog.Info("TG→MAX sending direct", "type", mediaAttType, "uid", uid, "tgChat", msg.Chat.ID, "maxChat", maxChatID)
		mid, sendErr = b.sendMaxDirectFormattedKb(ctx, maxChatID, mdCaption, mediaAttType, mediaToken, replyTo, format, openApp)
	} else {
		slog.Info("TG→MAX sending", "uid", uid, "tgChat", msg.Chat.ID, "maxChat", maxChatID)
		mid, sendErr = b.sendMaxDirectFormattedKb(ctx, maxChatID, mdCaption, "", "", replyTo, format, openApp)
	}

	if sendErr != nil {
		errStr := sendErr.Error()
		slog.Error("TG→MAX send failed", "err", errStr, "uid", uid, "tgChat", msg.Chat.ID, "maxChat", maxChatID)
		b.noteMaxSendErr(errStr) // глобальный бан аккаунта MAX → глушим уведомления
		// chat.not.found / chat.denied — перманентно: бота нет в MAX-группе или он не
		// админ. НЕ ретраим и сообщаем владельцу ТОЧНО (а не «MAX недоступен/в очереди»).
		notInChat := strings.Contains(errStr, "chat.not.found") || strings.Contains(errStr, "chat.denied")
		// 403/404 — permanent error, не ретраим
		if !strings.Contains(errStr, "403") && !strings.Contains(errStr, "404") && !strings.Contains(errStr, "chat.denied") {
			b.enqueueTg2Max(msg.Chat.ID, msg.MessageID, maxChatID, mdCaption, mediaAttType, mediaToken, replyTo, format)
		}
		if notInChat {
			// Бот недоступен в MAX-чате (удалён/не админ) — ставим связку на паузу
			// и DM-им владельцу (один раз, по переходу unpaused→paused).
			b.cbPermanentFail(ctx, maxChatID)
		} else if b.cbFail(maxChatID) {
			b.notifyTgUser(ctx, msg, maxChatID,
				"MAX API недоступен. Сообщения в очереди, будут доставлены автоматически.", isCrosspost)
		}
	} else {
		b.cbSuccess(maxChatID)
		slog.Info("TG→MAX sent", "mid", mid, "uid", uid, "tgChat", msg.Chat.ID, "maxChat", maxChatID)
		b.repo.SaveMsgOrigin(msg.Chat.ID, msg.MessageID, maxChatID, mid, msg.MessageThreadID, "tg")
		b.saveTgMediaState(msg)
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
	mdText := tgEntitiesToHTML(rawText, editEntities)
	name := tgName(msg)
	if b.hasPrefix("tg", msg.Chat.ID) {
		name = "[TG] " + name
	}
	mdCaption := formatAttributionHTML(name, mdText, b.cfg.MessageNewline)
	m.SetText(mdCaption)
	m.SetFormat("html")

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

	if err := b.maxClientFor(ctx, maxChatID).Messages.EditMessage(ctx, maxMsgID, m); err != nil {
		slog.Error("TG→MAX edit media failed", "err", err, "uid", uid, "tgChat", msg.Chat.ID, "maxMsgID", maxMsgID)
	} else {
		slog.Info("TG→MAX edited media", "mid", maxMsgID, "uid", uid, "tgChat", msg.Chat.ID)
	}
}

// handleTgChannelPost обрабатывает посты из TG-каналов (только пересылка crosspost).
func (b *Bridge) handleTgChannelPost(ctx context.Context, msg *TGMessage) {
	// Запоминаем канал (для мастера линковки в кабинете).
	b.noteBotChat("tg", msg.Chat.ID, msg.Chat.Title, "channel")
	// Команды в канале игнорируем — настройка через личку с ботом.
	// Расширение может обработать канальную команду.
	text := strings.TrimSpace(msg.Text)
	if strings.HasPrefix(text, "/") {
		if b.addon != nil {
			b.addon.HandleTgGroupCommand(ctx, 0, msg.Chat.ID, "channel", text)
		}
		return
	}

	// MAX→TG crosspost creates a normal Telegram channel_post update. Without
	// checking the persisted origin here, a bidirectional channel link sends
	// that bot-created post straight back to MAX and starts an endless loop.
	// This must run before addon fan-out too, otherwise the echo can leak to VK.
	if b.tgChannelPostCameFromMax(msg.Chat.ID, msg.MessageID) {
		slog.Info("skip mapped TG channel post (MAX echo)", "tgChannel", msg.Chat.ID, "tgMsg", msg.MessageID)
		return
	}

	// Дополнительная обработка поста расширением выполняется асинхронно.
	if b.addon != nil {
		go b.addonTgVKMessage(ctx, msg)
		go b.addon.HandleTgChannelPost(ctx, msg.Chat.ID, msg.MessageID, msg.MediaGroupID)
	}

	// Anti-loop
	checkText := msg.Text
	if checkText == "" {
		checkText = msg.Caption
	}
	if strings.HasPrefix(checkText, "[MAX]") || strings.HasPrefix(checkText, "[TG]") {
		return
	}

	// Пересылка crosspost: TG → MAX. Один TG-канал может иметь несколько MAX-адресатов,
	// а несколько TG-каналов могут сходиться в один MAX-чат.
	links := b.repo.GetCrosspostMaxChats(msg.Chat.ID)
	if len(links) == 0 {
		return
	}
	for _, link := range links {
		maxChatID, direction := link.MaxChatID, link.Direction
		if direction == "max>tg" {
			continue // только MAX→TG, пропускаем
		}
		// Аддон решает, доставлять ли пост конкретной связки (и сам уведомит владельца, если нет).
		if !b.crosspostDeliverablePair(ctx, msg.Chat.ID, maxChatID) {
			continue
		}
		// Идемпотентность per-destination: один исходный пост можно доставить в несколько
		// MAX-чатов, но нельзя дублировать в тот же адресат при ретрае вебхука.
		claimKey := strconv.Itoa(msg.MessageID) + ":" + strconv.FormatInt(maxChatID, 10)
		if !b.repo.ClaimCrosspost("tg", msg.Chat.ID, claimKey) {
			slog.Info("skip duplicate crosspost TG→MAX (already claimed)", "tgChannel", msg.Chat.ID, "tgMsg", msg.MessageID, "maxChat", maxChatID)
			continue
		}
		go b.publishTgCrosspost(ctx, msg, maxChatID, true)
	}
}

func (b *Bridge) tgChannelPostCameFromMax(tgChatID int64, tgMsgID int) bool {
	maxMsgID, ok := b.repo.LookupMaxMsgID(tgChatID, tgMsgID)
	if !ok {
		return false
	}
	origin, ok := b.repo.LookupTgMsgOrigin(maxMsgID)
	return ok && origin == "max"
}

// publishTgCrosspost формирует caption (markdown-разметка + замены TG→MAX) и
// публикует TG-пост в MAX как кросспост: альбом → буфер, иначе одиночное сообщение.
// Может вызываться асинхронно обработчиком канала или синхронно расширением.
// includeFooter управляет добавлением footer, который может вернуть расширение.
func (b *Bridge) publishTgCrosspost(ctx context.Context, msg *TGMessage, maxChatID int64, includeFooter bool) {
	b.publishTgCrosspostWithMode(ctx, msg, maxChatID, includeFooter, false)
}

func (b *Bridge) publishTgCrosspostWithMode(ctx context.Context, msg *TGMessage, maxChatID int64, includeFooter, manualAlbumFlush bool) {
	// Замены TG→MAX применяем на уровне (текст+entities) до HTML — чтобы вырезание
	// видимого текста ссылки убирало и сам text_link. Схлопывание пробелов — после.
	repl := b.repo.GetCrosspostReplacements(maxChatID)
	caption := formatTgCrosspostCaptionRepl(msg, repl.TgToMax)
	caption = collapseWhitespace(caption)

	// Footer расширения (раз в N постов). Для альбома — только на непустой части
	// (сборка альбома берёт caption первой НЕпустой части) и один вызов на альбом:
	// crosspostFooter инкрементит счётчик постов связки.
	if includeFooter && (msg.MediaGroupID == "" || caption != "") {
		caption += b.crosspostFooter(ctx, maxChatID, "max")
	}

	// Media group (альбом) — буферизуем и отправляем вместе
	if msg.MediaGroupID != "" {
		videoID := ""
		if msg.Video != nil {
			videoID = msg.Video.FileID
		}
		documentID, documentName := tgMediaGroupDocument(msg)
		item := mediaGroupItem{
			photoSizes:     msg.Photo,
			videoFileID:    videoID,
			documentFileID: documentID,
			documentName:   documentName,
			caption:        caption,
			replyToMsg:     msg.ReplyToMessage,
			entities:       msg.CaptionEntities,
			msg:            msg,
			maxChatID:      maxChatID,
			crosspost:      true,
		}
		if manualAlbumFlush {
			b.bufferMediaGroupManual(ctx, msg.MediaGroupID, item)
		} else {
			b.bufferMediaGroup(ctx, msg.MediaGroupID, item)
		}
		return
	}

	b.forwardTgToMax(ctx, msg, maxChatID, caption, true, false)
}

func tgMediaGroupDocument(msg *TGMessage) (fileID, name string) {
	if msg == nil || msg.Document == nil {
		return "", ""
	}
	name = msg.Document.FileName
	if name == "" {
		name = mimeToFilename("document", msg.Document.MimeType)
	}
	return msg.Document.FileID, name
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
		if b.addon.HandleCallback(ctx, fromID, chatID, query.ID, data, msgID) {
			return
		}
	}

	// Навигация по меню /start (help:*) — редактируем то же сообщение.
	if b.handleHelpMenuCallback(ctx, query, data) {
		return
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
		kb := tgCrosspostKeyboard(dir, maxChatID, b.repo.GetCrosspostSyncEdits(maxChatID), b.repo.CrosspostPaused(maxChatID))
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
		kb := tgCrosspostKeyboard(direction, maxChatID, !cur, b.repo.CrosspostPaused(maxChatID))
		b.tg.EditMessageText(ctx, chatID, msgID, text, &SendOpts{ReplyMarkup: kb})
		if !cur {
			b.tg.AnswerCallback(ctx, query.ID, "Синхронизация правок включена")
		} else {
			b.tg.AnswerCallback(ctx, query.ID, "Синхронизация правок выключена")
		}
		return
	}

	// cpp:maxChatID — toggle crosspost pause
	if strings.HasPrefix(data, "cpp:") {
		maxChatID, err := strconv.ParseInt(strings.TrimPrefix(data, "cpp:"), 10, 64)
		if err != nil {
			return
		}
		if !b.isCrosspostOwner(maxChatID, fromID) {
			b.tg.AnswerCallback(ctx, query.ID, "Только владелец связки может изменять настройки.")
			return
		}
		paused := !b.repo.CrosspostPaused(maxChatID)
		if err := b.repo.SetCrosspostPaused(maxChatID, paused); err != nil {
			b.tg.AnswerCallback(ctx, query.ID, "Не удалось изменить паузу.")
			return
		}
		tgChatID, direction, _ := b.repo.GetCrosspostTgChat(maxChatID)
		if !paused {
			b.cbSuccess(maxChatID)
			b.cbSuccess(tgChatID)
		}
		title := parseTgCrosspostTitle(query.Message.Text)
		text := tgCrosspostStatusText(title, direction)
		kb := tgCrosspostKeyboard(direction, maxChatID, b.repo.GetCrosspostSyncEdits(maxChatID), paused)
		b.tg.EditMessageText(ctx, chatID, msgID, text, &SendOpts{ReplyMarkup: kb})
		note := "Кросспостинг поставлен на паузу"
		if !paused {
			note = "Кросспостинг возобновлён"
		}
		b.tg.AnswerCallback(ctx, query.ID, note)
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
			"✏️ Какой текст на какой заменить?\n\n"+
				"Напишите одной строкой и разделите вертикальной чертой «|»:\n"+
				"<b>что заменить | на что заменить</b>\n\n"+
				"Примеры:\n"+
				"• <code>наш Телеграм | наш канал в MAX</code>\n"+
				"   — заменит эту фразу во всех постах\n"+
				"• <code>t.me/old_channel | max.ru/new_channel</code>\n"+
				"   — заменит ссылку\n"+
				"• <code>#реклама | </code>\n"+
				"   — удалит текст (правую часть оставили пустой)\n\n"+
				"Просто отправьте такую строку сообщением.\n\n"+
				"Для продвинутых (регулярное выражение): <code>/utm_source=\\w+/ | utm_source=max</code>",
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
		kb := tgCrosspostKeyboard(direction, maxChatID, b.repo.GetCrosspostSyncEdits(maxChatID), b.repo.CrosspostPaused(maxChatID))
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
		kb := tgCrosspostKeyboard(direction, maxChatID, b.repo.GetCrosspostSyncEdits(maxChatID), b.repo.CrosspostPaused(maxChatID))
		b.tg.EditMessageText(ctx, chatID, msgID, text, &SendOpts{ReplyMarkup: kb})
		b.tg.AnswerCallback(ctx, query.ID, "")
		return
	}
}

// tgCrosspostKeyboard строит inline-клавиатуру для управления кросспостингом.
func tgCrosspostKeyboard(direction string, maxChatID int64, syncEdits, paused bool) *InlineKeyboardMarkup {
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
	lblPause := "⏸ Поставить на паузу"
	if paused {
		lblPause = "▶️ Возобновить"
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
		NewInlineRow(NewInlineButton(lblPause, "cpp:"+id)),
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

	if currentMedia, hasMedia := tgMediaStateFromMessage(edited); hasMedia {
		previousMedia, known := b.repo.GetTgMediaState(edited.Chat.ID, edited.MessageID)
		mediaChanged := !known || previousMedia.Fingerprint != currentMedia.Fingerprint
		if mediaChanged {
			states := []TgMediaState{currentMedia}
			if edited.MediaGroupID != "" {
				states = replaceTgMediaState(
					b.repo.ListTgMediaStates(edited.Chat.ID, maxMsgID),
					currentMedia,
				)
				if err := validateAlbumMediaStates(states, edited.MediaGroupID); err != nil {
					// Старые альбомы ещё не имеют сохранённого состава. Запоминаем
					// увиденную часть, но не рискуем затереть остальные вложения.
					b.repo.SaveTgMediaState(edited.Chat.ID, currentMedia)
					slog.Warn("TG→MAX album media replacement deferred", "err", err,
						"tgChat", edited.Chat.ID, "tgMsg", edited.MessageID)
				} else {
					go b.editTgCrosspostMediaInMax(ctx, edited, states, maxChatID, maxMsgID, text)
					return
				}
			} else {
				go b.editTgCrosspostMediaInMax(ctx, edited, states, maxChatID, maxMsgID, text)
				return
			}
		}
	}

	// MAX трактует SDK EditMessage без явно добавленных вложений как полную замену
	// сообщения и удаляет уже опубликованные фото/видео. При правке подписи в
	// Telegram сами медиа не меняются, поэтому отправляем прямой PUT только с
	// полями text/format: отсутствие attachments в JSON сохраняет вложения MAX.
	if err := b.editMaxTextOnly(ctx, maxChatID, maxMsgID, text, ""); err != nil {
		slog.Error("TG→MAX crosspost edit failed", "err", err)
	} else {
		slog.Info("TG→MAX crosspost edited", "mid", maxMsgID)
	}
}
