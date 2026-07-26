package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	maxbot "github.com/max-messenger/max-bot-api-client-go"
	maxschemes "github.com/max-messenger/max-bot-api-client-go/schemes"
)

// maxShareAttachment keeps preview fields that the official SDK currently
// drops while decoding share attachments.
type maxShareAttachment struct {
	maxschemes.Attachment
	Payload     maxschemes.MediaAttachmentPayload `json:"payload"`
	Title       string                            `json:"title,omitempty"`
	Description string                            `json:"description,omitempty"`
	ImageURL    string                            `json:"image_url,omitempty"`
}

// parseMaxRawAttachments превращает сырой JSON-массив вложений MAX в концретные
// типы (PhotoAttachment, VideoAttachment, FileAttachment, и т.д.). Используется
// для forwarded-сообщений: SDK не парсит Link.Message.RawAttachments если
// body.RawAttachments выставлен в [] (а не nil).
func parseMaxRawAttachments(raw []json.RawMessage) []interface{} {
	out := make([]interface{}, 0, len(raw))
	for _, r := range raw {
		var base maxschemes.Attachment
		if err := json.Unmarshal(r, &base); err != nil {
			continue
		}
		var att interface{}
		switch base.Type {
		case maxschemes.AttachmentImage:
			a := &maxschemes.PhotoAttachment{}
			if err := json.Unmarshal(r, a); err == nil {
				att = a
			}
		case maxschemes.AttachmentVideo:
			a := &maxschemes.VideoAttachment{}
			if err := json.Unmarshal(r, a); err == nil {
				att = a
			}
		case maxschemes.AttachmentAudio:
			a := &maxschemes.AudioAttachment{}
			if err := json.Unmarshal(r, a); err == nil {
				att = a
			}
		case maxschemes.AttachmentFile:
			a := &maxschemes.FileAttachment{}
			if err := json.Unmarshal(r, a); err == nil {
				att = a
			}
		case maxschemes.AttachmentSticker:
			a := &maxschemes.StickerAttachment{}
			if err := json.Unmarshal(r, a); err == nil {
				att = a
			}
		case maxschemes.AttachmentShare:
			a := &maxShareAttachment{}
			if err := json.Unmarshal(r, a); err == nil {
				att = a
			}
		}
		if att != nil {
			out = append(out, att)
		}
	}
	return out
}

// openAppKeyboard собирает SDK-клавиатуру с одной OPEN_APP-кнопкой (для веток,
// где сообщение строится через maxbot.NewMessage()). nil — кнопки нет.
func (b *Bridge) openAppKeyboard(o *maxOpenApp) *maxbot.Keyboard {
	if o == nil || o.AppName == "" {
		return nil
	}
	kb := b.maxApi.Messages.NewKeyboardBuilder()
	kb.AddRow().AddOpenApp(o.Text, o.AppName, o.Payload, 0)
	return kb
}

// noteBotChatMax запоминает MAX-чат (для мастера линковки), с title из getChat.
// Троттлится через тот же botChatSeen, что и TG — getChat зовётся не чаще раза в 10 мин.
func (b *Bridge) noteBotChatMax(ctx context.Context, chatID int64, chatType string) {
	if chatID == 0 {
		return
	}
	key := "max:" + strconv.FormatInt(chatID, 10)
	now := time.Now().Unix()
	if v, ok := botChatSeen.Load(key); ok {
		if last, _ := v.(int64); now-last < 600 {
			return
		}
	}
	botChatSeen.Store(key, now)
	title := ""
	if ch, err := b.maxApi.Chats.GetChat(ctx, chatID); err == nil && ch != nil {
		title = ch.Title
	}
	b.repo.RecordBotChat("max", chatID, title, chatType)
}

