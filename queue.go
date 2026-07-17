package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

const (
	queueMaxAttempts = 30             // максимум попыток
	queueMaxAge      = 24 * time.Hour // дропаем сообщения старше 24 часов
	queueBatchSize   = 10
	queueItemTimeout = 45 * time.Second
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
	// Глобальный бан аккаунта MAX — молчим (бан общий, уведомлять каждого бессмысленно).
	if b.maxAccountBlocked() {
		slog.Debug("queue fail notify suppressed: MAX account blocked", "srcChat", item.SrcChatID)
		return
	}
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

// enqueueMax2TgAlbum ставит в очередь ВЕСЬ альбом MAX→TG: список {type,url} в JSON,
// AttType="album". Иначе при доставке из очереди уходило только первое фото.
func (b *Bridge) enqueueMax2TgAlbum(maxChatID, tgChatID int64, maxMid, caption string, items []maxAlbumItem, parseMode string) {
	data, err := json.Marshal(items)
	if err != nil {
		slog.Error("album enqueue marshal failed", "err", err)
		return
	}
	b.enqueueMax2Tg(maxChatID, tgChatID, maxMid, caption, "album", string(data), parseMode)
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

		itemCtx, cancel := context.WithTimeout(ctx, queueItemTimeout)
		switch item.Direction {
		case "tg2max":
			b.processQueueTg2Max(itemCtx, item, now)
		case "max2tg":
			b.processQueueMax2Tg(itemCtx, item, now)
		}
		cancel()
	}
}

func (b *Bridge) processQueueTg2Max(ctx context.Context, item QueueItem, now time.Time) {
	// Связка на паузе — НЕ доставляем из очереди (оставляем item, заберём после /unpause).
	if maxChatID, _, ok := b.repo.GetCrosspostMaxChat(item.SrcChatID); ok && maxChatID == item.DstChatID {
		if b.repo.CrosspostPaused(item.DstChatID) {
			return
		}
	} else {
		if b.repo.PairPaused(item.SrcChatID, item.DstChatID) {
			return
		}
		if !b.pairDirectionAllows(ctx, item.SrcChatID, item.DstChatID, "tg>max") {
			slog.Info("queue: drop tg2max blocked by pair direction", "id", item.ID, "srcChat", item.SrcChatID, "dstChat", item.DstChatID)
			b.repo.DeleteFromQueue(item.ID)
			return
		}
	}
	// Дедуп: если это сообщение уже доставлено (например, прямая отправка прошла,
	// но бот упал до удаления из очереди) — не отправляем повторно.
	if tgMsgID, _ := strconv.Atoi(item.SrcMsgID); b.alreadyDeliveredToMax(item.SrcChatID, tgMsgID) {
		slog.Info("queue: skip already-delivered tg2max", "id", item.ID, "srcChat", item.SrcChatID, "srcMsg", item.SrcMsgID)
		b.repo.DeleteFromQueue(item.ID)
		return
	}
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
		// Кросспост канала (а не зеркало bridge-группы) — уведомляем аддон.
	}
	b.repo.DeleteFromQueue(item.ID)
}

func (b *Bridge) processQueueMax2Tg(ctx context.Context, item QueueItem, now time.Time) {
	// Связка на паузе — НЕ доставляем из очереди (src=MAX-чат, dst=TG-чат).
	if tgChatID, _, ok := b.repo.GetCrosspostTgChat(item.SrcChatID); ok && tgChatID == item.DstChatID {
		if b.repo.CrosspostPaused(item.SrcChatID) {
			return
		}
	} else {
		if b.repo.PairPaused(item.DstChatID, item.SrcChatID) {
			return
		}
		if !b.pairDirectionAllows(ctx, item.DstChatID, item.SrcChatID, "max>tg") {
			slog.Info("queue: drop max2tg blocked by pair direction", "id", item.ID, "srcChat", item.SrcChatID, "dstChat", item.DstChatID)
			b.repo.DeleteFromQueue(item.ID)
			return
		}
	}
	// Дедуп: сообщение уже доставлено В ЭТОТ TG-чат (напр. рестарт до удаления из
	// очереди) — не шлём повторно. Пер-чат, чтобы фан-аут в несколько TG-групп
	// (несколько TG → одна MAX) не глушился глобально по mid.
	if item.SrcMsgID != "" {
		if b.repo.MaxMsgDeliveredTo(item.SrcMsgID, item.DstChatID) {
			slog.Info("queue: skip already-delivered max2tg", "id", item.ID, "maxMid", item.SrcMsgID)
			b.repo.DeleteFromQueue(item.ID)
			return
		}
	}
	var sentMsgID int
	var err error

	threadID := b.repo.GetTgThreadID(item.DstChatID)

	if item.AttType == "album" {
		// Альбом: восстанавливаем список {type,url} и шлём media group целиком.
		var items []maxAlbumItem
		if json.Unmarshal([]byte(item.AttURL), &items) == nil && len(items) > 0 {
			ids, e := b.sendMaxAlbumToTg(ctx, item.DstChatID, items, item.Text, item.ParseMode, threadID, 0)
			err = e
			if e == nil && len(ids) > 0 {
				sentMsgID = ids[0]
			}
		} else {
			slog.Warn("queue: bad album payload, dropping", "id", item.ID)
			b.repo.DeleteFromQueue(item.ID)
			return
		}
	} else if item.AttType != "" && item.AttURL != "" {
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
