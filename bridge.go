package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	maxbot "github.com/max-messenger/max-bot-api-client-go"
)

var internalVKHandlersOnce sync.Once

// Config — настройки bridge, читаемые из env.
type Config struct {
	MaxToken         string  // токен MAX API (основной бот; нужен для direct-send/upload)
	MaxTokenOld      string  // токен старого MAX-бота (резерв/failover; "" — выключено)
	TgBotURL         string  // ссылка на TG-бота для /help
	MaxBotURL        string  // ссылка на основного MAX-бота для /help (старый бот)
	MaxBotURLReserve string  // ссылка на запасного MAX-бота (для failover; "" — скрыть)
	MaxWebhookURL    string  // базовый URL для webhook MAX (если пусто — long polling)
	MaxWebhookPort   string  // порт HTTP-сервера для MAX webhook (по умолчанию 8443)
	TgWebhookURL     string  // базовый URL для webhook TG (если пусто — long polling)
	TgWebhookPort    string  // порт HTTP-сервера для TG webhook (по умолчанию 8444)
	TgAPIURL         string  // custom TG Bot API URL (если пусто — api.telegram.org)
	AllowedUsers     []int64 // whitelist TG user IDs (empty = allow all)
	TgMaxFileSizeMB  int     // max file size TG->MAX in MB (0 = unlimited)
	MaxMaxFileSizeMB int     // max file size MAX->TG in MB (0 = unlimited)
	// MaxAllowedExts — whitelist расширений для TG→MAX (nil = не проверять локально).
	// Если задан, файлы с не-вхождением блокируются до отправки на CDN.
	MaxAllowedExts map[string]struct{}
	// MessageNewline — если true, текст идёт с новой строки после имени отправителя:
	// "Имя:\nтекст" вместо "Имя: текст". Задаётся через env MESSAGE_FORMAT=newline.
	MessageNewline bool
	// DisablePrefix — глобально отключает префиксы [TG]/[MAX] на всех чатах,
	// независимо от настройки в БД. Задаётся через env DISABLE_PREFIX=true.
	DisablePrefix bool
}

const maxAPIBaseURL = "https://platform-api2.max.ru/"

// chatBreaker хранит состояние circuit breaker для одного чата.
type chatBreaker struct {
	fails     int
	blockedAt time.Time
}

const (
	cbMaxFails = 3               // после N фейлов — блокируем
	cbCooldown = 5 * time.Minute // на сколько блокируем
)