func (b *Bridge) listenMax(ctx context.Context) {
	var updates <-chan maxschemes.UpdateInterface
	var oldUpdates <-chan maxschemes.UpdateInterface // вебхук старого бота (дуал); nil иначе

	if b.cfg.MaxWebhookURL != "" {
		whPath := b.maxWebhookPath()
		whURL := strings.TrimRight(b.cfg.MaxWebhookURL, "/") + whPath
		ch := make(chan maxschemes.UpdateInterface, 100)
		http.HandleFunc(whPath, b.maxApi.GetHandler(ch))
		updateTypes := []string{
			"message_created", "message_edited", "message_removed",
			"message_callback", "bot_added", "bot_removed",
			"user_added", "user_removed", "chat_title_changed",
			"bot_started", // кнопка «Начать» в MAX — иначе бот молчит на старте
		}
		if _, err := b.maxApi.Subscriptions.Subscribe(ctx, whURL, updateTypes, ""); err != nil {
			slog.Error("MAX webhook subscribe failed", "err", err)
			return
		}
		// Второй (старый) бот — отдельный путь вебхука, ТОТ ЖЕ канал ch (единый диспетч).
		// Дубли (оба бота в чате) отсекаются дедупом по mid в обработчике.
		if b.dualEnabled() {
			oldPath := whPath + "-old"
			oldURL := strings.TrimRight(b.cfg.MaxWebhookURL, "/") + oldPath
			// Отдельный канал для старого бота — чтобы в диспетчере знать, КТО доставил
			// апдейт (нужно для ответов в ЛС/группах тем же ботом, что получил сообщение).
			chOld := make(chan maxschemes.UpdateInterface, 100)
			http.HandleFunc(oldPath, b.maxApiOld.GetHandler(chOld))
			if _, err := b.maxApiOld.Subscriptions.Subscribe(ctx, oldURL, updateTypes, ""); err != nil {
				slog.Error("MAX old-bot webhook subscribe failed", "err", err)
			} else {
				oldUpdates = chOld
				slog.Info("MAX old-bot webhook subscribed", "path", oldPath)
			}
		}

		updates = ch
		slog.Info("MAX webhook mode")

		// При завершении — снимаем подписку, чтобы MAX не долбился в мёртвый URL
		// и чтобы при переключении в polling события сразу пошли (MAX иначе продолжит
		// слать в старый webhook с экспоненциальным backoff).
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := b.maxApi.Subscriptions.Unsubscribe(shutdownCtx, whURL); err != nil {
				slog.Error("MAX webhook unsubscribe failed", "err", err, "url", whURL)
			} else {
				slog.Info("MAX webhook unsubscribed", "url", whURL)
			}
		}()
	} else {
		updates = b.maxApi.GetUpdates(ctx)
		slog.Info("MAX polling mode")
	}

	// Слияние апдейтов обоих ботов с пометкой бота-источника (чтобы отвечать тем же ботом).
	type maxSrc struct {
		upd maxschemes.UpdateInterface
		tok string
	}
	merged := make(chan maxSrc, 200)
	go func() {
		for u := range updates {
			merged <- maxSrc{u, b.cfg.MaxToken}
		}
	}()
	if oldUpdates != nil {
		go func() {
			for u := range oldUpdates {
				merged <- maxSrc{u, b.cfg.MaxTokenOld}
			}
		}()
	}

	for {
		select {
		case <-ctx.Done():
			return
		case m, ok := <-merged:
			if !ok {
				return
			}
			upd := m.upd
			// Бот-источник апдейта → его токен в ctx: ответы (ЛС/группы/callback) уйдут
			// тем же ботом (он точно в этом чате). maxClientFor читает токен из ctx.
			ctx := b.withMaxToken(ctx, m.tok)

			slog.Debug("MAX update", "type", fmt.Sprintf("%T", upd))

			// Логируем выкидывание бота из чата — пара остаётся в БД (мб случайно
			// выкинули, потом вернут), но видно кто/когда удалил.
			if rm, isRm := upd.(*maxschemes.BotRemovedFromChatUpdate); isRm {
				tgChatID, linked := b.repo.GetTgChat(rm.ChatId)
				slog.Warn("MAX bot removed from chat",
					"maxChat", rm.ChatId,
					"byUser", rm.User.UserId,
					"byUsername", rm.User.Username,
					"byName", rm.User.Name,
					"linkedTgChat", tgChatID,
					"linked", linked)
				continue
			}
			if add, isAdd := upd.(*maxschemes.BotAddedToChatUpdate); isAdd {
				slog.Info("MAX bot added to chat",
					"maxChat", add.ChatId,
					"byUser", add.User.UserId,
					"byUsername", add.User.Username,
					"byName", add.User.Name)
				continue
			}

			// Кнопка «Начать» в MAX (bot_started) — отвечаем приветствием, как на /start.
			// Без этого новый бот молчит на старте (текст «/start» MAX не шлёт).
			if bs, isStart := upd.(*maxschemes.BotStartedUpdate); isStart {
				slog.Info("MAX bot started", "maxChat", bs.GetChatID(), "user", bs.GetUserID())
				b.sendMaxStart(ctx, bs.GetChatID())
				continue
			}

			// Вступление пользователя в MAX-чат. Событие приходит, когда бот является
			// администратором; аддон использует его для приветствия новых участников.
			if add, isAdd := upd.(*maxschemes.UserAddedToChatUpdate); isAdd {
				key := fmt.Sprintf("join:%d:%d:%d", add.ChatId, add.User.UserId, add.Timestamp)
				if b.maxDupMid(key) {
					continue
				}
				name := strings.TrimSpace(add.User.Name)
				if name == "" {
					name = strings.TrimSpace(add.User.FirstName + " " + add.User.LastName)
				}
				b.memberJoined(ctx, "max", add.ChatId, add.User.UserId, name, add.User.Username, add.User.IsBot)
				continue
			}

			// Обработка удаления (только bridge, не crosspost)
			if delUpd, isDel := upd.(*maxschemes.MessageRemovedUpdate); isDel {
				if b.isSuppressedMaxDelete(delUpd.MessageId) {
					slog.Info("MAX delete sync suppressed", "maxMid", delUpd.MessageId, "maxChat", delUpd.ChatID)
					continue
				}
				if b.addon != nil {
					b.addon.HandleMaxMessageRemoved(ctx, delUpd.ChatID, delUpd.MessageId)
				}
				tgChatID, tgMsgID, _, ok := b.repo.LookupTgMsgID(delUpd.MessageId)
				if !ok {
					continue
				}
				origin, _ := b.repo.LookupTgMsgOrigin(delUpd.MessageId)
				if !shouldSyncMaxDelete(origin) {
					slog.Info("MAX delete ignored for non-MAX source", "maxMid", delUpd.MessageId, "maxChat", delUpd.ChatID, "origin", origin)
					continue
				}
				// Delete sync для crosspost: проверяем настройку sync_edits и direction
				if maxCP, dir, cpOk := b.repo.GetCrosspostMaxChat(tgChatID); cpOk {
					if !b.repo.GetCrosspostSyncEdits(maxCP) || dir == "tg>max" {
						continue
					}
				}
				if err := b.tg.DeleteMessage(ctx, tgChatID, tgMsgID); err != nil {
					slog.Error("MAX→TG delete failed", "err", err, "maxMid", delUpd.MessageId, "tgChat", tgChatID)
				} else {
					slog.Info("MAX→TG deleted", "tgMsg", tgMsgID, "tgChat", tgChatID)
				}
				continue
			}

			// Обработка edit
			if editUpd, isEdit := upd.(*maxschemes.MessageEditedUpdate); isEdit {
				if b.isSelfMaxBot(editUpd.Message.Sender.UserId) {
					continue
				}
				// Дуал-бот: правка (message_edited) приходит от ОБОИХ ботов и НЕ дедупится
				// как message_created. Без этого оба бота обрабатывают edit, и при фолбэке
				// edit-media (re-send) пост ДУБЛИРУЕТСЯ дважды в TG. Дедуп по mid+содержимому:
				// повтор той же правки режется, реальная следующая правка (другой текст/медиа) — нет.
				if b.maxDupMid("edit:" + editUpd.Message.Body.Mid + ":" + editUpd.Message.Body.Text +
					":" + strconv.Itoa(len(editUpd.Message.Body.Attachments))) {
					continue
				}
				// Модерация правок: спамер может опубликовать чистый текст (пройдёт фильтр),
				// затем изменить содержимое. Прогоняем edit через внешнюю проверку ДО
				// зеркалирования и независимо от наличия маппинга (иначе ниже `if !ok continue`
				// пропустил бы правку в standalone-группе).
				if isMaxGroup(editUpd.Message.Recipient.ChatType) && editUpd.Message.Sender.UserId != 0 {
					eChat := editUpd.Message.Recipient.ChatId
					eText := editUpd.Message.Body.Text
					isAdmin := false
					if admins, err := b.maxApi.Chats.GetChatAdmins(ctx, eChat); err == nil {
						isAdmin = isMaxUserAdmin(admins.Members, editUpd.Message.Sender.UserId)
					}
					hasLink := b.maxTextHasLink(eText)
					if b.moderateGroupMessage(ctx, GroupMessage{
						Platform: "max", ChatID: eChat, UserID: editUpd.Message.Sender.UserId, UserName: editUpd.Message.Sender.Name,
						MaxMid: editUpd.Message.Body.Mid, Text: eText, HasLink: hasLink, IsAdmin: isAdmin,
					}) {
						continue
					}
				}
				mid := editUpd.Message.Body.Mid
				tgChatID, tgMsgID, _, ok := b.repo.LookupTgMsgID(mid)
				if !ok {
					continue
				}
				// Edit sync для crosspost: проверяем настройку sync_edits и direction
				if maxCP, dir, cpOk := b.repo.GetCrosspostMaxChat(tgChatID); cpOk {
					if !b.repo.GetCrosspostSyncEdits(maxCP) || dir == "tg>max" {
						continue
					}
				}
				prefix := b.hasPrefix("max", editUpd.Message.Recipient.ChatId)
				name := editUpd.Message.Sender.Name
				if name == "" {
					name = editUpd.Message.Sender.Username
				}
				text := editUpd.Message.Body.Text
				if strings.HasPrefix(text, "[TG]") || strings.HasPrefix(text, "[MAX]") {
					continue
				}

				// Конвертируем в HTML (всегда, для жирного имени автора)
				var fwd string
				var editParseMode string
				{
					var htmlText string
					if len(editUpd.Message.Body.Markups) > 0 {
						htmlText = maxMarkupsToHTML(text, editUpd.Message.Body.Markups)
					} else {
						htmlText = html.EscapeString(text)
					}
					escapedName := html.EscapeString(name)
					if prefix {
						escapedName = "[MAX] " + escapedName
					}
					if b.cfg.MessageNewline {
						fwd = "<b>" + escapedName + "</b>:\n" + htmlText
					} else {
						fwd = "<b>" + escapedName + "</b>: " + htmlText
					}
					editParseMode = "HTML"
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
					if text == "" {
						continue
					}
					editOpts := &SendOpts{ParseMode: editParseMode}
					if err := b.tg.EditMessageCaption(ctx, tgChatID, tgMsgID, fwd, editOpts); err != nil {
						slog.Error("MAX→TG edit caption failed", "err", err, "uid", editUpd.Message.Sender.UserId)
					} else {
						slog.Info("MAX→TG edited caption", "tgMsg", tgMsgID, "type", mediaType, "uid", editUpd.Message.Sender.UserId)
					}
					continue
				}

				if text == "" {
					continue
				}
				var editOpts *SendOpts
				if editParseMode != "" {
					editOpts = &SendOpts{ParseMode: editParseMode}
				}
				if err := b.tg.EditMessageText(ctx, tgChatID, tgMsgID, fwd, editOpts); err != nil {
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
				// Диагностика: видео-кружки MAX могут приходить НЕ как MessageCreatedUpdate —
				// логируем тип, чтобы понять, что дропается (кружки «не пересылаются»).
				slog.Debug("MAX non-message update skipped", "go_type", fmt.Sprintf("%T", upd))
				continue
			}
			// Дуал-бот: одно и то же сообщение приходит от ОБОИХ ботов (если оба в чате) —
			// обрабатываем один раз (mid уникален per-сообщение). Заодно метим бот чата.
			if b.maxDupMid(msgUpd.Message.Body.Mid) {
				continue
			}

			body := msgUpd.Message.Body
			chatID := msgUpd.Message.Recipient.ChatId
			text := strings.TrimSpace(body.Text)
			isDialog := msgUpd.Message.Recipient.ChatType == "dialog"

			slog.Debug("MAX msg received", "uid", msgUpd.Message.Sender.UserId, "chat", chatID, "type", msgUpd.Message.Recipient.ChatType,
				"textLen", len(body.Text), "att", len(body.Attachments), "rawAtt", len(body.RawAttachments), "markups", len(body.Markups))

			// MAX channel webhooks may report bot-created posts with sender uid=0, so the
			// sender-based self-bot check cannot stop the echo. A known mid means this
			// MAX message is already mapped to a Telegram message: either we just sent
			// it TG->MAX or this is a replay of an already delivered MAX->TG update.
			if b.maxMessageAlreadyMapped(body.Mid) {
				slog.Info("skip mapped MAX message (echo/replay)", "maxChat", chatID, "mid", body.Mid)
				continue
			}

			// Запоминаем юзера при личном сообщении
			if isDialog && msgUpd.Message.Sender.UserId != 0 {
				b.repo.TouchUser(msgUpd.Message.Sender.UserId, "max", msgUpd.Message.Sender.Username, msgUpd.Message.Sender.Name)
				b.observePrivateUser(ctx, "max", msgUpd.Message.Sender.UserId,
					msgUpd.Message.Sender.Name, msgUpd.Message.Sender.Username)
			}
			// Запоминаем MAX-чат/канал (для мастера линковки в кабинете).
			if !isDialog {
				b.noteBotChatMax(ctx, chatID, string(msgUpd.Message.Recipient.ChatType))
			}

			// Команды MAX-диалога, обрабатываемые аддоном. Ядро не знает семантику.
			if isDialog && strings.HasPrefix(text, "/") && b.maxAddonCommand(ctx, msgUpd.Message.Sender.UserId, msgUpd.Message.Sender.UserId, text) {
				continue
			}
			if isDialog && !strings.HasPrefix(text, "/") &&
				b.maxAddonText(ctx, msgUpd.Message.Sender.UserId, msgUpd.Message.Sender.UserId, text) {
				continue
			}
			// Групповые команды расширения.
			if !isDialog && strings.HasPrefix(text, "/") && b.addon != nil &&
				b.addon.HandleMaxGroupCommand(ctx, msgUpd.Message.Sender.UserId, chatID, string(msgUpd.Message.Recipient.ChatType), text) {
				continue
			}
			if !isDialog && !strings.HasPrefix(text, "/") {
				replyMid := body.ReplyTo
				if replyMid == "" && msgUpd.Message.Link != nil && msgUpd.Message.Link.Type == maxschemes.REPLY {
					replyMid = msgUpd.Message.Link.Message.Mid
				}
				if b.addonMaxReply(ctx, chatID, msgUpd.Message.Sender.UserId, body.Mid, replyMid, text) {
					continue
				}
			}

			if text == "/whoami" {
				m := maxbot.NewMessage().SetChat(chatID).SetText(
					"MaxTelegramBridgeBot — мост между Telegram и MAX.\n" +
						"Автор: Andrey Lugovskoy (@BEARlogin)\n" +
						"Исходники: https://github.com/BEARlogin/max-telegram-bridge-bot\n" +
						"Лицензия: CC BY-NC 4.0")
				b.maxClientFor(ctx, chatID).Messages.Send(ctx, m)
				continue
			}

			if text == "/start" || text == "/help" {
				b.sendMaxStart(ctx, chatID)
				continue
			}

			if text == "/doctor" {
				if !isDialog || msgUpd.Message.Sender.UserId == 0 {
					m := maxbot.NewMessage().SetChat(chatID).
						SetText("Отчёт /doctor доступен только в личном диалоге с ботом.")
					_ = b.maxClientFor(ctx, chatID).Messages.Send(ctx, m)
					continue
				}
				if !b.doctorTakeRate("max", msgUpd.Message.Sender.UserId, time.Now()) {
					m := maxbot.NewMessage().SetChat(chatID).
						SetText("Отчёт уже собирался. Повторите через 10 секунд.")
					_ = b.maxClientFor(ctx, chatID).Messages.Send(ctx, m)
					continue
				}
				b.sendDoctorMax(ctx, chatID, msgUpd.Message.Sender.UserId)
				continue
			}

			// Проверка прав админа в группах
			isGroup := isMaxGroup(msgUpd.Message.Recipient.ChatType)
			isAdmin := false
			if isGroup && msgUpd.Message.Sender.UserId != 0 {
				// Через бота, который в чате (dual-bot): иначе новый бот в old-only чате
				// не прочитает админов → владелец/админ ошибочно считается не-админом.
				admins, err := b.maxClientFor(ctx, chatID).Chats.GetChatAdmins(ctx, chatID)
				if err == nil {
					isAdmin = isMaxUserAdmin(admins.Members, msgUpd.Message.Sender.UserId)
				}
			} else if isGroup {
				// В каналах MAX не передаёт sender userId — пропускаем проверку
				isAdmin = true
			}

			// Внешняя модерация сообщения до пересылки.
			if isGroup && msgUpd.Message.Sender.UserId != 0 {
				hasLink := b.maxTextHasLink(text)
				if b.moderateGroupMessage(ctx, GroupMessage{
					Platform: "max",
					ChatID:   chatID,
					UserID:   msgUpd.Message.Sender.UserId,
					UserName: msgUpd.Message.Sender.Name,
					MaxMid:   body.Mid,
					Text:     text,
					HasLink:  hasLink,
					IsAdmin:  isAdmin,
				}) {
					continue
				}
			}

			// /bridge prefix on/off
			if text == "/bridge prefix on" || text == "/bridge prefix off" {
				if isGroup && !isAdmin {
					m := maxbot.NewMessage().SetChat(chatID).SetText("Эта команда доступна только админам группы.")
					b.maxClientFor(ctx, chatID).Messages.Send(ctx, m)
					continue
				}
				on := text == "/bridge prefix on"
				if b.repo.SetPrefix("max", chatID, on) {
					reply := "Префикс [TG]/[MAX] включён."
					if !on {
						reply = "Префикс [TG]/[MAX] выключен."
					}
					m := maxbot.NewMessage().SetChat(chatID).SetText(reply)
					b.maxClientFor(ctx, chatID).Messages.Send(ctx, m)
				} else {
					m := maxbot.NewMessage().SetChat(chatID).SetText("Чат не связан. Сначала выполните /bridge.")
					b.maxClientFor(ctx, chatID).Messages.Send(ctx, m)
				}
				continue
			}

			// /bridge direction [tg>max|max>tg|both] — направление обычного bridge.
			if text == "/bridge direction" || strings.HasPrefix(text, "/bridge direction ") {
				if isGroup && !isAdmin {
					m := maxbot.NewMessage().SetChat(chatID).SetText("Эта команда доступна только админам группы.")
					b.maxClientFor(ctx, chatID).Messages.Send(ctx, m)
					continue
				}
				tgChatID, linked := b.repo.GetTgChat(chatID)
				if !linked {
					m := maxbot.NewMessage().SetChat(chatID).SetText("Чат не связан. Сначала выполните /bridge.")
					b.maxClientFor(ctx, chatID).Messages.Send(ctx, m)
					continue
				}
				arg := strings.TrimSpace(strings.TrimPrefix(text, "/bridge direction"))
				dir, ok := parsePairDirectionArg(arg)
				if !ok {
					m := maxbot.NewMessage().SetChat(chatID).SetText(pairDirectionHelp())
					b.maxClientFor(ctx, chatID).Messages.Send(ctx, m)
					continue
				}
				if dir == "" {
					cur := b.pairDirection(ctx, tgChatID, chatID)
					m := maxbot.NewMessage().SetChat(chatID).SetText("Текущее направление bridge: " + pairDirectionLabel(cur) + ".\n" + pairDirectionHelp())
					b.maxClientFor(ctx, chatID).Messages.Send(ctx, m)
					continue
				}
				if ok, reason := b.setPairDirection(ctx, msgUpd.Message.Sender.UserId, tgChatID, chatID, dir); !ok {
					m := maxbot.NewMessage().SetChat(chatID).SetText(reason)
					b.maxClientFor(ctx, chatID).Messages.Send(ctx, m)
					continue
				}
				m := maxbot.NewMessage().SetChat(chatID).SetText("Готово. Направление bridge: " + pairDirectionLabel(dir) + ".")
				b.maxClientFor(ctx, chatID).Messages.Send(ctx, m)
				continue
			}

			// /bridge_update — записать себя владельцем уже связанной группы (MAX-сторона).
			if text == "/bridge_update" {
				if isGroup && !isAdmin {
					m := maxbot.NewMessage().SetChat(chatID).SetText("Эта команда доступна только админам группы.")
					b.maxClientFor(ctx, chatID).Messages.Send(ctx, m)
					continue
				}
				if _, linked := b.repo.GetTgChat(chatID); !linked {
					m := maxbot.NewMessage().SetChat(chatID).SetText("Эта группа не связана с Telegram. Сначала свяжите её через /bridge.")
					b.maxClientFor(ctx, chatID).Messages.Send(ctx, m)
					continue
				}
				var reply string
				if b.repo.SetPairOwner("max", chatID, msgUpd.Message.Sender.UserId) {
					reply = "Готово ✅ Связка обновлена."
					slog.Info("bridge_update owner set", "platform", "max", "chat", chatID, "user", msgUpd.Message.Sender.UserId)
				} else {
					reply = "Не удалось обновить связку."
				}
				m := maxbot.NewMessage().SetChat(chatID).SetText(reply)
				b.maxClientFor(ctx, chatID).Messages.Send(ctx, m)
				continue
			}

			// /bridge или /bridge <key>
			if text == "/bridge" || strings.HasPrefix(text, "/bridge ") {
				if isGroup && !isAdmin {
					m := maxbot.NewMessage().SetChat(chatID).SetText("Эта команда доступна только админам группы.")
					b.maxClientFor(ctx, chatID).Messages.Send(ctx, m)
					continue
				}
				key := strings.TrimSpace(strings.TrimPrefix(text, "/bridge"))

				// /bridge без ключа — если чат уже связан, не создавать новый ключ
				if key == "" {
					if tgIDs := b.repo.GetTgChats(chatID); len(tgIDs) > 0 {
						var reply string
						if isGroup {
							if len(tgIDs) == 1 {
								reply = fmt.Sprintf("Эта группа уже связана с Telegram (ID %d).\n\n"+
									"Чтобы слить сюда ещё одну TG-группу (несколько TG → одна MAX): "+
									"в новой TG-группе отправьте /bridge, получите ключ и введите его здесь — /bridge <ключ>.\n"+
									"Направление каждой связки настраивается отдельно: /bridge direction tg>max|max>tg|both.\n\n"+
									"/unbridge — удалить связку.", tgIDs[0])
							} else {
								ids := make([]string, 0, len(tgIDs))
								for _, id := range tgIDs {
									ids = append(ids, strconv.FormatInt(id, 10))
								}
								reply = fmt.Sprintf("В эту MAX-группу слито %d TG-групп (ID: %s).\n\n"+
									"Добавить ещё одну: в новой TG-группе /bridge → ключ → здесь /bridge <ключ>.\n"+
									"Направление каждой связки — отдельно (/bridge direction). По умолчанию новая связка двусторонняя.\n\n"+
									"/unbridge — удалить все связки этой группы.", len(tgIDs), strings.Join(ids, ", "))
							}
						} else {
							tgID := tgIDs[0]
							reply = fmt.Sprintf("Этот личный чат уже связан с Telegram (ID %d).\n\nЧтобы связать группу — добавьте бота в неё и отправьте /bridge внутри группы, не здесь.\n\n/unbridge — удалить связку этого личного чата.", tgID)
						}
						m := maxbot.NewMessage().SetChat(chatID).SetText(reply)
						b.maxClientFor(ctx, chatID).Messages.Send(ctx, m)
						continue
					}
					if !isGroup {
						m := maxbot.NewMessage().SetChat(chatID).SetText(
							"Чтобы связать группу — добавьте бота в неё и отправьте /bridge внутри группы, не здесь.\n\nЕсли хотите связать этот личный чат с Telegram-пользователем — введите ключ от него: /bridge <ключ>.")
						b.maxClientFor(ctx, chatID).Messages.Send(ctx, m)
						continue
					}
				}
				// Гейт free-лимита связок (аддон, фича-флаг PAIR_FREE_LIMIT). ДО паринга:
				// при потреблении ключа знаем владельцев ОБЕИХ сторон (peek pending),
				// при генерации ключа — только свою (вторая проверится при потреблении).
				var pairTgOwner, pairTgChatID int64
				if key != "" {
					if peerPlatform, peerChat, peerUser, ok := b.repo.PeekBridgeKey(key); ok && peerPlatform == "tg" {
						pairTgOwner = peerUser
						pairTgChatID = peerChat
					}
				}
				if allowed, reason := b.pairAllowed(ctx, msgUpd.Message.Sender.UserId, pairTgOwner); !allowed {
					m := maxbot.NewMessage().SetChat(chatID).SetText(reason)
					b.maxClientFor(ctx, chatID).Messages.Send(ctx, m)
					continue
				}
				if allowed, reason := b.addonPairDestinationAllowed(ctx, key, "max", chatID, msgUpd.Message.Sender.UserId); !allowed {
					m := maxbot.NewMessage().SetChat(chatID).SetText(reason)
					b.maxClientFor(ctx, chatID).Messages.Send(ctx, m)
					continue
				}
				paired, generatedKey, err := b.repo.Register(key, "max", chatID, msgUpd.Message.Sender.UserId)
				if err != nil {
					slog.Error("register failed", "err", err)
					continue
				}

				if paired {
					if !b.addonPairCompleted(ctx, key, pairTgChatID, chatID) {
						b.repo.Unpair("max", chatID)
						m := maxbot.NewMessage().SetChat(chatID).SetText("Не удалось завершить настройку зеркала. Связка отменена, попробуйте ещё раз.")
						b.maxClientFor(ctx, chatID).Messages.Send(ctx, m)
						continue
					}
					m := maxbot.NewMessage().SetChat(chatID).SetText("Связано! Сообщения теперь пересылаются.")
					b.maxClientFor(ctx, chatID).Messages.Send(ctx, m)
					slog.Info("paired", "platform", "max", "chat", chatID, "key", key)
					// Оба владельца известны (MAX-сторона + пировавший TG-юзер) — автопривязка аккаунтов.
					go b.autoLinkAccounts(ctx, msgUpd.Message.Sender.UserId, pairTgOwner)
				} else if generatedKey != "" {
					m := maxbot.NewMessage().SetChat(chatID).
						SetText(fmt.Sprintf("Ключ для связки: %s\n\nДобавьте TG-бота в нужную Telegram-группу и отправьте в ней (не в ЛС бота):\n/bridge %s\n\nСсылка на TG-бота (для добавления в группу): %s", generatedKey, generatedKey, b.cfg.TgBotURL))
					b.maxClientFor(ctx, chatID).Messages.Send(ctx, m)
					slog.Info("pending", "platform", "max", "chat", chatID, "key", generatedKey)
				} else {
					m := maxbot.NewMessage().SetChat(chatID).SetText("Ключ не найден или чат той же платформы.")
					b.maxClientFor(ctx, chatID).Messages.Send(ctx, m)
				}
				continue
			}

			if text == "/unbridge" {
				if isGroup && !isAdmin {
					m := maxbot.NewMessage().SetChat(chatID).SetText("Эта команда доступна только админам группы.")
					b.maxClientFor(ctx, chatID).Messages.Send(ctx, m)
					continue
				}
				if b.repo.Unpair("max", chatID) {
					m := maxbot.NewMessage().SetChat(chatID).SetText("Связка удалена.")
					b.maxClientFor(ctx, chatID).Messages.Send(ctx, m)
				} else {
					m := maxbot.NewMessage().SetChat(chatID).SetText("Этот чат не связан.")
					b.maxClientFor(ctx, chatID).Messages.Send(ctx, m)
				}
				continue
			}

			// Пауза/возобновление связки групп (не удаляя её).
			if text == "/pause" || text == "/unpause" {
				if isGroup && !isAdmin {
					m := maxbot.NewMessage().SetChat(chatID).SetText("Эта команда доступна только админам группы.")
					b.maxClientFor(ctx, chatID).Messages.Send(ctx, m)
					continue
				}
				tgChats := b.repo.GetTgChats(chatID)
				if len(tgChats) == 0 {
					reply := "Этот чат не связан (пауза — для связки групп)."
					if isDialog {
						reply = "Для управления паузой кросспостинга откройте /crosspost и нажмите кнопку под нужной связкой каналов."
					}
					m := maxbot.NewMessage().SetChat(chatID).SetText(reply)
					b.maxClientFor(ctx, chatID).Messages.Send(ctx, m)
					continue
				}
				pause := text == "/pause"
				var pauseErr error
				for _, tgChatID := range tgChats {
					if err := b.repo.SetPairPaused(tgChatID, chatID, pause); err != nil {
						pauseErr = err
					}
				}
				if pauseErr != nil {
					m := maxbot.NewMessage().SetChat(chatID).SetText("Не удалось изменить паузу.")
					b.maxClientFor(ctx, chatID).Messages.Send(ctx, m)
					continue
				}
				txt := "▶️ Связка возобновлена — пересылка снова работает."
				if pause {
					txt = "⏸ Связка на паузе — сообщения не зеркалятся (в обе стороны). Возобновить: /unpause"
				}
				b.maxClientFor(ctx, chatID).Messages.Send(ctx, maxbot.NewMessage().SetChat(chatID).SetText(txt))
				continue
			}

			// /thread_bridge <key> — принять ключ, связать MAX-чат с конкретным TG-тредом
			if strings.HasPrefix(text, "/thread_bridge") {
				if isGroup && !isAdmin {
					m := maxbot.NewMessage().SetChat(chatID).SetText("Эта команда доступна только админам группы.")
					b.maxClientFor(ctx, chatID).Messages.Send(ctx, m)
					continue
				}
				key := strings.TrimSpace(strings.TrimPrefix(text, "/thread_bridge"))
				if key == "" {
					if tgID, tid, ok := b.repo.GetThreadTgPair(chatID); ok {
						m := maxbot.NewMessage().SetChat(chatID).SetText(
							fmt.Sprintf("Этот чат уже связан с TG-тредом (чат %d, thread %d).\n\n/thread_unbridge — разорвать.", tgID, tid))
						b.maxClientFor(ctx, chatID).Messages.Send(ctx, m)
						continue
					}
					m := maxbot.NewMessage().SetChat(chatID).SetText(
						"Нужен ключ: /thread_bridge <ключ>\n\nСначала в Telegram-форум-группе выполните /thread_bridge внутри нужного треда — там выдадут ключ.")
					b.maxClientFor(ctx, chatID).Messages.Send(ctx, m)
					continue
				}
				tgChatID, threadID, ok, err := b.repo.CompleteThreadBridge(key, chatID)
				if err == errThreadMaxBusy {
					m := maxbot.NewMessage().SetChat(chatID).SetText(
						"Этот MAX-чат уже участвует в другой связке (bridge или thread-bridge). Сначала /unbridge или /thread_unbridge.")
					b.maxClientFor(ctx, chatID).Messages.Send(ctx, m)
					continue
				}
				if err != nil {
					slog.Error("CompleteThreadBridge failed", "err", err)
					m := maxbot.NewMessage().SetChat(chatID).SetText("Ошибка сохранения связки.")
					b.maxClientFor(ctx, chatID).Messages.Send(ctx, m)
					continue
				}
				if !ok {
					m := maxbot.NewMessage().SetChat(chatID).SetText("Ключ не найден или истёк.")
					b.maxClientFor(ctx, chatID).Messages.Send(ctx, m)
					continue
				}
				m := maxbot.NewMessage().SetChat(chatID).SetText(
					fmt.Sprintf("Связано с TG-тредом (чат %d, thread %d). Сообщения из этого MAX-чата будут уходить в указанный тред, и обратно.", tgChatID, threadID))
				b.maxClientFor(ctx, chatID).Messages.Send(ctx, m)
				slog.Info("thread-bridge paired", "tgChat", tgChatID, "thread", threadID, "maxChat", chatID)
				continue
			}

			if text == "/thread_unbridge" {
				if isGroup && !isAdmin {
					m := maxbot.NewMessage().SetChat(chatID).SetText("Эта команда доступна только админам группы.")
					b.maxClientFor(ctx, chatID).Messages.Send(ctx, m)
					continue
				}
				if b.repo.UnpairThreadByMax(chatID) {
					m := maxbot.NewMessage().SetChat(chatID).SetText("Связка треда удалена.")
					b.maxClientFor(ctx, chatID).Messages.Send(ctx, m)
				} else {
					m := maxbot.NewMessage().SetChat(chatID).SetText("Этот чат не связан с тредом.")
					b.maxClientFor(ctx, chatID).Messages.Send(ctx, m)
				}
				continue
			}

			// Обработка ввода замены (если юзер в режиме ожидания)
			if isDialog && !strings.HasPrefix(text, "/") && msgUpd.Message.Sender.UserId != 0 {
				if w, ok := b.getReplWait(msgUpd.Message.Sender.UserId); ok {
					b.clearReplWait(msgUpd.Message.Sender.UserId)
					rule, valid := parseReplacementInput(text)
					if !valid {
						m := maxbot.NewMessage().SetChat(chatID).SetText("Не получилось разобрать. Нужна одна строка с вертикальной чертой «|»:\nчто заменить | на что\n\nНапример: наш Телеграм | наш канал в MAX\n\nПопробуйте ещё раз через кнопку «🔄 Замены».")
						b.maxClientFor(ctx, chatID).Messages.Send(ctx, m)
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
						b.maxClientFor(ctx, chatID).Messages.Send(ctx, m)
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
					b.maxClientFor(ctx, chatID).Messages.Send(ctx, m)
					continue
				}
			}

			// /link в личке MAX-бота — выдать код привязки TG-аккаунта (без похода в кабинет).
			if isDialog && (text == "/link" || strings.HasPrefix(text, "/link ")) {
				code, ttl, ok := b.issueLinkCode(ctx, msgUpd.Message.Sender.UserId)
				var reply string
				if ok {
					reply = fmt.Sprintf("🔗 Привязка Telegram-аккаунта\n\nОтправьте в личку TG-бота:\n/link %s\n\nTG-бот: %s\nКод действует %d минут.", code, b.cfg.TgBotURL, ttl)
				} else {
					reply = "Не удалось выдать код привязки. Попробуйте позже или через кабинет (кнопка «Привязать Telegram»)."
				}
				b.maxClientFor(ctx, chatID).Messages.Send(ctx, maxbot.NewMessage().SetChat(chatID).SetText(reply))
				continue
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
								"4. Перешлите пост из MAX-канала сюда\n\n" +
								"Связывали канал раньше, но его нет в списке? Перешлите пост из этого MAX-канала сюда — бот обновит связку.")
						b.maxClientFor(ctx, chatID).Messages.Send(ctx, m)
					} else {
						for _, l := range links {
							kb := maxCrosspostKeyboard(b.maxApi, l.Direction, l.MaxChatID, b.repo.GetCrosspostSyncEdits(l.MaxChatID), b.repo.CrosspostPaused(l.MaxChatID))
							tgTitle := b.tgChatTitle(ctx, l.TgChatID)
							statusText := maxCrosspostStatusText(l.TgChatID, l.Direction)
							if tgTitle != "" {
								statusText = fmt.Sprintf("TG: «%s» (%d)\n", tgTitle, l.TgChatID) + statusText
							}
							m := maxbot.NewMessage().SetChat(chatID).
								SetText(statusText).
								AddKeyboard(kb)
							b.maxClientFor(ctx, chatID).Messages.Send(ctx, m)
						}
						hint := maxbot.NewMessage().SetChat(chatID).SetText(
							"Связывали канал раньше, но его нет в списке? Перешлите пост из этого MAX-канала сюда — бот обновит связку.\n\n" +
								"Новая связка: /crosspost <TG_ID>")
						b.maxClientFor(ctx, chatID).Messages.Send(ctx, hint)
					}
					continue
				}
				tgChannelID, err := strconv.ParseInt(arg, 10, 64)
				if err != nil {
					m := maxbot.NewMessage().SetChat(chatID).SetText("Неверный ID. Пример: /crosspost -1001234567890")
					b.maxClientFor(ctx, chatID).Messages.Send(ctx, m)
					continue
				}

				// Валидация: ID TG-канала ВСЕГДА отрицательный (-100…). Положительный =
				// уронили минус или «сырой» ID из ссылки t.me/c/… — связка молча не заработает.
				if tgChannelID >= 0 {
					m := maxbot.NewMessage().SetChat(chatID).SetText(
						"⚠️ Похоже, ID введён неверно.\n\nID TG-канала — ОТРИЦАТЕЛЬНЫЙ, начинается с -100, например: -1001234567890.\n\nЧто делать:\n1. Перешлите пост из TG-канала в личку TG-бота " + b.cfg.TgBotURL + "\n2. Он покажет ID — скопируйте ЦЕЛИКОМ, со знаком «-»\n3. Пришлите сюда: /crosspost <ID>")
					b.maxClientFor(ctx, chatID).Messages.Send(ctx, m)
					continue
				}
				// Сохраняем ожидание: userId → tgChannelID
				b.cpWaitMu.Lock()
				b.cpWait[msgUpd.Message.Sender.UserId] = tgChannelID
				b.cpWaitMu.Unlock()

				m := maxbot.NewMessage().SetChat(chatID).SetText(
					fmt.Sprintf("TG канал ID: %d\n\nТеперь перешлите любой пост из MAX-канала, который хотите связать.", tgChannelID))
				b.maxClientFor(ctx, chatID).Messages.Send(ctx, m)
				slog.Info("crosspost waiting for forward", "user", msgUpd.Message.Sender.UserId, "tgChannel", tgChannelID)
				continue
			}

			// DEBUG: диагностика форвардов в личке (тип связки/чат) — понять, почему mirror-донор
			// не регистрируется. Временно.
			if isDialog {
				if l := msgUpd.Message.Link; l != nil {
					slog.Debug("MAX dialog link", "linkType", string(l.Type), "linkChatId", l.ChatId, "uid", msgUpd.Message.Sender.UserId)
				} else {
					slog.Debug("MAX dialog no-link", "textLen", len(text), "uid", msgUpd.Message.Sender.UserId)
				}
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
					// Достаём TG owner ID (кто переслал пост из TG-канала в TG-бот)
					b.cpTgOwnerMu.Lock()
					tgOwnerID := b.cpTgOwner[tgChannelID]
					b.cpTgOwnerMu.Unlock()

					// Новую связку создаём, только если аддон разрешил (иначе показываем его reason).
					if ok, reason := b.crosspostAllowed(ctx, msgUpd.Message.Sender.UserId, tgOwnerID); !ok {
						m := maxbot.NewMessage().SetChat(chatID).SetText(reason)
						b.maxClientFor(ctx, chatID).Messages.Send(ctx, m)
						continue
					}

					if err := b.repo.PairCrosspost(tgChannelID, maxChannelID, msgUpd.Message.Sender.UserId, tgOwnerID); err != nil {
						slog.Error("crosspost pair failed", "err", err)
						m := maxbot.NewMessage().SetChat(chatID).SetText("Ошибка при создании связки.")
						b.maxClientFor(ctx, chatID).Messages.Send(ctx, m)
						continue
					}

					// Показать статус + клавиатуру после паринга
					kb := maxCrosspostKeyboard(b.maxApi, "both", maxChannelID, false, false)
					m := maxbot.NewMessage().SetChat(chatID).
						SetText(fmt.Sprintf("Кросспостинг настроен!\nTG: %d ↔ MAX: %d\nНаправление: ⟷ оба", tgChannelID, maxChannelID)).
						AddKeyboard(kb)
					b.maxClientFor(ctx, chatID).Messages.Send(ctx, m)
					slog.Info("crosspost paired", "tg", tgChannelID, "max", maxChannelID, "maxOwner", msgUpd.Message.Sender.UserId, "tgOwner", tgOwnerID)
					continue
				}

				// Нет cpWait — проверяем, связан ли канал → показать управление
				if maxChannelID != 0 {
					if tgID, direction, ok := b.repo.GetCrosspostTgChat(maxChannelID); ok {
						statusText := maxCrosspostStatusText(tgID, direction)
						// Клейм владельца для старых связок без owner_id (аналог /bridge_update
						// для каналов): форвард поста админом канала проставляет владельца,
						// чтобы связка появилась в /crosspost и кабинете.
						if maxOwner, _ := b.repo.GetCrosspostOwner(maxChannelID); maxOwner == 0 {
							if admins, err := b.maxClientFor(ctx, chatID).Chats.GetChatAdmins(ctx, maxChannelID); err == nil && isMaxUserAdmin(admins.Members, userId) {
								if b.repo.SetCrosspostOwner("max", maxChannelID, userId) {
									statusText += "\n\n✅ Связка обновлена — канал теперь виден в списке /crosspost и в кабинете."
									slog.Info("crosspost owner claimed", "platform", "max", "channel", maxChannelID, "user", userId)
								}
							}
						}
						kb := maxCrosspostKeyboard(b.maxApi, direction, maxChannelID, b.repo.GetCrosspostSyncEdits(maxChannelID), b.repo.CrosspostPaused(maxChannelID))
						m := maxbot.NewMessage().SetChat(chatID).
							SetText(statusText).
							AddKeyboard(kb)
						b.maxClientFor(ctx, chatID).Messages.Send(ctx, m)
						continue
					}
				}

				// Расширение получает пересланный пост до crosspost-фолбэка.
				if b.addon != nil && maxChannelID != 0 && b.addon.HandleMaxDMForward(ctx, userId, chatID, maxChannelID) {
					continue
				}

				// Канал не связан, cpWait нет — сообщить
				if maxChannelID != 0 {
					m := maxbot.NewMessage().SetChat(chatID).SetText("Этот канал ни с чем не связан.\n\nКросспостинг TG↔MAX: /crosspost <TG_ID>")
					b.maxClientFor(ctx, chatID).Messages.Send(ctx, m)
				}
				continue
			}

			// Дополнительная обработка поста расширением.
			// (чат-источник может быть заодно и bridge-связан). Собственные посты бота и
			// [TG]/[MAX]-префиксы пропускаем (анти-луп). Аддон сам решает адресатов и шлёт.
			if b.addon != nil && body.Mid != "" && !b.isSelfMaxBot(msgUpd.Message.Sender.UserId) &&
				!strings.HasPrefix(text, "[TG]") && !strings.HasPrefix(text, "[MAX]") {
				replyMid := body.ReplyTo
				if replyMid == "" && msgUpd.Message.Link != nil && msgUpd.Message.Link.Type == maxschemes.REPLY {
					replyMid = msgUpd.Message.Link.Message.Mid
				}
				go b.addonMaxPostV2(ctx, chatID, msgUpd.Message.Sender.UserId, body.Mid, msgUpd.Message.Sender.Name, text, replyMid)
				var photoURLs, videoURLs []string
				for _, att := range body.Attachments {
					switch a := att.(type) {
					case *maxschemes.PhotoAttachment:
						if a.Payload.Url != "" {
							photoURLs = append(photoURLs, a.Payload.Url)
						}
					case *maxschemes.VideoAttachment:
						if a.Payload.Url != "" {
							videoURLs = append(videoURLs, a.Payload.Url)
						}
					}
				}
				b.addon.HandleMaxPost(ctx, chatID, msgUpd.Message.Sender.UserId, body.Mid, text, photoURLs, videoURLs)
			}

			// Пересылка (bridge). Сначала проверяем thread-bridge (MAX-чат = отдельный TG-тред),
			// потом обычную пару.
			tgChats := b.repo.GetTgChats(chatID)
			threadLinked := false
			var threadTg int64
			if len(tgChats) == 0 {
				if tg, _, ok := b.repo.GetThreadTgPair(chatID); ok {
					threadTg = tg
					threadLinked = true
				}
			}
			linked := len(tgChats) > 0 || threadLinked
			if linked && !b.isSelfMaxBot(msgUpd.Message.Sender.UserId) {
				// Anti-loop
				if !strings.HasPrefix(text, "[TG]") && !strings.HasPrefix(text, "[MAX]") {
					prefix := b.hasPrefix("max", chatID)
					caption := formatMaxCaption(msgUpd, prefix, b.cfg.MessageNewline)
					if threadLinked {
						go b.forwardMaxToTg(ctx, msgUpd, threadTg, caption, false)
					} else {
						// Несколько TG-групп → одна MAX-группа: обратку (MAX→TG) шлём
						// в каждую пару, чьё направление разрешает max>tg (both/max>tg).
						// Односторонние фидеры (tg>max) реверс не получают — так funnel
						// остаётся однонаправленным. Направление выбирается пер-связка
						// командой /bridge direction.
						for _, tgChatID := range tgChats {
							if !b.pairDirectionAllows(ctx, tgChatID, chatID, "max>tg") {
								continue
							}
							go b.forwardMaxToTg(ctx, msgUpd, tgChatID, caption, false)
						}
					}
				}
				continue
			}

			// Пересылка (crosspost fallback)
			if b.isSelfMaxBot(msgUpd.Message.Sender.UserId) {
				continue
			}
			links := b.repo.GetCrosspostTgChats(chatID)
			if len(links) == 0 {
				continue
			}

			// Anti-loop
			if strings.HasPrefix(text, "[TG]") || strings.HasPrefix(text, "[MAX]") {
				continue
			}

			for _, link := range links {
				tgChatID, direction := link.TgChatID, link.Direction
				if direction == "tg>max" {
					continue // только TG→MAX, пропускаем
				}

				caption := formatMaxCrosspostCaption(msgUpd)

				// Замены MAX→TG — на исходном тексте, схлопывание пробелов — после.
				repl := b.repo.GetCrosspostReplacements(chatID)
				if len(repl.MaxToTg) > 0 {
					caption = applyReplacements(caption, repl.MaxToTg)
				}
				caption = collapseWhitespace(caption)
				// Дополнительный footer расширения для конкретной связки.
				caption += b.crosspostFooterPair(ctx, tgChatID, chatID, "tg")

				// Идемпотентность per-destination: один MAX-пост можно доставить в несколько
				// TG-чатов, но нельзя дублировать в тот же адресат при ретрае вебхука.
				claimKey := body.Mid + ":" + strconv.FormatInt(tgChatID, 10)
				if !b.repo.ClaimCrosspost("max", chatID, claimKey) {
					slog.Info("skip duplicate crosspost MAX→TG (already claimed)", "maxChannel", chatID, "mid", body.Mid, "tgChat", tgChatID)
					continue
				}
				go b.forwardMaxToTg(ctx, msgUpd, tgChatID, caption, true)
			}
		}
	}
}

