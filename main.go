package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	maxbot "github.com/max-messenger/max-bot-api-client-go"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		fmt.Fprintf(os.Stderr, "Переменная окружения %s не задана\n", key)
		os.Exit(1)
	}
	return v
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func genKey() string {
	b := make([]byte, 4)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))

	cfg := Config{
		MaxToken:    mustEnv("MAX_TOKEN"),
		TgBotURL:    envOr("TG_BOT_URL", "https://t.me/MaxTelegramBridgeBot"),
		MaxBotURL:   envOr("MAX_BOT_URL", "https://max.ru/id710708943262_bot"),
		WebhookURL:    os.Getenv("TG_WEBHOOK_URL"),
		MaxWebhookURL: os.Getenv("MAX_WEBHOOK_URL"),
		WebhookPort:   envOr("WEBHOOK_PORT", "8443"),
	}

	tgToken := mustEnv("TG_TOKEN")
	dbPath := envOr("DB_PATH", "bridge.db")

	var repo Repository
	var err error
	if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
		repo, err = NewPostgresRepo(dsn)
		if err != nil {
			slog.Error("PostgreSQL error", "err", err)
			os.Exit(1)
		}
		slog.Info("БД: PostgreSQL")
	} else {
		repo, err = NewSQLiteRepo(dbPath)
		if err != nil {
			slog.Error("SQLite error", "err", err)
			os.Exit(1)
		}
		slog.Info("БД: SQLite", "path", dbPath)
	}
	defer repo.Close()

	tgBot, err := tgbotapi.NewBotAPI(tgToken)
	if err != nil {
		slog.Error("TG bot error", "err", err)
		os.Exit(1)
	}
	slog.Info("Telegram бот запущен", "username", tgBot.Self.UserName)

	maxApi, err := maxbot.New(cfg.MaxToken)
	if err != nil {
		slog.Error("MAX bot error", "err", err)
		os.Exit(1)
	}
	maxInfo, err := maxApi.Bots.GetBot(context.Background())
	if err != nil {
		slog.Error("MAX bot info error", "err", err)
		os.Exit(1)
	}
	slog.Info("MAX бот запущен", "name", maxInfo.Name)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		slog.Info("Завершение...")
		cancel()
	}()

	bridge := NewBridge(cfg, repo, tgBot, maxApi)
	bridge.Run(ctx)
	slog.Info("Bridge остановлен")
}
