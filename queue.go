package main

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

const (
	queueMaxAttempts = 30                // максимум попыток
	queueMaxAge      = 24 * time.Hour   // дропаем сообщения старше 24 часов
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

// enqueueMax2Tg ставит сообщение MAX→TG в очередь.
func (b *Bridge) enqueueMax2Tg(maxChatID, tgChatID int64, maxMid, text, attType, attURL, parseMode string) {
	now := time.Now().Unix()
	item := &QueueItem{
		Direction: "max2tg",
		SrcChatID: maxChatID,
		DstChatID: tgChatID,
		SrcMsgID:  maxMid,
		Text:      text,
		AttType:   attType,
		AttURL:    attURL,
		ParseMode: parseMode,
		CreatedAt: now,
		NextRetry: now + int64(retryDelay(0).Seconds()),
	}
	if err := b.repo.EnqueueSend(item); err != nil {
		slog.Error("enqueue failed", "err", err)
	} else {
		slog.Info("enqueued for retry", "dir", "max2tg", "dst", tgChatID)
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
			slog.Warn("queue item expired", "id", item.ID, "dir", item.Direction, "attempts", item.Attempts, "age", age)
			b.repo.DeleteFromQueue(item.ID)
			if item.Direction == "tg2max" {
				b.tg.SendMessage(ctx, item.SrcChatID, fmt.Sprintf("Сообщение не доставлено в MAX после %d попыток.", item.Attempts), nil)
			}
			continue
		}

		switch item.Direction {
		case "tg2max":
			b.processQueueTg2Max(ctx, item, now)
		case "max2tg":
			b.processQueueMax2Tg(ctx, item, now)
		}
	}
}

func (b *Bridge) processQueueTg2Max(ctx context.Context, item QueueItem, now time.Time) {
	mid, err := b.sendMaxDirectFormatted(ctx, item.DstChatID, item.Text, item.AttType, item.AttToken, item.ReplyTo, item.Format)
	if err != nil {
		errStr := err.Error()
		// Permanent errors — дропаем
		if strings.Contains(errStr, "403") || strings.Contains(errStr, "404") || strings.Contains(errStr, "chat.denied") {
			slog.Warn("queue item dropped (permanent error)", "id", item.ID, "err", errStr)
			b.repo.DeleteFromQueue(item.ID)
			return
		}
		slog.Warn("queue retry failed", "id", item.ID, "dir", "tg2max", "attempt", item.Attempts+1, "err", err)
		b.repo.IncrementAttempt(item.ID, now.Add(retryDelay(item.Attempts+1)).Unix())
		return
	}
	slog.Info("queue retry ok", "id", item.ID, "dir", "tg2max", "mid", mid)
	tgMsgID, _ := strconv.Atoi(item.SrcMsgID)
	if tgMsgID > 0 {
		b.repo.SaveMsg(item.SrcChatID, tgMsgID, item.DstChatID, mid)
	}
	b.repo.DeleteFromQueue(item.ID)
}

func (b *Bridge) processQueueMax2Tg(ctx context.Context, item QueueItem, now time.Time) {
	var sentMsgID int
	var err error

	threadID := b.repo.GetTgThreadID(item.DstChatID)

	if item.AttType != "" && item.AttURL != "" {
		opts := &SendOpts{Caption: item.Text, ParseMode: item.ParseMode, ThreadID: threadID}
		switch item.AttType {
		case "photo":
			sentMsgID, err = b.tg.SendPhoto(ctx, item.DstChatID, FileArg{URL: item.AttURL}, opts)
		case "video":
			sentMsgID, err = b.tg.SendVideo(ctx, item.DstChatID, FileArg{URL: item.AttURL}, opts)
		case "audio":
			sentMsgID, err = b.tg.SendAudio(ctx, item.DstChatID, FileArg{URL: item.AttURL}, opts)
		case "file":
			sentMsgID, err = b.tg.SendDocument(ctx, item.DstChatID, FileArg{URL: item.AttURL}, opts)
		default:
			sentMsgID, err = b.tg.SendPhoto(ctx, item.DstChatID, FileArg{URL: item.AttURL}, opts)
		}
	} else {
		sentMsgID, err = b.tg.SendMessage(ctx, item.DstChatID, item.Text, &SendOpts{ParseMode: item.ParseMode, ThreadID: threadID})
	}

	if err != nil {
		errStr := err.Error()
		// Топики выключены — сбрасываем и повторяем без thread_id
		if threadID != 0 && (strings.Contains(errStr, "message thread not found") ||
			strings.Contains(errStr, "TOPIC_NOT_FOUND") ||
			strings.Contains(errStr, "topics are disabled")) {
			slog.Info("queue: forum topics disabled, resetting thread_id", "tgChat", item.DstChatID)
			b.repo.SetTgThreadID(item.DstChatID, 0)
			b.repo.IncrementAttempt(item.ID, now.Unix()) // retry immediately
			return
		}
		if strings.Contains(errStr, "TOPIC_CLOSED") || strings.Contains(errStr, "403") || strings.Contains(errStr, "chat not found") {
			slog.Warn("queue item dropped (permanent error)", "id", item.ID, "dir", "max2tg", "err", errStr)
			b.repo.DeleteFromQueue(item.ID)
			return
		}
		slog.Warn("queue retry failed", "id", item.ID, "dir", "max2tg", "attempt", item.Attempts+1, "err", err)
		b.repo.IncrementAttempt(item.ID, now.Add(retryDelay(item.Attempts+1)).Unix())
		return
	}
	slog.Info("queue retry ok", "id", item.ID, "dir", "max2tg", "msgID", sentMsgID)
	b.repo.SaveMsg(item.DstChatID, sentMsgID, item.SrcChatID, item.SrcMsgID)
	b.repo.DeleteFromQueue(item.ID)
}
