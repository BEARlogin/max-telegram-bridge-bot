package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	maxschemes "github.com/max-messenger/max-bot-api-client-go/schemes"
)

const (
	queueMaxAttempts              = 30             // максимум попыток
	queueMaxAge                   = 24 * time.Hour // дропаем сообщения старше 24 часов
	queueBatchSize                = 256
	queueMaxInFlight              = 12
	queueMaxMediaInFlight         = 4 // оставляем не менее 8 слотов для обычных сообщений
	queueItemTimeout              = 45 * time.Second
	queueMediaTimeout             = 5 * time.Minute
	queueMediaFallbackAfter       = 10
	queueVideoRefreshAfter        = 2
	queueVideoFallbackAfter       = 4
	queueLegacyMediaFallbackAfter = 4
)

func queueTimeout(item QueueItem) time.Duration {
	if item.AttType != "" {
		return queueMediaTimeout
	}
	return queueItemTimeout
}

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

func telegramRetryAfter(errStr string) (time.Duration, bool) {
	const marker = "retry_after "
	pos := strings.LastIndex(strings.ToLower(errStr), marker)
	if pos < 0 {
		return 0, false
	}
	value := errStr[pos+len(marker):]
	end := 0
	for end < len(value) && value[end] >= '0' && value[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, false
	}
	seconds, err := strconv.Atoi(value[:end])
	if err != nil || seconds <= 0 {
		return 0, false
	}
	return time.Duration(seconds)*time.Second + time.Second, true
}

// hasPendingForChat возвращает true если в очереди уже есть сообщения для данного dst-чата.
// В этом случае новое сообщение тоже нужно ставить в очередь, чтобы не нарушить порядок.
func (b *Bridge) hasPendingForChat(direction string, dstChatID int64) bool {
	return b.repo.HasPendingQueue(direction, dstChatID)
}

// enqueueTg2Max ставит сообщение TG→MAX в очередь.
type tgQueueMediaSource struct {
	FileID     string `json:"file_id"`
	FileName   string `json:"file_name"`
	UploadType string `json:"upload_type"`
}

func encodeTgQueueMediaSource(fileID, fileName string, uploadType maxschemes.UploadType) string {
	if fileID == "" || uploadType == "" {
		return ""
	}
	data, err := json.Marshal(tgQueueMediaSource{
		FileID: fileID, FileName: fileName, UploadType: string(uploadType),
	})
	if err != nil {
		return ""
	}
	return string(data)
}

func decodeTgQueueMediaSource(raw string) (tgQueueMediaSource, bool) {
	var source tgQueueMediaSource
	if raw == "" || json.Unmarshal([]byte(raw), &source) != nil ||
		source.FileID == "" || source.UploadType == "" {
		return tgQueueMediaSource{}, false
	}
	return source, true
}

