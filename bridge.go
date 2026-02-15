package main

import (
	"context"
	"sync"
	"time"

	maxbot "github.com/max-messenger/max-bot-api-client-go"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Config — настройки bridge, читаемые из env.
type Config struct {
	MaxToken  string // токен MAX API (нужен для direct-send/upload)
	TgBotURL  string // ссылка на TG-бота для /help
	MaxBotURL string // ссылка на MAX-бота для /help
}

// Bridge — основная структура, объединяющая зависимости.
type Bridge struct {
	cfg    Config
	repo   Repository
	tgBot  *tgbotapi.BotAPI
	maxApi *maxbot.Api
}

// NewBridge создаёт экземпляр Bridge.
func NewBridge(cfg Config, repo Repository, tgBot *tgbotapi.BotAPI, maxApi *maxbot.Api) *Bridge {
	return &Bridge{
		cfg:    cfg,
		repo:   repo,
		tgBot:  tgBot,
		maxApi: maxApi,
	}
}

// Run запускает TG и MAX listener'ы + периодическую очистку.
func (b *Bridge) Run(ctx context.Context) {
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

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); b.listenTelegram(ctx) }()
	go func() { defer wg.Done(); b.listenMax(ctx) }()
	wg.Wait()
}
