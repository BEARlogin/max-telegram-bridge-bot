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

// humanQueueError переводит техническую ошибку доставки в MAX в понятную причину
// для пользователя.
func humanQueueError(errStr string) string {
	switch {
	case strings.Contains(errStr, "send-message.empty"):
		return "пустой пост — нет текста, а медиа не прикрепилось (вероятно, видео или файл больше лимита MAX)"
	case strings.Contains(errStr, "not enough rights"), strings.Contains(errStr, "chat.denied"),
		strings.Contains(errStr, "403"):
		return "у бота нет прав публиковать в MAX-чат (сделайте бота администратором канала в MAX)"
	case strings.Contains(errStr, "must be at most"), strings.Contains(errStr, "too big"):
		return "файл больше максимального размера, разрешённого в MAX"
	case strings.Contains(errStr, "attachment not ready"):
		return "MAX не успел обработать вложение"
	case strings.Contains(errStr, "404"):
		return "MAX-чат не найден (связка устарела?)"
	case strings.Contains(errStr, "service.unavailable"), strings.Contains(errStr, "503"):
		return "MAX временно недоступен"
	case errStr == "":
		return "превышено число попыток доставки"
	default:
		return errStr
	}
}

// notifyTg2MaxFailure сообщает о невозможности доставить пост в MAX: владельцу
// связки (для crosspost) с номером поста и причиной, иначе — в исходный чат.
func (b *Bridge) notifyTg2MaxFailure(ctx context.Context, item QueueItem, reason string) {
	post := ""
	if item.SrcMsgID != "" && item.SrcMsgID != "0" {
		post = fmt.Sprintf(" (пост #%s)", item.SrcMsgID)
	}
	text := fmt.Sprintf("⚠️ Не удалось перенести сообщение в MAX%s.\nПричина: %s.", post, reason)
	if _, _, isCp := b.repo.GetCrosspostMaxChat(item.SrcChatID); isCp {
		_, tgOwner := b.repo.GetCrosspostOwner(item.DstChatID)
		if tgOwner != 0 {
			b.tg.SendMessage(ctx, tgOwner, text, nil)
		} else {
			slog.Warn("queue fail notify skipped: no tg owner", "srcChat", item.SrcChatID, "maxChat", item.DstChatID)
		}
	} else {
		b.tg.SendMessage(ctx, item.SrcChatID, text, nil)
	}
}

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

// hasPendingForChat возвращает true если в очереди уже есть сообщения для данного dst-чата.
// В этом случае новое сообщение тоже нужно ставить в очередь, чтобы не нарушить порядок.
func (b *Bridge) hasPendingForChat(direction string, dstChatID int64) bool {
	return b.repo.HasPendingQueue(direction, dstChatID)
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
				b.notifyTg2MaxFailure(ctx, item, "MAX долго не принимал сообщение (превышено число попыток)")
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
		// Permanent errors — дропаем сразу (бессмысленно ретраить) и объясняем владельцу
		// причину с номером поста, чтобы было понятно что и почему не перенеслось.
		if strings.Contains(errStr, "403") || strings.Contains(errStr, "404") ||
			strings.Contains(errStr, "chat.denied") ||
			strings.Contains(errStr, "attachment not ready after") ||
			strings.Contains(errStr, "must be at most") ||
			strings.Contains(errStr, "send-message.empty") ||
			strings.Contains(errStr, "not enough rights") {
			slog.Warn("queue item dropped (permanent error)", "id", item.ID, "err", errStr)
			b.repo.DeleteFromQueue(item.ID)
			b.notifyTg2MaxFailure(ctx, item, humanQueueError(errStr))
			return
		}
		slog.Warn("queue retry failed", "id", item.ID, "dir", "tg2max", "attempt", item.Attempts+1, "err", err)
		b.repo.IncrementAttempt(item.ID, now.Add(retryDelay(item.Attempts+1)).Unix())
		return
	}
	slog.Info("queue retry ok", "id", item.ID, "dir", "tg2max", "mid", mid)
	tgMsgID, _ := strconv.Atoi(item.SrcMsgID)
	if tgMsgID > 0 {
		// Тред исходного TG-сообщения в очереди не сохраняется — реплаи
		// на такие сообщения из MAX будут уходить в тред по умолчанию.
		b.repo.SaveMsg(item.SrcChatID, tgMsgID, item.DstChatID, mid, 0)
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
		if strings.Contains(errStr, "TOPIC_CLOSED") || strings.Contains(errStr, "403") || strings.Contains(errStr, "chat not found") ||
			strings.Contains(errStr, "can't parse entities") ||
			strings.Contains(errStr, "caption is too long") ||
			strings.Contains(errStr, "message is too long") ||
			strings.Contains(errStr, "MESSAGE_TOO_LONG") {
			slog.Warn("queue item dropped (permanent error)", "id", item.ID, "dir", "max2tg", "err", errStr)
			b.repo.DeleteFromQueue(item.ID)
			return
		}
		slog.Warn("queue retry failed", "id", item.ID, "dir", "max2tg", "attempt", item.Attempts+1, "err", err)
		b.repo.IncrementAttempt(item.ID, now.Add(retryDelay(item.Attempts+1)).Unix())
		return
	}
	slog.Info("queue retry ok", "id", item.ID, "dir", "max2tg", "msgID", sentMsgID)
	b.repo.SaveMsg(item.DstChatID, sentMsgID, item.SrcChatID, item.SrcMsgID, threadID)
	b.repo.DeleteFromQueue(item.ID)
}