func (b *Bridge) maxMessageAlreadyMapped(mid string) bool {
	if mid == "" {
		return false
	}
	_, _, _, ok := b.repo.LookupTgMsgID(mid)
	return ok
}

func (b *Bridge) suppressMaxDelete(mid string) {
	if mid == "" {
		return
	}
	b.maxDeleteSuppress.Store(mid, struct{}{})
	time.AfterFunc(10*time.Minute, func() {
		b.maxDeleteSuppress.Delete(mid)
	})
}

func (b *Bridge) isSuppressedMaxDelete(mid string) bool {
	if mid == "" {
		return false
	}
	_, ok := b.maxDeleteSuppress.Load(mid)
	return ok
}

// handleMaxCallback обрабатывает нажатия inline-кнопок (crosspost management).
// sendMaxStart шлёт приветствие в MAX-чат (на /start и на кнопку «Начать»/bot_started).
func (b *Bridge) sendMaxStart(ctx context.Context, chatID int64) {
	if ch := customHelp(); ch != "" {
		b.maxClientFor(ctx, chatID).Messages.Send(ctx, maxbot.NewMessage().SetChat(chatID).SetText(helpPlain(ch)))
		return
	}
	text := "Бот-мост между MAX и Telegram.\n\n" +
		"Команды (группы):\n" +
		"/bridge — создать ключ для связки чатов\n" +
		"/bridge <ключ> — связать этот чат с Telegram-чатом по ключу\n" +
		"/bridge prefix on/off — включить/выключить префикс [TG]/[MAX]\n" +
		"/bridge direction tg>max|max>tg|both — направление bridge\n" +
		"/unbridge — удалить связку\n" +
		"/thread_bridge <ключ> — связать этот MAX-чат с отдельным TG-тредом (форум)\n" +
		"/thread_unbridge — разорвать связку треда\n\n" +
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
		"/doctor — приватный отчёт по всем вашим подключениям\n" +
		"Управление: перешлите пост из связанного канала → кнопки\n\n" +
		"Автозамены в кросспостинге:\n" +
		"В настройках связки (кнопка 🔄) можно добавить замены текста.\n" +
		"Формат: текст | замена  или  /regex/ | замена\n" +
		"Можно заменять только в ссылках или во всём тексте.\n\n" +
		"Как связать группы:\n" +
		"1. Добавьте бота в оба чата\n" +
		"   MAX: " + b.cfg.MaxBotURL + "\n" +
		"   TG: " + b.cfg.TgBotURL + "\n" +
		"2. В MAX сделайте бота админом группы С ПРАВОМ «Доступ к сообщениям» (читать все\n" +
		"   сообщения) — без него бот не видит команды и сообщения, связка не сработает\n" +
		"3. В TG тоже сделайте бота админом — иначе он не видит все сообщения.\n" +
		"4. В одном из чатов отправьте /bridge\n" +
		"5. Бот выдаст ключ — отправьте его в другом чате (в группе, не в ЛС бота)\n" +
		"6. Готово!" + b.reserveBotHint() + "\n\n" +
		"💬 Поддержка и новости: https://t.me/+0ucbOj4wBwQzMWNi"
	b.maxClientFor(ctx, chatID).Messages.Send(ctx, maxbot.NewMessage().SetChat(chatID).SetText(text))
}