func (b *Bridge) enqueueTg2Max(tgChatID int64, tgMsgID int, maxChatID int64, text, attType, attToken, attSource, replyTo, format string) {
	now := time.Now().Unix()
	item := &QueueItem{
		Direction: "tg2max",
		SrcChatID: tgChatID,
		DstChatID: maxChatID,
		SrcMsgID:  strconv.Itoa(tgMsgID),
		Text:      text,
		AttType:   attType,
		AttToken:  attToken,
		AttURL:    attSource,
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

func queueDestinationKey(item QueueItem) string {
	return item.Direction + ":" + strconv.FormatInt(item.DstChatID, 10)
}

func (b *Bridge) claimQueueItem(item QueueItem) bool {
	key := queueDestinationKey(item)
	b.queueMu.Lock()
	defer b.queueMu.Unlock()
	if b.queueInFlight == nil {
		b.queueInFlight = make(map[int64]struct{})
	}
	if b.queueDestInFlight == nil {
		b.queueDestInFlight = make(map[string]struct{})
	}
	if b.queueMediaInFlight == nil {
		b.queueMediaInFlight = make(map[int64]struct{})
	}
	if len(b.queueInFlight) >= queueMaxInFlight {
		return false
	}
	if item.AttType != "" && len(b.queueMediaInFlight) >= queueMaxMediaInFlight {
		return false
	}
	if _, exists := b.queueInFlight[item.ID]; exists {
		return false
	}
	if _, exists := b.queueDestInFlight[key]; exists {
		return false
	}
	b.queueInFlight[item.ID] = struct{}{}
	b.queueDestInFlight[key] = struct{}{}
	if item.AttType != "" {
		b.queueMediaInFlight[item.ID] = struct{}{}
	}
	return true
}

func (b *Bridge) releaseQueueItem(item QueueItem) {
	key := queueDestinationKey(item)
	b.queueMu.Lock()
	delete(b.queueInFlight, item.ID)
	delete(b.queueDestInFlight, key)
	delete(b.queueMediaInFlight, item.ID)
	b.queueMu.Unlock()
}

// processQueue запускает ограниченное число независимых доставок. PeekQueue
// возвращает только головной элемент каждого destination, поэтому внутри одного
// чата порядок сохраняется, а медиа другого чата больше не блокирует очередь.
func (b *Bridge) processQueue(ctx context.Context) {
	items, err := b.repo.PeekQueue(queueBatchSize)
	if err != nil {
		slog.Error("peek queue failed", "err", err)
		return
	}
	for _, item := range items {
		if !b.claimQueueItem(item) {
			continue
		}
		go func(item QueueItem) {
			defer b.releaseQueueItem(item)
			b.processQueueItem(ctx, item)
		}(item)
	}
}

func (b *Bridge) processQueueItem(ctx context.Context, item QueueItem) {
	now := time.Now()
	// Слишком старое или слишком много попыток — дропаем
	age := now.Sub(time.Unix(item.CreatedAt, 0))
	if item.Attempts >= queueMaxAttempts || age > queueMaxAge {
		slog.Warn("queue item expired", "id", item.ID, "dir", item.Direction, "attempts", item.Attempts, "age", age)
		b.repo.DeleteFromQueue(item.ID)
		if item.Direction == "tg2max" {
			b.notifyTg2MaxFailure(ctx, item, "MAX долго не принимал сообщение (превышено число попыток)")
		}
		return
	}

	itemCtx, cancel := context.WithTimeout(ctx, queueTimeout(item))
	defer cancel()
	switch item.Direction {
	case "tg2max":
		b.processQueueTg2Max(itemCtx, item, now)
	case "max2tg":
		b.processQueueMax2Tg(itemCtx, item, now)
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

	// Видео MAX иногда навсегда остаётся attachment.not.ready. Для новых элементов
	// очереди сохраняем Telegram file_id и после двух неудач выдаём MAX новый upload
	// token. Старые элементы без file_id безопасно деградируют до текста/подписи,
	// только если MAX снова подтвердил именно ошибку обработки вложения.
	if item.AttType != "" && item.Attempts >= queueLegacyMediaFallbackAfter {
		if _, recoverable := decodeTgQueueMediaSource(item.AttURL); !recoverable {
			b.deliverTg2MaxMediaFallback(ctx, item, "старое медиа не принято MAX; повторная загрузка для этой записи недоступна")
			return
		}
	}
	if item.AttType == "video" && item.Attempts >= queueVideoRefreshAfter {
		if source, ok := decodeTgQueueMediaSource(item.AttURL); ok {
			uploaded, uploadErr := b.uploadTgMediaToMax(
				ctx,
				source.FileID,
				maxschemes.UploadType(source.UploadType),
				source.FileName,
			)
			if uploadErr != nil {
				slog.Warn("queue video refresh failed", "id", item.ID, "attempt", item.Attempts+1, "err", uploadErr)
				b.repo.IncrementAttempt(item.ID, now.Add(retryDelay(item.Attempts+1)).Unix())
				return
			}
			item.AttToken = uploaded.Token
			slog.Info("queue video token refreshed", "id", item.ID, "attempt", item.Attempts+1)
		}
	}

	mid, err := b.sendMaxDirectFormatted(ctx, item.DstChatID, item.Text, item.AttType, item.AttToken, item.ReplyTo, item.Format)
	if err != nil {
		errStr := err.Error()
		if item.AttType == "video" && strings.Contains(errStr, "attachment not ready after") {
			if _, recoverable := decodeTgQueueMediaSource(item.AttURL); recoverable &&
				item.Attempts+1 < queueVideoFallbackAfter {
				slog.Warn("queue video not ready; retrying with fresh upload", "id", item.ID, "attempt", item.Attempts+1)
				b.repo.IncrementAttempt(item.ID, now.Add(retryDelay(item.Attempts+1)).Unix())
			} else {
				b.deliverTg2MaxMediaFallback(ctx, item, "MAX не смог обработать видео")
			}
			return
		}
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
		b.repo.SaveMsgOrigin(item.SrcChatID, tgMsgID, item.DstChatID, mid, 0, "tg")
		// Кросспост канала (а не зеркало bridge-группы) — уведомляем аддон.
	}
	b.repo.DeleteFromQueue(item.ID)
}

// deliverTg2MaxMediaFallback не даёт сломанному медиа навсегда остановить свой чат.
// Подпись/текст доставляются без изменения, а владелец получает отдельное уведомление.
func (b *Bridge) deliverTg2MaxMediaFallback(ctx context.Context, item QueueItem, reason string) {
	if item.Text == "" {
		b.repo.DeleteFromQueue(item.ID)
		b.notifyTg2MaxFailure(ctx, item, reason)
		return
	}
	mid, err := b.sendMaxDirectFormatted(ctx, item.DstChatID, item.Text, "", "", item.ReplyTo, item.Format)
	if err != nil {
		slog.Warn("queue media text fallback failed", "id", item.ID, "err", err)
		b.repo.IncrementAttempt(item.ID, time.Now().Add(retryDelay(item.Attempts+1)).Unix())
		return
	}
	tgMsgID, _ := strconv.Atoi(item.SrcMsgID)
	if tgMsgID > 0 {
		b.repo.SaveMsgOrigin(item.SrcChatID, tgMsgID, item.DstChatID, mid, 0, "tg")
	}
	b.repo.DeleteFromQueue(item.ID)
	b.notifyTg2MaxFailure(ctx, item, reason+"; текст сообщения доставлен без видео")
	slog.Info("queue media degraded to text", "id", item.ID, "type", item.AttType, "mid", mid)
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
	var sentMsgIDs []int
	var richMediaSent bool
	var err error

	threadID := b.repo.GetTgThreadID(item.DstChatID)

	// Один повреждённый/неподдерживаемый файл не должен навсегда останавливать
	// все следующие сообщения своего чата. После достаточного числа попыток
	// сохраняем хотя бы подпись и освобождаем голову очереди.
	if item.AttType != "" && item.Attempts >= queueMediaFallbackAfter {
		b.deliverMax2TgTextFallback(ctx, item, threadID)
		return
	}

	if item.AttType == "album" {
		// Альбом: сохранённый JSON нужен для совместимости, но CDN URL в нём
		// короткоживущие. Перед каждой попыткой перечитываем исходное MAX-сообщение.
		var items []maxAlbumItem
		if json.Unmarshal([]byte(item.AttURL), &items) == nil && len(items) > 0 {
			items, err = b.refreshQueuedMaxAlbumItems(ctx, item.SrcChatID, item.SrcMsgID)
			if err == nil {
				ids, e := b.sendMaxAlbumToTg(ctx, item.DstChatID, items, item.Text, item.ParseMode, threadID, 0)
				err = e
				if e == nil && len(ids) > 0 {
					sentMsgIDs = ids
					sentMsgID = ids[0]
				}
			}
		} else {
			slog.Warn("queue: bad album payload, dropping", "id", item.ID)
			b.repo.DeleteFromQueue(item.ID)
			return
		}
	} else if item.AttType != "" && item.AttURL != "" {
		mediaURL := item.AttURL
		if item.AttType == "video" {
			mediaURL, err = b.refreshQueuedMaxVideoURL(ctx, item.SrcChatID, item.SrcMsgID)
		}
		if err == nil {
			// Тот же путь, что у прямой доставки: длинный текст + одиночное
			// фото/видео уходит одним Rich Message, с совместимым fallback.
			var ids []int
			ids, richMediaSent, err = b.sendMaxSingleMediaToTg(ctx, item.DstChatID, mediaURL, item.AttType,
				item.Text, item.ParseMode, 0, threadID)
			if len(ids) > 0 {
				sentMsgIDs = ids
				sentMsgID = ids[0]
			}
		}
	} else {
		sentMsgID, err = b.tg.SendMessage(ctx, item.DstChatID, item.Text, &SendOpts{ParseMode: item.ParseMode, ThreadID: threadID})
	}

	if err != nil {
		errStr := err.Error()
		if delay, ok := telegramRetryAfter(errStr); ok {
			slog.Warn("queue Telegram rate limited", "id", item.ID, "retryIn", delay)
			b.repo.IncrementAttempt(item.ID, now.Add(delay).Unix())
			return
		}
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
			strings.Contains(errStr, "MESSAGE_TOO_LONG") ||
			strings.Contains(errStr, "message text is empty") ||
			strings.Contains(errStr, "refresh MAX video: API error 404") {
			slog.Warn("queue item dropped (permanent error)", "id", item.ID, "dir", "max2tg", "err", errStr)
			b.repo.DeleteFromQueue(item.ID)
			return
		}
		slog.Warn("queue retry failed", "id", item.ID, "dir", "max2tg", "attempt", item.Attempts+1, "err", err)
		b.repo.IncrementAttempt(item.ID, now.Add(retryDelay(item.Attempts+1)).Unix())
		return
	}
	slog.Info("queue retry ok", "id", item.ID, "dir", "max2tg", "msgID", sentMsgID)
	if len(sentMsgIDs) == 0 && sentMsgID != 0 {
		sentMsgIDs = []int{sentMsgID}
	}
	for _, msgID := range sentMsgIDs {
		b.repo.SaveMsgOrigin(item.DstChatID, msgID, item.SrcChatID, item.SrcMsgID, threadID, "max")
	}
	if richMediaSent && sentMsgID != 0 {
		b.repo.SaveTgMediaState(item.DstChatID, TgMediaState{
			TgMsgID:     sentMsgID,
			Kind:        "rich_" + item.AttType,
			FileID:      item.AttURL,
			Fingerprint: "rich:" + item.SrcMsgID,
		})
	}
	b.repo.DeleteFromQueue(item.ID)
}

func (b *Bridge) deliverMax2TgTextFallback(ctx context.Context, item QueueItem, threadID int) {
	if item.Text == "" {
		slog.Warn("queue MAX media dropped after retries", "id", item.ID, "type", item.AttType, "attempts", item.Attempts)
		b.repo.DeleteFromQueue(item.ID)
		return
	}
	sentMsgID, err := b.tg.SendMessage(ctx, item.DstChatID, item.Text, &SendOpts{
		ParseMode: item.ParseMode,
		ThreadID:  threadID,
	})
	if err != nil {
		slog.Warn("queue MAX media text fallback failed", "id", item.ID, "err", err)
		b.repo.IncrementAttempt(item.ID, time.Now().Add(retryDelay(item.Attempts+1)).Unix())
		return
	}
	b.repo.SaveMsgOrigin(item.DstChatID, sentMsgID, item.SrcChatID, item.SrcMsgID, threadID, "max")
	b.repo.DeleteFromQueue(item.ID)
	slog.Info("queue MAX media degraded to text", "id", item.ID, "type", item.AttType, "msgID", sentMsgID)
}
