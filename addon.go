package main

import (
	"context"
)

// Addon — опциональная точка расширения бриджа дополнительными командами.
// В публичной сборке отсутствует (loadAddon возвращает nil); реализация
// подключается отдельной сборкой через build-tag `addon`.
type Addon interface {
	Start(ctx context.Context) error
	HandleDMCommand(ctx context.Context, userID, chatID int64, text string) (handled bool)
	// HandleDMForward вызывается, когда юзер пересылает в личку пост из канала.
	// sourceMsgID — msg_id оригинала в канале (0 если неизвестен).
	HandleDMForward(ctx context.Context, userID, dmChatID, sourceChatID int64, sourceTitle string, sourceMsgID int) (handled bool)
	// HandleCallback вызывается на нажатие inline-кнопки. Аддон проверяет, его ли
	// это callback (по своему префиксу), и возвращает true если обработал. msgID —
	// id сообщения с нажатой кнопкой (чтобы аддон мог его удалить).
	HandleCallback(ctx context.Context, userID, chatID int64, callbackID, data string, msgID int) (handled bool)
	// CrosspostButton вызывается ПЕРЕД отправкой кросспоста в MAX. Аддон решает,
	// нужна ли inline-кнопка мини-аппа под этим постом, и
	// возвращает её спецификацию. ok=false ⇒ кнопки нет. Generic: ядро не знает
	// семантику, просто навешивает OPEN_APP-кнопку на сообщение, если аддон попросил.
	CrosspostButton(ctx context.Context, ownerID, tgChatID int64, tgMsgID int, maxChatID int64) (appName, text, payload string, ok bool)
	// Moderate вызывается на входящее групповое сообщение (TG или MAX). Generic-хук:
	// бридж не знает семантику (для него это «проверь сообщение»), аддон сам решает
	// и исполняет действие через Deps. Параметры — примитивы (а не struct), чтобы
	// сигнатура совпадала между main.Addon и приватным пакетом аддона (Go требует
	// идентичности типов в методах интерфейса). Возвращает true, если сообщение
	// обработано как нежелательное (удалено) — ядру для остановки дальнейшей пересылки.
	// viaBot — сообщение через инлайн-бота/переслано от бота ("@username"/id, "" если нет):
	// типовой способ вкидывать скам-карточки (фейковые опросы) — аддон учитывает как сигнал.
	Moderate(ctx context.Context, platform string, chatID, userID int64, userName string, tgMsgID int, maxMid, text, viaBot string, hasLink, isAdmin, extReply bool) (handled bool)
	// WantImageCheck — нужно ли ядру скачивать картинку для внешней проверки.
	WantImageCheck(platform string, chatID int64) bool
	// DeleteServiceMsgs — удалять ли служебные сообщения (вошёл/вышел/смена названия/
	// закреп) в этом чате (тоггл владельца). Ядро удаляет, аддон лишь хранит флаг.
	DeleteServiceMsgs(platform string, chatID int64) bool
	// ModerateImage вызывается на входящее групповое ФОТО (TG/MAX): ядро скачивает
	// байты картинки и отдаёт аддону. Аддон сам решает (декод QR, мультимодальный
	// LLM по конфигу и доверию) и исполняет действие через Deps. true ⇒ удалено.
	// Generic: ядро не знает семантику, просто «проверь картинку».
	ModerateImage(ctx context.Context, platform string, chatID, userID int64, tgMsgID int, maxMid string, img []byte, mime string, isAdmin bool) (handled bool)
	// CrosspostAllowed вызывается ПЕРЕД созданием новой связки кросспоста. ok=false ⇒
	// ядро не создаёт связку и показывает reason. Generic: ядро не знает семантику решения.
	CrosspostAllowed(ctx context.Context, ownerMaxID, ownerTgID int64) (ok bool, reason string)
	// PairAllowed вызывается ПЕРЕД созданием новой bridge-связки групп (и перед выдачей
	// ключа). ok=false ⇒ ядро не создаёт связку и показывает reason. Generic: ядро не
	// знает семантику решения.
	PairAllowed(ctx context.Context, ownerMaxID, ownerTgID int64) (ok bool, reason string)
	// CrosspostDeliverable вызывается ПЕРЕД доставкой поста кросспоста: deliver=false ⇒
	// ядро молча пропускает доставку. Generic: ядро не знает семантику решения.
	CrosspostDeliverable(ctx context.Context, ownerMaxID, ownerTgID, maxChatID int64) (deliver bool)
	// CrosspostFooter возвращает дополнительный footer или пустую строку.
	CrosspostFooter(ctx context.Context, ownerMaxID, ownerTgID, maxChatID int64, dst, botURL string) string
	// PairDirection возвращает направление обычного bridge: both | tg>max | max>tg.
	// Пустое/неизвестное значение ядро трактует как both.
	PairDirection(ctx context.Context, tgChatID, maxChatID int64) string
	// SetPairDirection меняет направление обычного bridge.
	SetPairDirection(ctx context.Context, userID, tgChatID, maxChatID int64, direction string) (ok bool, reason string)
	// HandleMaxCommand — команда из MAX-диалога. true ⇒ обработано аддоном.
	HandleMaxCommand(ctx context.Context, userID, chatID int64, text string) (handled bool)
	// HandleChatShared — пользователь выбрал чат нативной кнопкой (chat_shared в личке).
	// requestID эхо запроса, sharedChatID — выбранный чат. Generic: ядро не знает семантику.
	HandleChatShared(ctx context.Context, userID int64, requestID int, sharedChatID int64, title string) (handled bool)
	// HandleJoin вызывается на вступление участника в группу (капча / анти-рейд).
	HandleJoin(ctx context.Context, platform string, chatID, userID int64, name, username string, isBot bool)
	// HandleLeave — участник вышел/удалён: чистим его pending-капчу и удаляем сообщение капчи.
	HandleLeave(ctx context.Context, platform string, chatID, userID int64)
	// ScreenContent — синхронный relay-гейт: можно ли пересылать этот текст ОТ ЛИЦА
	// бота (в обе стороны TG↔MAX). block=true ⇒ ядро НЕ пересылает сообщение
	// (запрещённый/токсичный контент — наркосбыт, скам-вакансии, гемблинг, игровой/
	// финансовый скам, оружие, CSAM — ИЛИ обфусцированный «точно спам»). Это НЕ
	// модерация групп: что юзеры пишут в своих группах — их дело; гейт лишь защищает
	// бота от рассылки запрещёнки/спама от его имени (иначе платформа банит бота).
	// Generic: ядро не знает семантику. reason — для лога/уведомления. platform/chatID/
	// userID — метаданные для лога сообщений (накопление корпуса под тест/обучение).
	ScreenContent(ctx context.Context, platform string, chatID, userID int64, text string) (block bool, reason string)
	// HoldForModeration — на блок relay-гейта кладёт сообщение в очередь модерации и
	// постит карточку с кнопками в чат. true ⇒ задержано (ядро не пересылает). payload —
	// JSON исходного сообщения для ре-релея на «Пропустить».
	HoldForModeration(ctx context.Context, platform string, srcChat int64, srcMsg string, target, authorID int64, authorName, text, caption, reason, payload string) bool
	// HandleMaxCallback — нажатие callback-кнопки в MAX (меню аддона в личке). Аддон проверяет
	// свой префикс payload и возвращает true если обработал. chatID — где кнопка (в личке = userID).
	HandleMaxCallback(ctx context.Context, userID, chatID int64, callbackID, data string) (handled bool)
	// HandleMaxGroupCommand — команда «/...» в MAX-ГРУППЕ (не в личке). Generic: ядро не знает
	// семантику, отдаёт аддону; true ⇒ обработано.
	HandleMaxGroupCommand(ctx context.Context, userID, chatID int64, chatType, text string) (handled bool)
	// HandleMaxPost передаёт расширению входящий пост MAX-чата.
	HandleMaxPost(ctx context.Context, srcChatID, senderUserID int64, mid, text string, photoURLs, videoURLs []string)
	// HandleMaxMessageRemoved сообщает расширению об удалении сообщения в MAX.
	// Generic-хук позволяет расширению инвалидировать собственные ссылки на сообщение.
	HandleMaxMessageRemoved(ctx context.Context, maxChatID int64, mid string)
	// HandleTgGroupCommand — команда «/...» в TG-группе или TG-канале. Generic: ядро не знает
	// семантику, отдаёт аддону; true ⇒ обработано.
	// В канале userID=0 (у channel post нет отправителя).
	HandleTgGroupCommand(ctx context.Context, userID, chatID int64, chatType, text string) (handled bool)
	// HandleTgChannelPost передаёт расширению входящий пост TG-канала.
	HandleTgChannelPost(ctx context.Context, srcChatID int64, msgID int, mediaGroupID string)
	// HandleModCommand — быстрая модер-команда админа в группе: reply + /ban|/mute|/unban|/unmute
	// (или с аргументом @user/id). Ядро уже проверило админство и разрезолвило цель (reply/@user).
	// targetID=0 ⇒ цель не определена (аддон подсказывает формат). Generic: семантику знает аддон.
	HandleModCommand(ctx context.Context, platform string, chatID, adminID, targetID int64, targetMsgID, cmdMsgID int, cmd, arg string) (handled bool)
	// HandleDMText — свободный текст в личке бота (не команда). AI-саппорт: диагностика связок/
	// прав и помощь. true ⇒ обработано (ядро не идёт дальше). Generic: семантику знает аддон.
	HandleDMText(ctx context.Context, userID, chatID int64, text string) (handled bool)
	// HandleMaxDMForward — пересланный в личку бота пост из MAX-чата (fwdChatID — id источника).
	// Аддон решает семантику (напр. регистрация MAX-канала как донора зеркала). true ⇒ обработано
	// (ядро не идёт дальше по своим веткам форварда). Ответ шлёт сам аддон через Deps.SendMaxPost.
	HandleMaxDMForward(ctx context.Context, userID, dmChatID, fwdChatID int64) (handled bool)
}