func (b *Bridge) handleMaxCallback(ctx context.Context, cbUpd *maxschemes.MessageCallbackUpdate) {
	data := cbUpd.Callback.Payload
	callbackID := cbUpd.Callback.CallbackID
	userID := cbUpd.Callback.User.UserId

	slog.Debug("MAX callback", "uid", userID, "data", data)

	// Расширение обрабатывает собственные callback-префиксы. У исходящего сообщения
	// в личке recipient.user_id может указывать на бота, поэтому адресат ответа —
	// пользователь, нажавший кнопку.
	chatID := maxCallbackChatID(cbUpd)
	if b.addon != nil && cbUpd.Message != nil &&
		b.maxAddonCallbackMessage(ctx, userID, chatID, callbackID, data, cbUpd.Message.Body.Mid) {
		return
	}
	if b.addon != nil && b.addon.HandleMaxCallback(ctx, userID, userID, callbackID, data) {
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
		if !b.isCrosspostOwner(maxChatID, userID) {
			b.maxClientFor(ctx, 0).Messages.AnswerOnCallback(ctx, callbackID, &maxschemes.CallbackAnswer{
				Notification: "Только владелец связки может изменять настройки.",
			})
			return
		}
		b.repo.SetCrosspostDirection(maxChatID, dir)

		tgID, _, _ := b.repo.GetCrosspostTgChat(maxChatID)
		body := maxCrosspostMessageBody(b.maxClientFor(ctx, 0), maxCrosspostStatusText(tgID, dir), dir, maxChatID, b.repo.GetCrosspostSyncEdits(maxChatID), b.repo.CrosspostPaused(maxChatID))
		b.maxClientFor(ctx, 0).Messages.AnswerOnCallback(ctx, callbackID, &maxschemes.CallbackAnswer{
			Message:      body,
			Notification: "Готово",
		})
		return
	}

	// cps:maxChatID — toggle sync edits
	if strings.HasPrefix(data, "cps:") {
		maxChatID, err := strconv.ParseInt(strings.TrimPrefix(data, "cps:"), 10, 64)
		if err != nil {
			return
		}
		if !b.isCrosspostOwner(maxChatID, userID) {
			b.maxClientFor(ctx, 0).Messages.AnswerOnCallback(ctx, callbackID, &maxschemes.CallbackAnswer{
				Notification: "Только владелец связки может изменять настройки.",
			})
			return
		}
		cur := b.repo.GetCrosspostSyncEdits(maxChatID)
		b.repo.SetCrosspostSyncEdits(maxChatID, !cur)
		tgID, direction, _ := b.repo.GetCrosspostTgChat(maxChatID)
		body := maxCrosspostMessageBody(b.maxClientFor(ctx, 0), maxCrosspostStatusText(tgID, direction), direction, maxChatID, !cur, b.repo.CrosspostPaused(maxChatID))
		note := "Синхронизация правок выключена"
		if !cur {
			note = "Синхронизация правок включена"
		}
		b.maxClientFor(ctx, 0).Messages.AnswerOnCallback(ctx, callbackID, &maxschemes.CallbackAnswer{
			Message:      body,
			Notification: note,
		})
		return
	}

	// cpp:maxChatID — toggle crosspost pause
	if strings.HasPrefix(data, "cpp:") {
		maxChatID, err := strconv.ParseInt(strings.TrimPrefix(data, "cpp:"), 10, 64)
		if err != nil {
			return
		}
		if !b.isCrosspostOwner(maxChatID, userID) {
			b.maxClientFor(ctx, 0).Messages.AnswerOnCallback(ctx, callbackID, &maxschemes.CallbackAnswer{
				Notification: "Только владелец связки может изменять настройки.",
			})
			return
		}
		paused := !b.repo.CrosspostPaused(maxChatID)
		if err := b.repo.SetCrosspostPaused(maxChatID, paused); err != nil {
			b.maxClientFor(ctx, 0).Messages.AnswerOnCallback(ctx, callbackID, &maxschemes.CallbackAnswer{Notification: "Не удалось изменить паузу."})
			return
		}
		tgChatID, direction, _ := b.repo.GetCrosspostTgChat(maxChatID)
		if !paused {
			b.cbSuccess(maxChatID)
			b.cbSuccess(tgChatID)
		}
		body := maxCrosspostMessageBody(b.maxClientFor(ctx, 0), maxCrosspostStatusText(tgChatID, direction), direction, maxChatID, b.repo.GetCrosspostSyncEdits(maxChatID), paused)
		note := "Кросспостинг поставлен на паузу"
		if !paused {
			note = "Кросспостинг возобновлён"
		}
		b.maxClientFor(ctx, 0).Messages.AnswerOnCallback(ctx, callbackID, &maxschemes.CallbackAnswer{Message: body, Notification: note})
		return
	}

	// cpu:maxChatID — unlink (show confirmation)
	if strings.HasPrefix(data, "cpu:") {
		maxChatID, err := strconv.ParseInt(strings.TrimPrefix(data, "cpu:"), 10, 64)
		if err != nil {
			return
		}
		if !b.isCrosspostOwner(maxChatID, userID) {
			b.maxClientFor(ctx, 0).Messages.AnswerOnCallback(ctx, callbackID, &maxschemes.CallbackAnswer{
				Notification: "Только владелец связки может удалять.",
			})
			return
		}
		kb := b.maxClientFor(ctx, 0).Messages.NewKeyboardBuilder()
		kb.AddRow().
			AddCallback("Да, удалить", maxschemes.NEGATIVE, fmt.Sprintf("cpuc:%d", maxChatID)).
			AddCallback("Отмена", maxschemes.DEFAULT, fmt.Sprintf("cpux:%d", maxChatID))
		body := &maxschemes.NewMessageBody{
			Text:        "Удалить кросспостинг?",
			Attachments: []interface{}{maxschemes.NewInlineKeyboardAttachmentRequest(kb.Build())},
		}
		b.maxClientFor(ctx, 0).Messages.AnswerOnCallback(ctx, callbackID, &maxschemes.CallbackAnswer{
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
			b.maxClientFor(ctx, 0).Messages.AnswerOnCallback(ctx, callbackID, &maxschemes.CallbackAnswer{
				Notification: "Только владелец связки может удалять.",
			})
			return
		}
		slog.Info("MAX crosspost unlink", "maxChatID", maxChatID, "by", userID)
		b.repo.UnpairCrosspost(maxChatID, userID)
		body := &maxschemes.NewMessageBody{Text: "Кросспостинг удалён."}
		b.maxClientFor(ctx, 0).Messages.AnswerOnCallback(ctx, callbackID, &maxschemes.CallbackAnswer{
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
		kb := maxReplacementsKeyboard(b.maxClientFor(ctx, 0), maxChatID)
		body := &maxschemes.NewMessageBody{
			Text:        formatReplacementsHeader(repl),
			Attachments: []interface{}{maxschemes.NewInlineKeyboardAttachmentRequest(kb.Build())},
		}
		b.maxClientFor(ctx, 0).Messages.AnswerOnCallback(ctx, callbackID, &maxschemes.CallbackAnswer{Message: body})
		// Каждая замена — отдельное сообщение с кнопками
		for i, r := range repl.TgToMax {
			dkb := maxReplItemKeyboard(b.maxClientFor(ctx, 0), "tg>max", i, id, r.Target)
			m := maxbot.NewMessage().SetChat(cbUpd.Callback.User.UserId).
				SetText(formatReplacementItem(r, "tg>max")).
				AddKeyboard(dkb)
			b.maxClientFor(ctx, 0).Messages.Send(ctx, m)
		}
		for i, r := range repl.MaxToTg {
			dkb := maxReplItemKeyboard(b.maxClientFor(ctx, 0), "max>tg", i, id, r.Target)
			m := maxbot.NewMessage().SetChat(cbUpd.Callback.User.UserId).
				SetText(formatReplacementItem(r, "max>tg")).
				AddKeyboard(dkb)
			b.maxClientFor(ctx, 0).Messages.Send(ctx, m)
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
		dkb := maxReplItemKeyboard(b.maxClientFor(ctx, 0), dir, idx, id, r.Target)
		body := &maxschemes.NewMessageBody{
			Text:        newText,
			Attachments: []interface{}{maxschemes.NewInlineKeyboardAttachmentRequest(dkb.Build())},
		}
		label := "весь текст"
		if newTarget == "links" {
			label = "только ссылки"
		}
		b.maxClientFor(ctx, 0).Messages.AnswerOnCallback(ctx, callbackID, &maxschemes.CallbackAnswer{
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
		b.maxClientFor(ctx, 0).Messages.AnswerOnCallback(ctx, callbackID, &maxschemes.CallbackAnswer{
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
		kb := b.maxClientFor(ctx, 0).Messages.NewKeyboardBuilder()
		kb.AddRow().
			AddCallback("📝 Весь текст", maxschemes.DEFAULT, "cprat:"+dir+":all:"+id).
			AddCallback("🔗 Только ссылки", maxschemes.DEFAULT, "cprat:"+dir+":links:"+id)
		body := &maxschemes.NewMessageBody{
			Text:        fmt.Sprintf("Добавление замены для %s.\nГде применять замену?", dirLabel),
			Attachments: []interface{}{maxschemes.NewInlineKeyboardAttachmentRequest(kb.Build())},
		}
		b.maxClientFor(ctx, 0).Messages.AnswerOnCallback(ctx, callbackID, &maxschemes.CallbackAnswer{Message: body})
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
			Text: "✏️ Какой текст на какой заменить?\n\n" +
				"Напишите одной строкой и разделите вертикальной чертой «|»:\n" +
				"что заменить | на что заменить\n\n" +
				"Примеры:\n" +
				"• наш Телеграм | наш канал в MAX — заменит фразу во всех постах\n" +
				"• t.me/old_channel | max.ru/new_channel — заменит ссылку\n" +
				"• #реклама |   — удалит текст (правую часть оставили пустой)\n\n" +
				"Просто отправьте такую строку сообщением.\n\n" +
				"Для продвинутых (регулярное выражение): /utm_source=\\w+/ | utm_source=max",
		}
		b.maxClientFor(ctx, 0).Messages.AnswerOnCallback(ctx, callbackID, &maxschemes.CallbackAnswer{Message: body})
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
		kb := maxReplacementsKeyboard(b.maxClientFor(ctx, 0), maxChatID)
		body := &maxschemes.NewMessageBody{
			Text:        formatReplacementsHeader(repl),
			Attachments: []interface{}{maxschemes.NewInlineKeyboardAttachmentRequest(kb.Build())},
		}
		b.maxClientFor(ctx, 0).Messages.AnswerOnCallback(ctx, callbackID, &maxschemes.CallbackAnswer{
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
		body := maxCrosspostMessageBody(b.maxClientFor(ctx, 0), maxCrosspostStatusText(tgID, direction), direction, maxChatID, b.repo.GetCrosspostSyncEdits(maxChatID), b.repo.CrosspostPaused(maxChatID))
		b.maxClientFor(ctx, 0).Messages.AnswerOnCallback(ctx, callbackID, &maxschemes.CallbackAnswer{Message: body})
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
			b.maxClientFor(ctx, 0).Messages.AnswerOnCallback(ctx, callbackID, &maxschemes.CallbackAnswer{
				Message: body,
			})
			return
		}
		body := maxCrosspostMessageBody(b.maxClientFor(ctx, 0), maxCrosspostStatusText(tgID, direction), direction, maxChatID, b.repo.GetCrosspostSyncEdits(maxChatID), b.repo.CrosspostPaused(maxChatID))
		b.maxClientFor(ctx, 0).Messages.AnswerOnCallback(ctx, callbackID, &maxschemes.CallbackAnswer{
			Message: body,
		})
		return
	}

	// Неизвестный ядру префикс отдаём расширению.
	if b.addon != nil {
		b.addon.HandleCallback(ctx, userID, userID, callbackID, data, 0)
	}
}

func maxCallbackChatID(cbUpd *maxschemes.MessageCallbackUpdate) int64 {
	if cbUpd == nil {
		return 0
	}
	if cbUpd.Message == nil || cbUpd.Message.Recipient.ChatType == maxschemes.DIALOG || cbUpd.Message.Recipient.ChatId == 0 {
		return cbUpd.Callback.User.UserId
	}
	return cbUpd.Message.Recipient.ChatId
}

// maxCrosspostMessageBody строит NewMessageBody с текстом и inline-клавиатурой.
func maxCrosspostMessageBody(api *maxbot.Api, text, direction string, maxChatID int64, syncEdits, paused bool) *maxschemes.NewMessageBody {
	kb := maxCrosspostKeyboard(api, direction, maxChatID, syncEdits, paused)
	return &maxschemes.NewMessageBody{
		Text:        text,
		Attachments: []interface{}{maxschemes.NewInlineKeyboardAttachmentRequest(kb.Build())},
	}
}

// maxCrosspostKeyboard строит inline-клавиатуру для управления кросспостингом в MAX.
func maxCrosspostKeyboard(api *maxbot.Api, direction string, maxChatID int64, syncEdits, paused bool) *maxbot.Keyboard {
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
	kb := api.Messages.NewKeyboardBuilder()
	kb.AddRow().
		AddCallback(lblTgMax, maxschemes.DEFAULT, "cpd:tg>max:"+id).
		AddCallback(lblMaxTg, maxschemes.DEFAULT, "cpd:max>tg:"+id).
		AddCallback(lblBoth, maxschemes.DEFAULT, "cpd:both:"+id)
	kb.AddRow().
		AddCallback(lblSync, maxschemes.DEFAULT, "cps:"+id).
		AddCallback("🔄 Замены", maxschemes.DEFAULT, "cpr:"+id).
		AddCallback("❌ Удалить", maxschemes.NEGATIVE, "cpu:"+id)
	kb.AddRow().AddCallback(lblPause, maxschemes.DEFAULT, "cpp:"+id)
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

func maxVideoURLFromAttachments(attachments []interface{}, token string) string {
	fallback := ""
	for _, att := range attachments {
		v, ok := att.(*maxschemes.VideoAttachment)
		if !ok {
			continue
		}
		if v.Payload.Url == "" {
			continue
		}
		if fallback == "" {
			fallback = v.Payload.Url
		}
		if token != "" && v.Payload.Token == token {
			return v.Payload.Url
		}
	}
	return fallback
}

func maxMessageAttachments(msg *maxschemes.Message) []interface{} {
	if msg == nil {
		return nil
	}
	attachments := msg.Body.Attachments
	if len(msg.Body.RawAttachments) > 0 {
		attachments = parseMaxRawAttachments(msg.Body.RawAttachments)
	}
	if msg.Link != nil && msg.Link.Type == maxschemes.FORWARD {
		linked := msg.Link.Message.Attachments
		if len(msg.Link.Message.RawAttachments) > 0 {
			linked = parseMaxRawAttachments(msg.Link.Message.RawAttachments)
		}
		attachments = append(linked, attachments...)
	}
	return attachments
}

// refreshQueuedMaxVideoURL получает новую CDN-ссылку перед повторной отправкой.
// MAX CDN URL короткоживущие: сохранять URL в очереди можно как фолбэк, но после
// Telegram 429 он часто уже отдаёт HTML/ошибку вместо видео.
func (b *Bridge) refreshQueuedMaxVideoURL(ctx context.Context, chatID int64, mid string) (string, error) {
	if mid == "" {
		return "", fmt.Errorf("empty MAX message id")
	}
	msg, err := b.maxClientFor(ctx, chatID).Messages.GetMessage(ctx, mid)
	if err != nil {
		return "", fmt.Errorf("refresh MAX video: %w", err)
	}
	if url := maxVideoURLFromAttachments(maxMessageAttachments(msg), ""); url != "" {
		return url, nil
	}
	return "", fmt.Errorf("refresh MAX video: attachment URL is not ready")
}

func maxAlbumItemsFromAttachments(attachments []interface{}) []maxAlbumItem {
	items := make([]maxAlbumItem, 0, len(attachments))
	for _, att := range attachments {
		switch a := att.(type) {
		case *maxschemes.PhotoAttachment:
			if a.Payload.Url != "" {
				items = append(items, maxAlbumItem{Type: "photo", URL: a.Payload.Url})
			}
		case *maxschemes.VideoAttachment:
			if a.Payload.Url != "" {
				items = append(items, maxAlbumItem{Type: "video", URL: a.Payload.Url})
			}
		}
	}
	return items
}

func (b *Bridge) refreshQueuedMaxAlbumItems(ctx context.Context, chatID int64, mid string) ([]maxAlbumItem, error) {
	if mid == "" {
		return nil, fmt.Errorf("empty MAX message id")
	}
	msg, err := b.maxClientFor(ctx, chatID).Messages.GetMessage(ctx, mid)
	if err != nil {
		return nil, fmt.Errorf("refresh MAX album: %w", err)
	}
	items := maxAlbumItemsFromAttachments(maxMessageAttachments(msg))
	if len(items) == 0 {
		return nil, fmt.Errorf("refresh MAX album: attachments are not ready")
	}
	return items, nil
}

func (b *Bridge) resolveMaxVideoURL(ctx context.Context, chatID int64, mid string, att *maxschemes.VideoAttachment) string {
	if att == nil {
		return ""
	}
	if att.Payload.Url != "" {
		return att.Payload.Url
	}
	token := att.Payload.Token
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ""
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
		msg, err := b.maxClientFor(ctx, chatID).Messages.GetMessage(ctx, mid)
		if err != nil {
			slog.Warn("MAX→TG video URL resolve failed", "err", err, "maxChat", chatID, "mid", mid, "attempt", attempt+1)
			continue
		}
		attachments := maxMessageAttachments(msg)
		if url := maxVideoURLFromAttachments(attachments, token); url != "" {
			if attempt > 0 {
				slog.Info("MAX→TG video URL resolved", "maxChat", chatID, "mid", mid, "attempt", attempt+1)
			}
			return url
		}
	}
	slog.Warn("MAX→TG video attachment without URL", "maxChat", chatID, "mid", mid, "has_token", token != "")
	return ""
}

// maxAlbumItem — элемент MAX-альбома для пересылки в TG (тип + URL на MAX-CDN).
// Сериализуется в JSON и кладётся в очередь (AttType="album"), чтобы альбом не терял фото.
type maxAlbumItem struct {
	Type string `json:"t"` // "photo" | "video"
	URL  string `json:"u"`
}

func splitMaxMediaCaption(caption string) (mediaCaption, overflowCaption string) {
	if utf8.RuneCountInString(caption) > 1024 {
		return "", caption
	}
	return caption, ""
}

// sendMaxAlbumToTg качает байты каждого элемента альбома и шлёт media group в TG.
// Байты (а не URL) — Telegram не фетчит MAX-CDN надёжно; уникальные attach-имена ставит
// SendMediaGroup (иначе все фото схлопывались в первое). caption/parseMode — на [0].
// Единый путь и для прямой пересылки, и для доставки из очереди.
func (b *Bridge) sendMaxAlbumToTg(ctx context.Context, tgChatID int64, items []maxAlbumItem, caption, parseMode string, threadID, replyToID int) ([]int, error) {
	// Лимит подписи media-group в Telegram — 1024 символа. Длинную подпись (частый кейс —
	// пост-инструкция с альбомом) НЕ вешаем на альбом (иначе «caption is too long» → фото
	// уходят без текста), а шлём отдельным сообщением следом. Порог по рунам с запасом: для
	// HTML-подписи теги завышают счёт — тогда просто отправим текст отдельно, это безопасно.
	albumCaption, overflowCaption := splitMaxMediaCaption(caption)

	media := make([]TGInputMedia, 0, len(items))
	for i, it := range items {
		typ := it.Type
		if typ == "" {
			typ = "photo"
		}
		im := TGInputMedia{Type: typ, File: FileArg{URL: it.URL}}
		if data, name, dlErr := b.downloadURLWithLimit(it.URL, b.cfg.maxMaxFileBytes()); dlErr == nil {
			im.File = FileArg{Name: name, Bytes: data}
		} else {
			slog.Warn("MAX→TG album item download failed, keep URL", "err", dlErr, "idx", i)
		}
		if i == 0 && albumCaption != "" {
			im.Caption = albumCaption
			im.ParseMode = parseMode
		}
		media = append(media, im)
	}
	msgs, err := b.tg.SendMediaGroup(ctx, tgChatID, media, &SendOpts{ThreadID: threadID, ReplyToID: replyToID})
	if err != nil {
		return msgs, err
	}
	// Длинная подпись — отдельным сообщением (после альбома, в том же треде).
	if overflowCaption != "" {
		if overflowID, serr := b.tg.SendMessage(ctx, tgChatID, overflowCaption, &SendOpts{ParseMode: parseMode, ThreadID: threadID}); serr != nil {
			slog.Warn("MAX→TG album long caption send failed", "tgChat", tgChatID, "err", serr)
		} else {
			msgs = append(msgs, overflowID)
		}
	}
	return msgs, nil
}

// forwardMaxToTg пересылает MAX-сообщение (текст/медиа) в TG-чат.
// Если isCrosspost=true, caption используется как финальный текст (с заменами, без атрибуции).
func (b *Bridge) forwardMaxToTg(ctx context.Context, msgUpd *maxschemes.MessageCreatedUpdate, tgChatID int64, caption string, isCrosspost bool) {
	if b.cbBlocked(tgChatID) {
		return
	}

	body := msgUpd.Message.Body
	chatID := msgUpd.Message.Recipient.ChatId
	text := strings.TrimSpace(body.Text)
	if parsed := parseMaxRawAttachments(body.RawAttachments); len(parsed) > 0 {
		body.Attachments = parsed
	}

	// Пауза связки — временно не пересылаем (связку не удаляем). Возобновить: /unpause.
	if isCrosspost {
		if b.repo.CrosspostPaused(chatID) {
			return
		}
	} else if b.repo.PairPaused(tgChatID, chatID) {
		return
	}

	// Дедуп: это MAX-сообщение уже переслано в ЭТОТ TG-чат (повторная доставка вебхука
	// после рестарта / двойная доставка). Пер-чат, а не глобально по mid: при фан-ауте
	// одного MAX-сообщения в несколько TG-групп доставка в другие чаты не глушится.
	if body.Mid != "" {
		if b.repo.MaxMsgDeliveredTo(body.Mid, tgChatID) {
			slog.Info("MAX→TG skip: already delivered (dedup)", "maxMid", body.Mid, "tgChat", tgChatID)
			return
		}
	}

	// Forward (репост) из MAX: оригинал лежит в Link.Message, а body.Text часто пустой
	// (если юзер не дописал свой комментарий). Подмешиваем содержимое Link.Message.Text,
	// иначе в TG прилетит пустое "Name:" сообщение.
	// Вложения оригинала тоже лежат в Link.Message.Attachments — body.Attachments при
	// пересылке пустой, поэтому их нужно взять явно.
	markups := body.Markups
	if lk := msgUpd.Message.Link; lk != nil && lk.Type == maxschemes.FORWARD {
		fwd := strings.TrimSpace(lk.Message.Text)
		if fwd != "" {
			if text != "" {
				// У юзера есть комментарий — склеиваем коммент + перенос + переcланный текст.
				// markups для склеенного текста невалидны, поэтому отбрасываем форматирование.
				text = text + "\n\n" + fwd
				markups = nil
			} else {
				// Только репост — берём текст и markups из Link.Message как есть.
				text = fwd
				markups = lk.Message.Markups
			}
		}
		// Вложения оригинала добавляем перед вложениями обёртки (обычно она пустая).
		// SDK MAX парсит Link.Message.RawAttachments → Attachments только если
		// body.RawAttachments == nil. У forwarded-сообщений MAX шлёт body.RawAttachments=[]
		// (пустой массив, не nil) → парсинг пропускается, Link.Message.Attachments пуст.
		// Поэтому парсим сырые JSON-байты сами.
		if len(lk.Message.RawAttachments) > 0 {
			parsed := parseMaxRawAttachments(lk.Message.RawAttachments)
			if len(parsed) > 0 {
				body.Attachments = append(parsed, body.Attachments...)
			}
		} else if len(lk.Message.Attachments) > 0 {
			body.Attachments = append(lk.Message.Attachments, body.Attachments...)
		}
	}
	text = appendMaxShareURLs(text, body.Attachments)

	// Relay-гейт ТОЛЬКО для бридж-ГРУПП (бот постит от своего имени → страйк боту).
	// Каналы (даже через /bridge) и кросспосты не трогаем — бот там не отправитель.
	if !isCrosspost && isMaxGroup(msgUpd.Message.Recipient.ChatType) {
		// Скриним только тело (text), без caption — он содержит имя автора (вычурные ники
		// травили mixed-script/emoji-junk и блокировали легит).
		if block, reason := b.screenRelay(ctx, "max", chatID, msgUpd.Message.Sender.UserId, text); block {
			slog.Warn("relay blocked MAX→TG (group): prohibited/spam",
				"maxChat", chatID, "tgChat", tgChatID, "reason", reason)
			// Расширение может задержать сообщение для ручной проверки.
			// Жёсткие категории holdForModeration дропнет без карточки.
			payload, _ := json.Marshal(msgUpd)
			b.holdForModeration(ctx, "max", chatID, msgUpd.Message.Body.Mid, tgChatID,
				msgUpd.Message.Sender.UserId, msgUpd.Message.Sender.Name, text, caption, reason, string(payload))
			return
		}
	}

	// Определяем тред-назначение:
	// — thread-pair: MAX-чат жёстко привязан к одному TG-треду, все сообщения идут туда,
	//   reply-override не меняет тред.
	// — обычная пара: дефолтный тред пары + для reply переопределяем на тред исходника
	//   (чтобы ответ попадал в ту же ветку, где лежит исходное сообщение).
	var threadID int
	_, threadPairThread, isThreadPair := b.repo.GetThreadTgPair(chatID)
	if isThreadPair {
		threadID = threadPairThread
	} else {
		threadID = b.repo.GetTgThreadID(tgChatID)
	}

	var replyToID int
	if body.ReplyTo != "" {
		if _, rid, tid, ok := b.repo.LookupTgMsgID(body.ReplyTo); ok {
			replyToID = rid
			if !isThreadPair {
				threadID = tid
			}
		}
	} else if msgUpd.Message.Link != nil && msgUpd.Message.Link.Type == maxschemes.REPLY {
		mid := msgUpd.Message.Link.Message.Mid
		if mid != "" {
			if _, rid, tid, ok := b.repo.LookupTgMsgID(mid); ok {
				replyToID = rid
				if !isThreadPair {
					threadID = tid
				}
			}
		}
	}

	// Проверяем вложения
	var sentMsgID int
	var sentMsgIDs []int
	var sendErr error
	mediaSent := false
	var qAttType, qAttURL string // для очереди при ошибке

	// Определяем HTML caption: всегда для bridge-режима (жирное имя) и при наличии markups
	htmlCaption := caption
	hasMarkups := len(markups) > 0
	hasAttribution := !isCrosspost // bridge-режим (не кросспостинг)
	useHTML := hasMarkups || hasAttribution
	if useHTML {
		var htmlText string
		if hasMarkups {
			htmlText = maxMarkupsToHTML(text, markups)
		} else {
			htmlText = html.EscapeString(text)
		}
		if !hasAttribution {
			// Кросспостинг: caption = сырой текст, без атрибуции
			htmlCaption = htmlText
		} else {
			// Bridge: caption с атрибуцией — жирное имя
			name := maxName(msgUpd)
			if b.hasPrefix("max", msgUpd.Message.Recipient.ChatId) {
				name = "[MAX] " + name
			}
			escapedName := html.EscapeString(name)
			if b.cfg.MessageNewline {
				htmlCaption = "<b>" + escapedName + "</b>:\n" + htmlText
			} else {
				htmlCaption = "<b>" + escapedName + "</b>: " + htmlText
			}
		}
	}

	// Собираем вложения: фото/видео → albumMedia (отправляем вместе), остальные → soloMedia
	var albumMedia []TGInputMedia
	var albumItems []maxAlbumItem // тот же альбом как список {type,url} — для очереди (JSON)
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
				p := TGInputMedia{Type: "photo", File: FileArg{URL: a.Payload.Url}}
				albumMedia = append(albumMedia, p)
				albumItems = append(albumItems, maxAlbumItem{Type: "photo", URL: a.Payload.Url})
			}
		case *maxschemes.VideoAttachment:
			if url := b.resolveMaxVideoURL(ctx, chatID, body.Mid, a); url != "" {
				if len(albumMedia) == 0 {
					qAttType, qAttURL = "video", url
				}
				v := TGInputMedia{Type: "video", File: FileArg{URL: url}}
				albumMedia = append(albumMedia, v)
				albumItems = append(albumItems, maxAlbumItem{Type: "video", URL: url})
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
		case *maxShareAttachment:
			if a.ImageURL != "" {
				if len(albumMedia) == 0 {
					qAttType, qAttURL = "photo", a.ImageURL
				}
				p := TGInputMedia{Type: "photo", File: FileArg{URL: a.ImageURL}}
				albumMedia = append(albumMedia, p)
				albumItems = append(albumItems, maxAlbumItem{Type: "photo", URL: a.ImageURL})
			}
		default:
			// Неизвестный/необработанный тип вложения (диагностика кружков и пр.).
			slog.Warn("MAX→TG unhandled attachment type", "maxChat", chatID, "tgChat", tgChatID, "go_type", fmt.Sprintf("%T", att))
		}
	}

	// Если для этого чата уже есть сообщения в очереди — не отправляем напрямую,
	// чтобы не нарушить порядок доставки. Сразу ставим в очередь.
	if b.hasPendingForChat("max2tg", tgChatID) {
		slog.Info("MAX→TG queued (pending exists)", "uid", msgUpd.Message.Sender.UserId, "maxChat", chatID, "tgChat", tgChatID)
		if len(albumItems) > 1 {
			b.enqueueMax2TgAlbum(chatID, tgChatID, body.Mid, htmlCaption, albumItems, pm)
		} else {
			b.enqueueMax2Tg(chatID, tgChatID, body.Mid, htmlCaption, qAttType, qAttURL, pm)
		}
		return
	}

	// Отправляем фото/видео как альбом (если их несколько — grouped, иначе — single)
	if len(albumMedia) > 0 {
		mediaSent = true
		// Caption и reply только к первому элементу
		if htmlCaption != "" || replyToID != 0 {
			albumMedia[0].Caption = htmlCaption
			if pm != "" {
				albumMedia[0].ParseMode = pm
			}
		}

		if len(albumMedia) == 1 {
			// Одно вложение — отправляем обычным сообщением (альбом из 1 элемента не имеет reply)
			mediaCaption, overflowCaption := splitMaxMediaCaption(htmlCaption)
			sentMsgID, sendErr = b.sendTgMediaFromURL(ctx, tgChatID, qAttURL, qAttType, mediaCaption, pm, replyToID, threadID, b.cfg.maxMaxFileBytes())
			if sendErr == nil && sentMsgID != 0 {
				sentMsgIDs = append(sentMsgIDs, sentMsgID)
			}
			if sendErr == nil && overflowCaption != "" {
				if overflowID, err := b.tg.SendMessage(ctx, tgChatID, overflowCaption, &SendOpts{ParseMode: pm, ThreadID: threadID}); err != nil {
					slog.Warn("MAX→TG media long caption send failed", "tgChat", tgChatID, "err", err)
				} else {
					sentMsgIDs = append(sentMsgIDs, overflowID)
				}
			}
			var e *ErrFileTooLarge
			if errors.As(sendErr, &e) {
				slog.Warn("MAX→TG media too big", "name", e.Name, "size", e.Size)
				m := maxbot.NewMessage().SetChat(chatID).SetText(
					fmt.Sprintf("⚠️ Файл \"%s\" слишком большой для пересылки (%s). Максимальный размер файла %d МБ.",
						e.Name, formatFileSize(int(e.Size)), b.cfg.MaxMaxFileSizeMB))
				b.maxApi.Messages.Send(ctx, m)
			}
		} else {
			// Несколько — media group. Единый helper (качает байты + уникальные attach-имена).
			// На фейле НЕ спамим чат — sendErr → outer уведёт весь альбом в очередь (album-aware).
			msgIDs, err := b.sendMaxAlbumToTg(ctx, tgChatID, albumItems, htmlCaption, pm, threadID, replyToID)
			if err != nil {
				slog.Error("MAX→TG album send failed", "err", err)
				sendErr = err
			} else if len(msgIDs) > 0 {
				sentMsgID = msgIDs[0]
				sentMsgIDs = append(sentMsgIDs, msgIDs...)
			}
		}
	}

	// Отправляем остальные вложения (аудио, файлы, стикеры) по одному
	// Если фото/видео не отправлялось, caption добавляем к первому вложению
	firstSolo := true
	for _, sm := range soloMedia {
		smCaption := ""
		overflowCaption := ""
		smReplyTo := 0
		if firstSolo && !mediaSent {
			smCaption, overflowCaption = splitMaxMediaCaption(htmlCaption)
			smReplyTo = replyToID
		}
		firstSolo = false
		s, err := b.sendTgMediaFromURL(ctx, tgChatID, sm.url, sm.attType, smCaption, pm, smReplyTo, threadID, b.cfg.maxMaxFileBytes(), sm.name)
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
					fmt.Sprintf("Не удалось отправить файл \"%s\".", sm.name))
				b.maxApi.Messages.Send(ctx, m)
			}
			if sendErr == nil {
				sendErr = err
			}
		} else {
			sentMsgIDs = append(sentMsgIDs, s)
			if !mediaSent {
				sentMsgID = s
				mediaSent = true
			}
			if overflowCaption != "" {
				if overflowID, err := b.tg.SendMessage(ctx, tgChatID, overflowCaption, &SendOpts{ParseMode: pm, ThreadID: threadID}); err != nil {
					slog.Warn("MAX→TG file long caption send failed", "tgChat", tgChatID, "err", err)
				} else {
					sentMsgIDs = append(sentMsgIDs, overflowID)
				}
			}
		}
	}

	// Текст без медиа
	if !mediaSent {
		if text == "" {
			return
		}
		if useHTML {
			sentMsgID, sendErr = b.tg.SendMessage(ctx, tgChatID, htmlCaption, &SendOpts{ParseMode: "HTML", ReplyToID: replyToID, ThreadID: threadID})
		} else {
			sentMsgID, sendErr = b.tg.SendMessage(ctx, tgChatID, caption, &SendOpts{ReplyToID: replyToID, ThreadID: threadID})
		}
		if sendErr == nil && sentMsgID != 0 {
			sentMsgIDs = append(sentMsgIDs, sentMsgID)
		}
	}

	if sendErr != nil {
		errStr := sendErr.Error()
		slog.Error("MAX→TG send failed", "err", errStr, "uid", msgUpd.Message.Sender.UserId, "maxChat", chatID, "tgChat", tgChatID)

		// Группа преобразована в supergroup — автоматически мигрируем chat ID
		var tgErr *TGError
		if errors.As(sendErr, &tgErr) && tgErr.MigrateToChatID != 0 {
			newChatID := tgErr.MigrateToChatID
			slog.Info("TG chat migrated, updating pair", "old", tgChatID, "new", newChatID)
			if err := b.repo.MigrateTgChat(tgChatID, newChatID); err != nil {
				slog.Error("MigrateTgChat failed", "err", err)
			} else {
				// Повторяем отправку с новым ID
				go b.forwardMaxToTg(ctx, msgUpd, newChatID, caption, isCrosspost)
			}
			return
		}
		if strings.Contains(errStr, "upgraded to a supergroup") {
			// Fallback если не удалось получить новый ID из ошибки
			m := maxbot.NewMessage().SetChat(chatID).SetText(
				"TG-группа была преобразована в супергруппу. Перепривяжите чат: /unbridge в MAX, затем /bridge заново в обоих чатах.")
			b.maxApi.Messages.Send(ctx, m)
			return
		}

		// TOPIC_CLOSED — General топик закрыт, уведомляем и не ретраим
		if strings.Contains(errStr, "TOPIC_CLOSED") {
			m := maxbot.NewMessage().SetChat(chatID).SetText(
				"Не удалось переслать сообщение: основной топик (General) закрыт.\nОткройте General в настройках группы или сделайте бота админом.")
			b.maxApi.Messages.Send(ctx, m)
			return
		}

		// Топики были выключены — сбрасываем thread_id и повторяем
		if threadID != 0 && (strings.Contains(errStr, "message thread not found") ||
			strings.Contains(errStr, "TOPIC_NOT_FOUND") ||
			strings.Contains(errStr, "topics are disabled")) {
			slog.Info("TG forum topics disabled, resetting thread_id", "tgChat", tgChatID, "oldThread", threadID)
			b.repo.SetTgThreadID(tgChatID, 0)
			go b.forwardMaxToTg(ctx, msgUpd, tgChatID, caption, isCrosspost)
			return
		}

		parseMode := ""
		if useHTML {
			parseMode = "HTML"
		}
		if isChatUnavailable(errStr) {
			// Бот удалён/заблокирован/лишён прав в TG-чате → связку на паузу + DM
			// владельцу. Не ретраим (очередь всё равно дропнет перманент).
			b.cbPermanentFail(ctx, tgChatID)
		} else {
			// Транзиент (429 rate-limit / таймаут / API): НЕ пишем в MAX-чат «не удалось /
			// в очереди» на каждый пост — при массовом постинге это флудило чат десятками
			// сообщений. Молча в очередь: доставится автоматически (+ ретрай 429 в tgsender).
			// Альбом кладём ЦЕЛИКОМ (album-aware), иначе доставилось бы 1 фото.
			if len(albumItems) > 1 {
				b.enqueueMax2TgAlbum(chatID, tgChatID, body.Mid, htmlCaption, albumItems, parseMode)
			} else {
				b.enqueueMax2Tg(chatID, tgChatID, body.Mid, htmlCaption, qAttType, qAttURL, parseMode)
			}
			b.cbFail(tgChatID)
		}
	} else {
		b.cbSuccess(tgChatID)
		slog.Info("MAX→TG sent", "msgID", sentMsgID, "media", mediaSent, "uid", msgUpd.Message.Sender.UserId, "maxChat", chatID, "tgChat", tgChatID)
		if len(sentMsgIDs) == 0 && sentMsgID != 0 {
			sentMsgIDs = append(sentMsgIDs, sentMsgID)
		}
		for _, id := range sentMsgIDs {
			b.repo.SaveMsgOrigin(tgChatID, id, chatID, body.Mid, threadID, "max")
		}
	}
}

func shouldSyncMaxDelete(origin string) bool {
	return origin == "max"
}

func appendMaxShareURLs(text string, attachments []interface{}) string {
	for _, att := range attachments {
		var url string
		switch share := att.(type) {
		case *maxschemes.ShareAttachment:
			url = share.Payload.Url
		case *maxShareAttachment:
			url = share.Payload.Url
		default:
			continue
		}
		url = strings.TrimSpace(url)
		if url == "" || strings.Contains(text, url) {
			continue
		}
		if text != "" {
			text += "\n\n"
		}
		text += url
	}
	return text
}
