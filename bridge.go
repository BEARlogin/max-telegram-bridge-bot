package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	maxbot "github.com/max-messenger/max-bot-api-client-go"
)

// Config — настройки bridge, читаемые из env.
type Config struct {
	MaxToken     string  // токен MAX API (нужен для direct-send/upload)
	TgBotURL     string  // ссылка на TG-бота для /help
	MaxBotURL    string  // ссылка на MAX-бота для /help
	MaxWebhookURL  string  // базовый URL для webhook MAX (если пусто — long polling)
	MaxWebhookPort string  // порт HTTP-сервера для MAX webhook (по умолчанию 8443)
	TgWebhookURL   string  // базовый URL для webhook TG (если пусто — long polling)
	TgWebhookPort  string  // порт HTTP-сервера для TG webhook (по умолчанию 8444)
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

// chatBreaker хранит состояние circuit breaker для одного чата.
type chatBreaker struct {
	fails    int
	blockedAt time.Time
}

const (
	cbMaxFails = 3              // после N фейлов — блокируем
	cbCooldown = 5 * time.Minute // на сколько блокируем
)

// Bridge — основная структура, объединяющая зависимости.
type Bridge struct {
	cfg        Config
	repo       Repository
	tg          TGSender
	maxApi      *maxbot.Api
	maxBotUID   int64 // MAX bot user ID (для фильтрации своих сообщений)
	httpClient *http.Client // для скачивания/загрузки файлов (большой таймаут)
	apiClient  *http.Client // для коротких API-запросов (малый таймаут)
	whSecret   string // random path segment for webhook URLs

	cpWaitMu sync.Mutex
	cpWait   map[int64]int64 // MAX userId → TG channel ID (ожидание пересылки)

	cpTgOwnerMu sync.Mutex
	cpTgOwner   map[int64]int64 // TG channel ID → TG user ID (кто переслал пост)

	cbMu       sync.Mutex
	breakers   map[int64]*chatBreaker // destination chatID → breaker

	// Буферизация TG media groups (альбомы)
	mgMu      sync.Mutex
	mgBuffers map[string]*mediaGroupBuffer // MediaGroupID → buffer

	// Опциональный аддон-расширение. Подключается через build-tag,
	// в публичной сборке всегда nil.
	addon Addon
	// extraCommands — команды меню, добавленные расширением (заполняет loadAddon).
	// Бридж не знает их семантику, просто показывает в setMyCommands.
	extraCommands []BotCommand
}

// NewBridge создаёт экземпляр Bridge.
func NewBridge(cfg Config, repo Repository, tg TGSender, maxApi *maxbot.Api, maxBotUID int64) *Bridge {
	// Derive webhook secret from tokens (stable across restarts)
	h := sha256.Sum256([]byte(cfg.MaxToken + tg.BotToken()))
	secret := hex.EncodeToString(h[:8])

	b := &Bridge{
		cfg:    cfg,
		repo:   repo,
		tg:        tg,
		maxApi:    maxApi,
		maxBotUID: maxBotUID,
		httpClient: &http.Client{
			Timeout: 5 * time.Minute, // для download/upload больших файлов
		},
		apiClient: &http.Client{
			Timeout: 15 * time.Second, // для коротких API-запросов
		},
		whSecret:  secret,
		cpWait:    make(map[int64]int64),
		cpTgOwner: make(map[int64]int64),
		breakers:  make(map[int64]*chatBreaker),
		mgBuffers: make(map[string]*mediaGroupBuffer),
	}
	b.addon = loadAddon(b)
	return b
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

// cbSuccess сбрасывает счётчик ошибок для чата.
func (b *Bridge) cbSuccess(chatID int64) {
	b.cbMu.Lock()
	defer b.cbMu.Unlock()
	delete(b.breakers, chatID)
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
		{Command: "unbridge", Description: "Удалить связку чатов"},
		{Command: "thread", Description: "Установить топик для сообщений из MAX"},
		{Command: "thread_bridge", Description: "Связать тред с отдельным MAX-чатом"},
		{Command: "thread_unbridge", Description: "Удалить связку треда"},
		{Command: "crosspost", Description: "Список связок кросспостинга"},
		{Command: "help", Description: "Инструкция"},
	}
	// Команды, добавленные опциональными расширениями (через loadAddon). Бридж не
	// знает, что это за команды — только что их нужно показать в меню.
	cmds = append(cmds, b.extraCommands...)
	if err := b.tg.SetMyCommands(ctx, cmds, nil); err != nil {
		slog.Error("TG setMyCommands (default) failed", "err", err)
	}
	if err := b.tg.SetMyCommands(ctx, cmds, &CommandScope{Type: "all_chat_administrators"}); err != nil {
		slog.Error("TG setMyCommands (admins) failed", "err", err)
	}
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

	// Воркер очереди — проверяет каждые 10 секунд
	go func() {
		t := time.NewTicker(10 * time.Second)
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