// GroupMessage — удобная обёртка для вызова Moderate из ядра (call-site читаемость).
type GroupMessage struct {
	Platform string // "tg" | "max"
	ChatID   int64
	UserID   int64
	UserName string
	TgMsgID  int
	MaxMid   string
	Text     string
	ViaBot   string // через инлайн-бота / переслано от бота ("@username"/id), "" если нет
	HasLink  bool
	IsAdmin  bool
	ExtReply bool // сообщение цитирует пост из другого чата/канала (external_reply)
}

// isTgServiceSender — служебные/каналовые отправители Telegram: это КОНТЕНТ КАНАЛА или
// системного бота, а не обычные пользовательские сообщения.
// 777000=Telegram, 1087968824=GroupAnonymousBot, 136817688=Channel_Bot.
func isTgServiceSender(userID int64) bool {
	return userID == 777000 || userID == 1087968824 || userID == 136817688
}

// moderateGroupMessage прогоняет сообщение через аддон-модерацию (если подключён).
func (b *Bridge) moderateGroupMessage(ctx context.Context, m GroupMessage) bool {
	if b.addon == nil {
		return false
	}
	// Служебные/каналовые отправители (пост канала, анонимный админ, авто-форвард) — не спам.
	if m.Platform == "tg" && isTgServiceSender(m.UserID) {
		return false
	}
	return b.addon.Moderate(ctx, m.Platform, m.ChatID, m.UserID, m.UserName, m.TgMsgID, m.MaxMid, m.Text, m.ViaBot, m.HasLink, m.IsAdmin, m.ExtReply)
}