// Bridge — основная структура, объединяющая зависимости.
type Bridge struct {
	cfg       Config
	repo      Repository
	tg        TGSender
	maxApi    *maxbot.Api
	maxApiOld *maxbot.Api // второй (старый) MAX-бот: резерв + обслуживание чатов без нового
	maxBotUID int64       // MAX bot user ID (для фильтрации своих сообщений)
	maxOldUID int64       // user ID старого MAX-бота (для фильтрации своих сообщений)
	// maxBotCache — кэш «какой бот в чате»: chatID → токен для отправки. Заполняется
	// по членству (GetChatMembership) и по тому, через чей вебхук пришёл апдейт.
	maxBotMu    sync.Mutex
	maxBotCache map[int64]string // chatID → token (новый по умолчанию, старый если нового нет)
	// maxSeenMid — дедуп входящих: один и тот же mid приходит от ОБОИХ ботов (если оба
	// в чате). Обрабатываем один раз.
	maxSeenMu  sync.Mutex
	maxSeenMid map[string]int64 // mid → unix-время (TTL-очистка)
	// maxDeleteSuppress — сообщения, которые бот удаляет локальной операцией.
	// Их message_removed нельзя зеркалировать обратно в Telegram.
	maxDeleteSuppress sync.Map // mid → struct{}

	httpClient *http.Client // для скачивания/загрузки файлов (большой таймаут)
	apiClient  *http.Client // для коротких API-запросов (малый таймаут)
	whSecret   string       // random path segment for webhook URLs

	cpWaitMu sync.Mutex
	cpWait   map[int64]int64 // MAX userId → TG channel ID (ожидание пересылки)

	cpTgOwnerMu sync.Mutex
	cpTgOwner   map[int64]int64 // TG channel ID → TG user ID (кто переслал пост)

	cbMu     sync.Mutex
	breakers map[int64]*chatBreaker // destination chatID → breaker

	// maxBlockedUntil — unix-секунды, до которых считаем аккаунт MAX заблокированным
	// (MAX вернул 403 account.blocked). Пока активно — глушим пользовательские
	// уведомления о недоставке: это глобальный бан, слать каждому бессмысленно.
	// Сбрасывается на первой успешной отправке в MAX.
	maxBlockedUntil atomic.Int64

	doctorMu   sync.Mutex
	doctorLast map[string]time.Time

	// Очередь обрабатывается параллельно между разными чатами, но строго по одному
	// сообщению на чат. Медленное видео одного пользователя не должно останавливать
	// доставку всех остальных.
	queueMu            sync.Mutex
	queueInFlight      map[int64]struct{}
	queueDestInFlight  map[string]struct{}
	queueMediaInFlight map[int64]struct{}

	// Буферизация TG media groups (альбомы)
	mgMu      sync.Mutex
	mgBuffers map[string]*mediaGroupBuffer // MediaGroupID → buffer

	// Опциональный аддон-расширение. Подключается через build-tag,
	// в публичной сборке всегда nil.
	addon Addon
	// extraCommands — команды меню, добавленные расширением (заполняет loadAddon).
	// Бридж не знает их семантику, просто показывает в setMyCommands.
	extraCommands []BotCommand
	// extraHelp — доп. блок для /help и /start (заполняет loadAddon). Пусто без аддона.
	extraHelp string
	// extraHelpPages — отдельные страницы расширенной справки. Ядро отображает
	// кнопки и тексты, не зная семантики приватного расширения.
	extraHelpPages []extraHelpPage
	// extraDescription — доп. строка к описанию бота (setMyDescription). Пусто без аддона.
	extraDescription string
}

type extraHelpPage struct {
	ID     string
	Button string
	Text   string
}

// NewBridge создаёт экземпляр Bridge.
func NewBridge(cfg Config, repo Repository, tg TGSender, maxApi *maxbot.Api, maxBotUID int64, maxApiOld *maxbot.Api, maxOldUID int64) *Bridge {
	// Derive webhook secret from tokens (stable across restarts)
	h := sha256.Sum256([]byte(cfg.MaxToken + tg.BotToken()))
	secret := hex.EncodeToString(h[:8])

	b := &Bridge{
		cfg:       cfg,
		repo:      repo,
		tg:        tg,
		maxApi:    maxApi,
		maxApiOld: maxApiOld,
		maxBotUID: maxBotUID,
		maxOldUID: maxOldUID,
		httpClient: &http.Client{
			Timeout: 5 * time.Minute, // для download/upload больших файлов
		},
		apiClient: &http.Client{
			Timeout: 15 * time.Second, // для коротких API-запросов
		},
		whSecret:           secret,
		cpWait:             make(map[int64]int64),
		cpTgOwner:          make(map[int64]int64),
		breakers:           make(map[int64]*chatBreaker),
		doctorLast:         make(map[string]time.Time),
		queueInFlight:      make(map[int64]struct{}),
		queueDestInFlight:  make(map[string]struct{}),
		queueMediaInFlight: make(map[int64]struct{}),
		mgBuffers:          make(map[string]*mediaGroupBuffer),
		maxBotCache:        make(map[int64]string),
		maxSeenMid:         make(map[string]int64),
	}
	b.addon = loadAddon(b)
	internalVKHandlersOnce.Do(func() {
		http.HandleFunc("/internal/vk-reload", b.handleInternalVKReload)
		http.HandleFunc("/internal/vk-connect", b.handleInternalVKConnect)
		http.HandleFunc("/internal/vk-chats", b.handleInternalVKChats)
		http.HandleFunc("/internal/vk-chat-bind", b.handleInternalVKChatBind)
		http.HandleFunc("/internal/vk-wall-bind", b.handleInternalVKWallBind)
	})
	return b
}

