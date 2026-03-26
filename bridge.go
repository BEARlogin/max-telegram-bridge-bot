package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"sync"
	"time"

	maxbot "github.com/max-messenger/max-bot-api-client-go"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Config — настройки bridge, читаемые из env.
type Config struct {
	MaxToken         string  // токен MAX API (нужен для direct-send/upload)
	TgBotURL         string  // ссылка на TG-бота для /help
	MaxBotURL        string  // ссылка на MAX-бота для /help
	WebhookURL       string  // базовый URL для webhook (если пусто — long polling)
	WebhookPort      string  // порт для webhook сервера
	TgAPIURL         string  // custom TG Bot API URL (если пусто — api.telegram.org)
	AllowedUsers     []int64 // whitelist TG user IDs (empty = allow all)
	TgMaxFileSizeMB  int     // max file size TG->MAX in MB (0 = unlimited)
	MaxMaxFileSizeMB int     // max file size MAX->TG in MB (0 = unlimited)
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
	tgBot      *tgbotapi.BotAPI
	maxApi     *maxbot.Api
	httpClient *http.Client
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
}

// NewBridge создаёт экземпляр Bridge.
func NewBridge(cfg Config, repo Repository, tgBot *tgbotapi.BotAPI, maxApi *maxbot.Api) *Bridge {
	// Derive webhook secret from tokens (stable across restarts)
	h := sha256.Sum256([]byte(cfg.MaxToken + tgBot.Token))
	secret := hex.EncodeToString(h[:8])

	return &Bridge{
		cfg:    cfg,
		repo:   repo,
		tgBot:  tgBot,
		maxApi: maxApi,
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
		whSecret:  secret,
		cpWait:    make(map[int64]int64),
		cpTgOwner: make(map[int64]int64),
		breakers:  make(map[int64]*chatBreaker),
		mgBuffers: make(map[string]*mediaGroupBuffer),
	}
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
func (b *Bridge) tgFileURL(fileID string) (string, error) {
	file, err := b.tgBot.GetFile(tgbotapi.FileConfig{FileID: fileID})
	if err != nil {
		return "", err
	}
	if b.cfg.TgAPIURL != "" {
		// --local mode: file_path — абсолютный путь, отдаём через nginx
		return b.cfg.TgAPIURL + "/" + file.FilePath, nil
	}
	return file.Link(b.tgBot.Token), nil
}

// tgChatTitle возвращает title TG-чата/канала по ID. Пустая строка если не удалось.
func (b *Bridge) tgChatTitle(chatID int64) string {
	chat, err := b.tgBot.GetChat(tgbotapi.ChatInfoConfig{ChatConfig: tgbotapi.ChatConfig{ChatID: chatID}})
	if err != nil {
		return ""
	}
	return chat.Title
}

func (b *Bridge) tgWebhookPath() string {
	return "/tg-webhook-" + b.whSecret
}

func (b *Bridge) maxWebhookPath() string {
	return "/max-webhook-" + b.whSecret
}

// registerCommands регистрирует команды бота в Telegram.
func (b *Bridge) registerCommands() {
	// Команды для групп и личных чатов
	groupCmds := tgbotapi.NewSetMyCommands(
		tgbotapi.BotCommand{Command: "bridge", Description: "Связать чат с MAX-чатом"},
		tgbotapi.BotCommand{Command: "unbridge", Description: "Удалить связку чатов"},
		tgbotapi.BotCommand{Command: "crosspost", Description: "Список связок кросспостинга"},
		tgbotapi.BotCommand{Command: "help", Description: "Инструкция"},
	)
	if _, err := b.tgBot.Request(groupCmds); err != nil {
		slog.Error("TG setMyCommands (default) failed", "err", err)
	}

	// Команды для админов (группы + каналы)
	channelCmds := tgbotapi.NewSetMyCommandsWithScope(
		tgbotapi.NewBotCommandScopeAllChatAdministrators(),
		tgbotapi.BotCommand{Command: "bridge", Description: "Связать чат с MAX-чатом"},
		tgbotapi.BotCommand{Command: "unbridge", Description: "Удалить связку чатов"},
		tgbotapi.BotCommand{Command: "crosspost", Description: "Список связок кросспостинга"},
		tgbotapi.BotCommand{Command: "help", Description: "Инструкция"},
	)
	if _, err := b.tgBot.Request(channelCmds); err != nil {
		slog.Error("TG setMyCommands (admins) failed", "err", err)
	}
}

// Run запускает TG и MAX listener'ы + периодическую очистку.
func (b *Bridge) Run(ctx context.Context) {
	b.registerCommands()
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

	if b.cfg.WebhookURL != "" {
		go func() {
			addr := ":" + b.cfg.WebhookPort
			srv := &http.Server{
				Addr:         addr,
				ReadTimeout:  10 * time.Second,
				WriteTimeout: 10 * time.Second,
				IdleTimeout:  60 * time.Second,
			}
			slog.Info("Webhook server starting", "addr", addr)
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				slog.Error("Webhook server failed", "err", err)
			}
		}()
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); b.listenTelegram(ctx) }()
	go func() { defer wg.Done(); b.listenMax(ctx) }()
	wg.Wait()
}