// screenRelay — relay-гейт: блокирует пересылку запрещённого/обфусцированного контента
// ОТ ЛИЦА бота (в обе стороны TG↔MAX). Без аддона (публичная сборка) — всегда пропускаем.
func (b *Bridge) screenRelay(ctx context.Context, platform string, chatID, userID int64, text string) (block bool, reason string) {
	if b.addon == nil {
		return false, ""
	}
	// Служебные/каналовые отправители Telegram — контент канала, не режем.
	if platform == "tg" && isTgServiceSender(userID) {
		return false, ""
	}
	return b.addon.ScreenContent(ctx, platform, chatID, userID, text)
}

// holdForModeration — на блок relay-гейта: отдать сообщение аддону в очередь модерации
// (он запостит карточку с кнопками). Без аддона — false (ядро дропает как обычно).
func (b *Bridge) holdForModeration(ctx context.Context, platform string, srcChat int64, srcMsg string, target, authorID int64, authorName, text, caption, reason, payload string) bool {
	if b.addon == nil {
		return false
	}
	return b.addon.HoldForModeration(ctx, platform, srcChat, srcMsg, target, authorID, authorName, text, caption, reason, payload)
}

// wantImageCheck — нужна ли расширению картинка сообщения.
func (b *Bridge) wantImageCheck(platform string, chatID int64) bool {
	return b.addon != nil && b.addon.WantImageCheck(platform, chatID)
}

// delServiceMsgs — удалять ли служебные сообщения в чате (решает аддон по конфигу).
func (b *Bridge) delServiceMsgs(platform string, chatID int64) bool {
	return b.addon != nil && b.addon.DeleteServiceMsgs(platform, chatID)
}

// moderateImage прогоняет картинку через аддон-модерацию (если подключён).
func (b *Bridge) moderateImage(ctx context.Context, platform string, chatID, userID int64, tgMsgID int, maxMid string, img []byte, mime string, isAdmin bool) bool {
	if b.addon == nil || len(img) == 0 {
		return false
	}
	return b.addon.ModerateImage(ctx, platform, chatID, userID, tgMsgID, maxMid, img, mime, isAdmin)
}

// crosspostAllowed — можно ли создать новую связку (решает аддон). Без аддона — да.
func (b *Bridge) crosspostAllowed(ctx context.Context, ownerMaxID, ownerTgID int64) (bool, string) {
	if b.addon == nil {
		return true, ""
	}
	return b.addon.CrosspostAllowed(ctx, ownerMaxID, ownerTgID)
}

