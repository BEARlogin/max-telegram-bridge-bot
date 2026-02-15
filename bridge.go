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
	MaxToken    string // токен MAX API (нужен для direct-send/upload)
	TgBotURL    string // ссылка на TG-бота для /help
	MaxBotURL   string // ссылка на MAX-бота для /help
	WebhookURL  string // базовый URL для webhook (если пусто — long polling)
	WebhookPort string // порт для webhook сервера
}

// Bridge — основная структура, объединяющая зависимости.
type Bridge struct {
	cfg        Config
	repo       Repository
	tgBot      *tgbotapi.BotAPI
	maxApi     *maxbot.Api
	httpClient *http.Client
	whSecret   string // random path segment for webhook URLs
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
		whSecret: secret,
	}
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
		tgbotapi.BotCommand{Command: "help", Description: "Инструкция"},
	)
	if _, err := b.tgBot.Request(groupCmds); err != nil {
		slog.Error("TG setMyCommands (default) failed", "err", err)
	}

	// Команды для каналов
	channelCmds := tgbotapi.NewSetMyCommandsWithScope(
		tgbotapi.NewBotCommandScopeAllChatAdministrators(),
		tgbotapi.BotCommand{Command: "crosspost", Description: "Связать канал для кросспостинга"},
		tgbotapi.BotCommand{Command: "uncrosspost", Description: "Удалить кросспостинг"},
		tgbotapi.BotCommand{Command: "bridge", Description: "Связать чат с MAX-чатом"},
		tgbotapi.BotCommand{Command: "unbridge", Description: "Удалить связку чатов"},
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