func validInternalSecret(got string) bool {
	secret := os.Getenv("COMMENT_SYNC_SECRET")
	return secret != "" && len(got) == len(secret) &&
		subtle.ConstantTimeCompare([]byte(got), []byte(secret)) == 1
}

// handleInternalVKChats проксирует безопасный список бесед: без OAuth-токенов
// и содержимого сообщений. Вызывается только авторизованным commenter.
func (b *Bridge) handleInternalVKChats(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Secret  string `json:"secret"`
		OwnerID int64  `json:"owner_id"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&in) != nil || in.OwnerID <= 0 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if !validInternalSecret(in.Secret) {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	lister, ok := b.addon.(interface {
		VKCabinetChatsJSON(context.Context, int64) ([]byte, error)
	})
	if !ok {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	body, err := lister.VKCabinetChatsJSON(r.Context(), in.OwnerID)
	if err != nil {
		slog.Warn("vk cabinet chats failed", "owner", in.OwnerID, "err", err)
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(body)
}

func (b *Bridge) handleInternalVKChatBind(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Secret       string `json:"secret"`
		ActorID      int64  `json:"actor_id"`
		Platform     string `json:"platform"`
		SourceChatID int64  `json:"source_chat_id"`
		AccountID    int64  `json:"account_id"`
		PeerID       int64  `json:"peer_id"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&in) != nil ||
		in.ActorID <= 0 || in.SourceChatID == 0 || in.AccountID <= 0 ||
		in.PeerID < 2000000000 || in.PeerID > 2999999999 ||
		(in.Platform != "tg" && in.Platform != "max") {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if !validInternalSecret(in.Secret) {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	creator, ok := b.addon.(interface {
		CreateVKCabinetChatBinding(context.Context, int64, string, int64, int64, int64) (int64, bool, string)
	})
	if !ok {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	endpointID, created, reason := creator.CreateVKCabinetChatBinding(
		r.Context(), in.ActorID, in.Platform, in.SourceChatID, in.AccountID, in.PeerID)
	status := http.StatusOK
	if !created {
		status = http.StatusUnprocessableEntity
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok": created, "endpoint_id": endpointID, "error": reason,
	})
}

func (b *Bridge) handleInternalVKWallBind(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Secret       string `json:"secret"`
		ActorID      int64  `json:"actor_id"`
		SourceChatID int64  `json:"source_chat_id"`
		AccountID    int64  `json:"account_id"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&in) != nil ||
		in.ActorID <= 0 || in.SourceChatID == 0 || in.AccountID <= 0 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if !validInternalSecret(in.Secret) {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	creator, ok := b.addon.(interface {
		CreateVKCabinetWallBinding(context.Context, int64, int64, int64) (int64, bool, string)
	})
	if !ok {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	endpointID, created, reason := creator.CreateVKCabinetWallBinding(
		r.Context(), in.ActorID, in.SourceChatID, in.AccountID)
	status := http.StatusOK
	if !created {
		status = http.StatusUnprocessableEntity
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok": created, "endpoint_id": endpointID, "error": reason,
	})
}

// handleInternalVKReload обновляет только in-memory индекс источников VK после
// изменения связки из кабинета. Доступ — по тому же внутреннему секрету, что у commenter.
func (b *Bridge) handleInternalVKReload(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Secret string `json:"secret"`
	}
	if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&in) != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	secret := os.Getenv("COMMENT_SYNC_SECRET")
	if secret == "" || in.Secret != secret {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	reloader, ok := b.addon.(interface{ ReloadVKConfig() })
	if !ok {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	reloader.ReloadVKConfig()
	w.WriteHeader(http.StatusNoContent)
}

// handleInternalVKConnect создаёт одноразовую ссылку существующего VK OAuth для
// пользователя, уже авторизованного в браузерном кабинете.
func (b *Bridge) handleInternalVKConnect(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Secret   string `json:"secret"`
		OwnerID  int64  `json:"owner_id"`
		Platform string `json:"platform"`
		ChatID   int64  `json:"chat_id"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&in) != nil ||
		in.OwnerID <= 0 || in.ChatID <= 0 || (in.Platform != "tg" && in.Platform != "max") {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	secret := os.Getenv("COMMENT_SYNC_SECRET")
	if secret == "" || len(in.Secret) != len(secret) ||
		subtle.ConstantTimeCompare([]byte(in.Secret), []byte(secret)) != 1 {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	starter, ok := b.addon.(interface {
		CreateVKCabinetLink(context.Context, int64, string, int64) (string, error)
	})
	if !ok {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	link, err := starter.CreateVKCabinetLink(r.Context(), in.OwnerID, in.Platform, in.ChatID)
	if err != nil {
		slog.Warn("vk cabinet connect link failed", "owner", in.OwnerID, "err", err)
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]string{"url": link})
}

// cbBlocked проверяет, заблокирован ли чат.
func (b *Bridge) cbBlocked(chatID int64) bool {
	b.cbMu.Lock()
	defer b.cbMu.Unlock()
	cb, ok := b.breakers[chatID]
	if !ok {
		return false
	}
	if cb.fails >= cbMaxFails && time.Since(cb.blockedAt) < cbCooldown {
		return true
	}
	if cb.fails >= cbMaxFails {
		// Кулдаун прошёл — сбрасываем, пробуем снова
		delete(b.breakers, chatID)
	}
	return false
}

// cbFail регистрирует ошибку. Возвращает true если чат только что заблокировался.
func (b *Bridge) cbFail(chatID int64) bool {
	b.cbMu.Lock()
	defer b.cbMu.Unlock()
	cb, ok := b.breakers[chatID]
	if !ok {
		cb = &chatBreaker{}
		b.breakers[chatID] = cb
	}
	cb.fails++
	if cb.fails == cbMaxFails {
		cb.blockedAt = time.Now()
		slog.Warn("circuit breaker: chat blocked", "chatID", chatID, "cooldown", cbCooldown)
		return true
	}
	return false
}

// cbSuccess сбрасывает счётчик ошибок для чата. Любая успешная отправка в MAX также
// снимает глобальный флаг «аккаунт заблокирован» (бан, видимо, сняли).
func (b *Bridge) cbSuccess(chatID int64) {
	b.cbMu.Lock()
	delete(b.breakers, chatID)
	b.cbMu.Unlock()
	b.maxBlockedUntil.Store(0)
}

// chatUnavailableMarkers — маркеры ПЕРМАНЕНТНОЙ недоступности чата: бот удалён,
// заблокирован или лишён прав в этом конкретном чате. Намеренно БЕЗ голых "403"/"404"
// и account.blocked — это глобальный бан аккаунта MAX (транзиент), его ловит
// maxAccountBlocked(), и связки на паузу по нему ставить нельзя.
var chatUnavailableMarkers = []string{
	"chat.not.found", "chat.denied", "not enough rights",
	"bot was kicked", "bot was blocked", "Forbidden",
	"chat not found", "CHAT_WRITE_FORBIDDEN", "user is deactivated",
	"need administrator rights", "PEER_ID_INVALID",
}

// isChatUnavailable — постоянная ошибка «бот недоступен в этом чате».
func isChatUnavailable(errStr string) bool {
	for _, m := range chatUnavailableMarkers {
		if strings.Contains(errStr, m) {
			return true
		}
	}
	return false
}

// cbPauseForChat ставит на паузу связку(и), в которые входит недоступный chatID
// (пробуем все интерпретации: chatID как MAX- или TG-сторона пары/кросспоста).
// Возвращает true ТОЛЬКО при переходе unpaused→paused — чтобы уведомить владельца
// один раз, а не на каждом упавшем сообщении.
func (b *Bridge) cbPauseForChat(chatID int64) bool {
	newly := false
	if tg, ok := b.repo.GetTgChat(chatID); ok && !b.repo.PairPaused(tg, chatID) {
		b.repo.SetPairPaused(tg, chatID, true)
		newly = true
	}
	if mx, ok := b.repo.GetMaxChat(chatID); ok && !b.repo.PairPaused(chatID, mx) {
		b.repo.SetPairPaused(chatID, mx, true)
		newly = true
	}
	if _, _, ok := b.repo.GetCrosspostTgChat(chatID); ok && !b.repo.CrosspostPaused(chatID) {
		b.repo.SetCrosspostPaused(chatID, true)
		newly = true
	}
	if mx, _, ok := b.repo.GetCrosspostMaxChat(chatID); ok && !b.repo.CrosspostPaused(mx) {
		b.repo.SetCrosspostPaused(mx, true)
		newly = true
	}
	return newly
}

// cbPermanentFail вызывается на ПЕРМАНЕНТНОЙ ошибке доставки (бот заблокирован/
// удалён/лишён прав в чате chatID): ставит связку на паузу и DM-ит владельцу.
// Глобальный бан аккаунта MAX игнорируем — это транзиент, не повод паузить всё.
func (b *Bridge) cbPermanentFail(ctx context.Context, chatID int64) {
	if b.maxAccountBlocked() {
		return
	}
	if !b.cbPauseForChat(chatID) {
		return // уже на паузе или связки нет — не дублируем
	}
	slog.Warn("связка на паузе: чат недоступен (бот заблокирован/удалён/лишён прав)", "chatID", chatID)
	b.notifyChatPaused(ctx, chatID)
}

// notifyChatPaused DM-ит владельцу TG-стороны: чат недоступен, связка на паузе.
func (b *Bridge) notifyChatPaused(ctx context.Context, chatID int64) {
	msg := "🚫 Чат недоступен — бот заблокирован, удалён или лишён прав.\n\n" +
		"Связку поставил на паузу, чтобы не терять сообщения на бесконечных ретраях. " +
		"Когда вернёте бота в чат и сделаете администратором, отправьте /unpause в связанной группе."
	if _, _, ok := b.repo.GetCrosspostTgChat(chatID); ok {
		msg = "🚫 Канал MAX недоступен — бот заблокирован, удалён или лишён прав администратора.\n\n" +
			"Кросспостинг поставлен на паузу. Верните бота в канал, затем откройте /crosspost и нажмите «▶️ Возобновить»."
	} else if _, _, ok := b.repo.GetCrosspostMaxChat(chatID); ok {
		msg = "🚫 Канал Telegram недоступен — бот удалён или лишён прав администратора.\n\n" +
			"Кросспостинг поставлен на паузу. Верните бота в канал, затем откройте /crosspost и нажмите «▶️ Возобновить»."
	}
	owner := b.repo.TgOwnerForChat(chatID)
	if owner == 0 {
		slog.Warn("paused-notify: владелец TG не найден", "chatID", chatID)
		return
	}
	if _, err := b.tg.SendMessage(ctx, owner, msg, nil); err != nil {
		slog.Warn("paused-notify: DM не отправилось", "err", err, "owner", owner, "chatID", chatID)
	}
}

// usernameFromMaxURL извлекает юзернейм бота из ссылки вида https://max.ru/id..._bot.
func usernameFromMaxURL(u string) string {
	if i := strings.LastIndexByte(u, '/'); i >= 0 {
		return u[i+1:]
	}
	return u
}

// maxTextHasLink — есть ли в MAX-тексте ссылка/внешнее упоминание (для правила link-new).
// http/t.me — всегда. Голый «@» считаем ссылкой ТОЛЬКО если упоминают НЕ нашего бота:
// иначе новичка ложно банило за @id..._bot (упоминание самого бота — не реклама).
func (b *Bridge) maxTextHasLink(text string) bool {
	if strings.Contains(text, "http") || strings.Contains(text, "t.me") {
		return true
	}
	if !strings.Contains(text, "@") {
		return false
	}
	cleaned := text
	for _, u := range []string{usernameFromMaxURL(b.cfg.MaxBotURL), usernameFromMaxURL(b.cfg.MaxBotURLReserve)} {
		if u != "" {
			cleaned = strings.ReplaceAll(cleaned, "@"+u, "")
		}
	}
	return strings.Contains(cleaned, "@")
}

// noteMaxSendErr выставляет глобальный флаг блокировки аккаунта MAX, если ошибка —
// account.blocked (403). Окно 10 мин, продлевается на каждой такой ошибке.
func (b *Bridge) noteMaxSendErr(errStr string) {
	if strings.Contains(errStr, "account.blocked") {
		b.maxBlockedUntil.Store(time.Now().Unix() + 600)
	}
}

// maxAccountBlocked — активен ли глобальный бан аккаунта MAX прямо сейчас.
func (b *Bridge) maxAccountBlocked() bool {
	return time.Now().Unix() < b.maxBlockedUntil.Load()
}

// maxMaxFileBytes returns the MAX-to-TG file size limit in bytes (0 = unlimited).
func (c *Config) maxMaxFileBytes() int64 {
	if c.MaxMaxFileSizeMB <= 0 {
		return 0
	}
	return int64(c.MaxMaxFileSizeMB) * 1024 * 1024
}

// isUserAllowed проверяет, есть ли tgUserID в белом списке.
// Если AllowedUsers пуст — доступ разрешён всем.
func (b *Bridge) isUserAllowed(tgUserID int64) bool {
	if len(b.cfg.AllowedUsers) == 0 {
		return true
	}
	for _, id := range b.cfg.AllowedUsers {
		if id == tgUserID {
			return true
		}
	}
	return false
}

// checkUserAllowed проверяет доступ пользователя и отправляет сообщение об отказе если нужно.
// Возвращает true если доступ разрешён, false — если запрещён (и уже отправил ответ).
// userID == 0 трактуется как «нет отправителя» — доступ запрещается.
func (b *Bridge) checkUserAllowed(ctx context.Context, chatID, userID int64, threadID int) bool {
	if userID != 0 && b.isUserAllowed(userID) {
		return true
	}
	slog.Debug("TG user not allowed", "uid", userID)
	b.tg.SendMessage(ctx, chatID, "У вас нет прав доступа к боту.", &SendOpts{ThreadID: threadID})
	return false
}

// isCrosspostOwner проверяет, является ли userID владельцем связки.
// owner_id=0 и tg_owner_id=0 — старая связка, доступна всем.
func (b *Bridge) isCrosspostOwner(maxChatID, userID int64) bool {
	maxOwner, tgOwner := b.repo.GetCrosspostOwner(maxChatID)
	if maxOwner == 0 && tgOwner == 0 {
		return true // legacy, no owner
	}
	return userID == maxOwner || userID == tgOwner
}

// tgFileURL возвращает прямой URL файла из TG — через custom API если настроен.
func (b *Bridge) tgFileURL(ctx context.Context, fileID string) (string, error) {
	filePath, err := b.tg.GetFile(ctx, fileID)
	if err != nil {
		return "", err
	}
	return b.tg.GetFileDirectURL(filePath), nil
}

// tgChatTitle возвращает title TG-чата/канала по ID. Пустая строка если не удалось.
func (b *Bridge) tgChatTitle(ctx context.Context, chatID int64) string {
	title, err := b.tg.GetChat(ctx, chatID)
	if err != nil {
		return ""
	}
	return title
}

// isSelfTgBot проверяет, является ли отправитель нашим ботом (а не чужим).
func (b *Bridge) isSelfTgBot(from *UserInfo) bool {
	return from != nil && from.IsBot && from.UserName == b.tg.BotUsername()
}

// hasPrefix — обёртка над repo.HasPrefix с учётом глобального флага DisablePrefix.
// Возвращает false если префиксы отключены глобально через env.
func (b *Bridge) hasPrefix(platform string, chatID int64) bool {
	if b.cfg.DisablePrefix {
		return false
	}
	return b.repo.HasPrefix(platform, chatID)
}

// notifyTgUser отправляет пользовательское уведомление (например, об ошибке загрузки).
// Для bridge-режима — в чат, где пришло сообщение (в нужный тред, если форум).
// Для crosspost — в ЛС владельцу связки (tg_owner_id), чтобы не мусорить в канал.
// Если владелец не задан (legacy) — уведомление дропается с warn-логом.
func (b *Bridge) notifyTgUser(ctx context.Context, srcChat *TGMessage, maxChatID int64, text string, isCrosspost bool) {
	// Глобальный бан аккаунта MAX — не заваливаем юзеров уведомлениями о недоставке
	// (бан общий для всех чатов; сообщения и так копятся в очереди).
	if b.maxAccountBlocked() {
		slog.Debug("notify suppressed: MAX account blocked", "text", text)
		return
	}
	if isCrosspost {
		_, tgOwner := b.repo.GetCrosspostOwner(maxChatID)
		if tgOwner == 0 {
			slog.Warn("crosspost notify skipped: no tg owner", "maxChat", maxChatID, "text", text)
			return
		}
		if _, err := b.tg.SendMessage(ctx, tgOwner, text, nil); err != nil {
			slog.Warn("crosspost notify DM failed", "err", err, "tgOwner", tgOwner)
		}
		return
	}
	var opts *SendOpts
	if srcChat != nil && srcChat.MessageThreadID != 0 {
		opts = &SendOpts{ThreadID: srcChat.MessageThreadID}
	}
	b.tg.SendMessage(ctx, srcChat.Chat.ID, text, opts)
}

// uploadErrHint превращает техническую ошибку загрузки в короткий текст для юзера.
// Возвращает пустую строку для неизвестных ошибок — вызывающий код тогда
// отправит только generic-сообщение без технической мути.
func uploadErrHint(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	switch {
	case strings.Contains(s, "file is too big"):
		return "файл слишком большой"
	case strings.Contains(s, "file is not found") || strings.Contains(s, "FILE_REFERENCE_EXPIRED"):
		return "файл не найден"
	case strings.Contains(s, "attachment.not.ready"):
		return "попробуйте ещё раз"
	}
	return ""
}

// uploadErrMsg собирает user-facing сообщение: base + ": hint" если подсказка известна,
// иначе просто "base." (без технической ошибки).
func uploadErrMsg(base string, err error) string {
	if hint := uploadErrHint(err); hint != "" {
		return base + ": " + hint + "."
	}
	return base + "."
}

func (b *Bridge) tgWebhookPath() string {
	return "/tg-webhook-" + b.whSecret
}

func (b *Bridge) maxWebhookPath() string {
	return "/max-webhook-" + b.whSecret
}

// registerCommands регистрирует команды бота в Telegram.
func (b *Bridge) registerCommands(ctx context.Context) {
	cmds := []BotCommand{
		{Command: "bridge", Description: "Связать чат с MAX-чатом"},
		{Command: "bridge_update", Description: "Обновить связку группы (показать в кабинете)"},
		{Command: "pause", Description: "Поставить связку на паузу"},
		{Command: "unpause", Description: "Возобновить пересылку"},
		{Command: "unbridge", Description: "Удалить связку чатов"},
		{Command: "thread", Description: "Установить топик для сообщений из MAX"},
		{Command: "thread_bridge", Description: "Связать тред с отдельным MAX-чатом"},
		{Command: "thread_unbridge", Description: "Удалить связку треда"},
		{Command: "crosspost", Description: "Список связок кросспостинга"},
		{Command: "doctor", Description: "Диагностика всех моих подключений"},
		{Command: "help", Description: "Инструкция"},
	}
	// Команды, добавленные опциональными расширениями (через loadAddon). Бридж не
	// знает, что это за команды — только что их нужно показать в меню.
	cmds = append(cmds, b.extraCommands...)
	if err := b.tg.SetMyCommands(ctx, cmds, nil); err != nil {
		slog.Error("TG setMyCommands (default) failed", "err", err)
	}
	groupCmds := append([]BotCommand(nil), cmds...)
	for i := range groupCmds {
		groupCmds[i].IsEphemeral = true
	}
	if err := b.tg.SetMyCommands(ctx, groupCmds, &CommandScope{Type: "all_group_chats"}); err != nil {
		slog.Error("TG setMyCommands (groups) failed", "err", err)
	}
	if err := b.tg.SetMyCommands(ctx, groupCmds, &CommandScope{Type: "all_chat_administrators"}); err != nil {
		slog.Error("TG setMyCommands (admins) failed", "err", err)
	}

	// Описание бота (экран "Что умеет этот бот"). Доп. строка от расширения (impl
	// в loadAddon) — без расширения пусто.
	desc := "Мост между Telegram и MAX: зеркалит сообщения в связанных группах и кросспостит каналы." + b.extraDescription +
		"\n\n📚 База знаний: " + knowledgeBaseURL +
		"\n\n💬 Поддержка и новости: https://t.me/+0ucbOj4wBwQzMWNi"
	if err := b.tg.SetMyDescription(ctx, desc); err != nil {
		slog.Error("TG setMyDescription failed", "err", err)
	}

	// Кнопка «Меню» бота → открыть веб-апп (кабинет). Только если задан MINIAPP_URL
	// (в публичной сборке без него кнопка не ставится — приватный URL не светим).
	if url := os.Getenv("MINIAPP_URL"); url != "" {
		if err := b.tg.SetMenuButtonWebApp(ctx, "Кабинет", url); err != nil {
			slog.Error("TG setChatMenuButton failed", "err", err)
		}
	}
}

// reserveBotHint — подсказка добавить запасного MAX-бота админом (для failover).
// Пусто, если запасной не настроен (MaxBotURLReserve == "").
func (b *Bridge) reserveBotHint() string {
	if b.cfg.MaxBotURLReserve == "" {
		return ""
	}
	return "\n\n🔁 Для надёжности добавьте админом и запасного бота (подменит основной, если тот недоступен): " + b.cfg.MaxBotURLReserve
}

// Run запускает TG и MAX listener'ы + периодическую очистку.
func (b *Bridge) Run(ctx context.Context) {
	b.registerCommands(ctx)
	go func() {
		t := time.NewTicker(10 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				b.repo.CleanOldMessages()
			}
		}
	}()

	// Воркер очереди — часто подхватывает следующий элемент каждого свободного
	// чата. Сами отправки выполняются асинхронно и изолированы по destination.
	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				b.processQueue(ctx)
			}
		}
	}()

	startWebhookServer := func(port string, label string) {
		go func() {
			addr := ":" + port
			srv := &http.Server{
				Addr:         addr,
				ReadTimeout:  10 * time.Second,
				WriteTimeout: 10 * time.Second,
				IdleTimeout:  60 * time.Second,
			}
			slog.Info("Webhook server starting", "label", label, "addr", addr)
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				slog.Error("Webhook server failed", "label", label, "err", err)
			}
		}()
	}

	if b.cfg.MaxWebhookURL != "" && b.cfg.TgWebhookURL != "" && b.cfg.MaxWebhookPort == b.cfg.TgWebhookPort {
		// оба на одном порту — один сервер
		startWebhookServer(b.cfg.MaxWebhookPort, "MAX+TG")
	} else {
		if b.cfg.MaxWebhookURL != "" {
			startWebhookServer(b.cfg.MaxWebhookPort, "MAX")
		}
		if b.cfg.TgWebhookURL != "" {
			startWebhookServer(b.cfg.TgWebhookPort, "TG")
		}
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); b.listenTelegram(ctx) }()
	go func() { defer wg.Done(); b.listenMax(ctx) }()
	if b.addon != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := b.addon.Start(ctx); err != nil {
				slog.Error("addon stopped with error", "err", err)
			}
		}()
	}
	wg.Wait()
}