// pairAllowed — можно ли создать новую bridge-связку групп (решает аддон). Без аддона — да.
func (b *Bridge) pairAllowed(ctx context.Context, ownerMaxID, ownerTgID int64) (bool, string) {
	if b.addon == nil {
		return true, ""
	}
	return b.addon.PairAllowed(ctx, ownerMaxID, ownerTgID)
}

// maxAddonCommand — отдать команду MAX-диалога аддону (если подключён). true ⇒ обработано.
func (b *Bridge) maxAddonCommand(ctx context.Context, userID, chatID int64, text string) bool {
	return b.addon != nil && b.addon.HandleMaxCommand(ctx, userID, chatID, text)
}

// chatShared — отдать аддону выбранный нативной кнопкой чат (если подключён). true ⇒ обработано.
func (b *Bridge) chatShared(ctx context.Context, userID int64, requestID int, sharedChatID int64, title string) bool {
	return b.addon != nil && b.addon.HandleChatShared(ctx, userID, requestID, sharedChatID, title)
}

// memberJoined — уведомить аддон о вступлении участника (капча/анти-рейд).
func (b *Bridge) memberJoined(ctx context.Context, platform string, chatID, userID int64, name, username string, isBot bool) {
	if b.addon != nil {
		b.addon.HandleJoin(ctx, platform, chatID, userID, name, username, isBot)
	}
}

// memberLeft — уведомить аддон о выходе/удалении участника (сразу убрать капчу).
func (b *Bridge) memberLeft(ctx context.Context, platform string, chatID, userID int64) {
	if b.addon != nil {
		b.addon.HandleLeave(ctx, platform, chatID, userID)
	}
}

// crosspostDeliverable — доставлять ли пост связки (решает аддон). Без аддона — да.
func (b *Bridge) crosspostDeliverable(ctx context.Context, maxChatID int64) bool {
	if b.addon == nil {
		return true
	}
	maxOwner, tgOwner := b.repo.GetCrosspostOwner(maxChatID)
	return b.addon.CrosspostDeliverable(ctx, maxOwner, tgOwner, maxChatID)
}

func normalizePairDirection(direction string) string {
	switch direction {
	case "tg>max", "max>tg", "both":
		return direction
	default:
		return "both"
	}
}

func pairDirectionLabel(direction string) string {
	switch normalizePairDirection(direction) {
	case "tg>max":
		return "TG → MAX"
	case "max>tg":
		return "MAX → TG"
	default:
		return "TG ↔ MAX"
	}
}

func (b *Bridge) pairDirection(ctx context.Context, tgChatID, maxChatID int64) string {
	if b.addon == nil {
		return "both"
	}
	return normalizePairDirection(b.addon.PairDirection(ctx, tgChatID, maxChatID))
}

func (b *Bridge) pairDirectionAllows(ctx context.Context, tgChatID, maxChatID int64, flow string) bool {
	direction := b.pairDirection(ctx, tgChatID, maxChatID)
	return direction == "both" || direction == flow
}

func (b *Bridge) setPairDirection(ctx context.Context, userID, tgChatID, maxChatID int64, direction string) (bool, string) {
	direction = normalizePairDirection(direction)
	if b.addon == nil {
		return false, "Изменение направления недоступно в этой сборке."
	}
	return b.addon.SetPairDirection(ctx, userID, tgChatID, maxChatID, direction)
}

// crosspostFooter запрашивает у расширения дополнительный footer.
// dst — платформа НАЗНАЧЕНИЯ ("tg"|"max"): ссылка на бота той платформы, где пост увидят.
// ВАЖНО: вызов инкрементит счётчик постов связки — звать один раз на пост.
func (b *Bridge) crosspostFooter(ctx context.Context, maxChatID int64, dst string) string {
	if b.addon == nil {
		return ""
	}
	maxOwner, tgOwner := b.repo.GetCrosspostOwner(maxChatID)
	url := b.cfg.TgBotURL
	if dst == "max" {
		url = b.cfg.MaxBotURL
	}
	return b.addon.CrosspostFooter(ctx, maxOwner, tgOwner, maxChatID, dst, url)
}

// crosspostOpenApp спрашивает у аддона inline-кнопку для кросспоста (если подключён)
// и возвращает спецификацию OPEN_APP-кнопки для прикрепления к MAX-сообщению. nil —
// кнопки нет (нет аддона / не нужна). Вызывается ПЕРЕД отправкой.
func (b *Bridge) crosspostOpenApp(ctx context.Context, tgChatID int64, tgMsgID int, maxChatID int64) *maxOpenApp {
	if b.addon == nil {
		return nil
	}
	_, tgOwner := b.repo.GetCrosspostOwner(maxChatID)
	appName, text, payload, ok := b.addon.CrosspostButton(ctx, tgOwner, tgChatID, tgMsgID, maxChatID)
	if !ok || appName == "" {
		return nil
	}
	return &maxOpenApp{Text: text, AppName: appName, Payload: payload}
}
