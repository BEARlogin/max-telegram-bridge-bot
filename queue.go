package main

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const (
	queueMaxAttempts = 20            // максимум попыток
	queueMaxAge      = 30 * time.Minute // дропаем сообщения старше 30 мин
	queueBatchSize   = 10
)

// retryDelay возвращает задержку перед следующей попыткой (экспоненциально).
func retryDelay(attempt int) time.Duration {
	switch {
	case attempt < 3:
		return 10 * time.Second
	case attempt < 6:
		return 30 * time.Second
	case attempt < 10:
		return 1 * time.Minute
	default:
		return 2 * time.Minute
	}
}

// enqueueTg2Max ставит сообщение TG→MAX в очередь.
func (b *Bridge) enqueueTg2Max(tgChatID int64, tgMsgID int, maxChatID int64, text, attType, attToken, replyTo, format string) {
	now := time.Now().Unix()
	item := &QueueItem{
		Direction: "tg2max",
		SrcChatID: tgChatID,
		DstChatID: maxChatID,
		SrcMsgID:  strconv.Itoa(tgMsgID),
		Text:      text,
		AttType:   attType,
		AttToken:  attToken,
		ReplyTo:   replyTo,
		Format:    format,
		CreatedAt: now,
		NextRetry: now + int64(retryDelay(0).Seconds()),
	}
	if err := b.repo.EnqueueSend(item); err != nil {
		slog.Error("enqueue failed", "err", err)
	} else {
		slog.Info("enqueued for retry", "dir", "tg2max", "dst", maxChatID)
	}
}

// processQueue обрабатывает очередь — вызывается периодически.
func (b *Bridge) processQueue(ctx context.Context) {
	items, err := b.repo.PeekQueue(queueBatchSize)
	if err != nil {
		slog.Error("peek queue failed", "err", err)
		return
	}

	now := time.Now()
	for _, item := range items {
		// Слишком старое или слишком много попыток — дропаем
		age := now.Sub(time.Unix(item.CreatedAt, 0))
		if item.Attempts >= queueMaxAttempts || age > queueMaxAge {
			slog.Warn("queue item expired", "id", item.ID, "attempts", item.Attempts, "age", age)
			b.repo.DeleteFromQueue(item.ID)
			// Уведомляем источник
			if item.Direction == "tg2max" {
				b.tgBot.Send(tgbotapi.NewMessage(item.SrcChatID,
					fmt.Sprintf("Сообщение не доставлено в MAX после %d попыток. MAX API был недоступен.", item.Attempts)))
			}
			continue
		}

		// Пробуем отправить
		if item.Direction == "tg2max" {
			mid, err := b.sendMaxDirectFormatted(ctx, item.DstChatID, item.Text, item.AttType, item.AttToken, item.ReplyTo, item.Format)
			if err != nil {
				slog.Warn("queue retry failed", "id", item.ID, "attempt", item.Attempts+1, "err", err)
				nextRetry := now.Add(retryDelay(item.Attempts + 1)).Unix()
				b.repo.IncrementAttempt(item.ID, nextRetry)
				continue
			}
			slog.Info("queue retry ok", "id", item.ID, "mid", mid)
			// Сохраняем маппинг
			tgMsgID, _ := strconv.Atoi(item.SrcMsgID)
			if tgMsgID > 0 {
				b.repo.SaveMsg(item.SrcChatID, tgMsgID, item.DstChatID, mid)
			}
			b.repo.DeleteFromQueue(item.ID)
		}
	}
}
