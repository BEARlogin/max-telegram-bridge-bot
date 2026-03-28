package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	maxbot "github.com/max-messenger/max-bot-api-client-go"
	maxschemes "github.com/max-messenger/max-bot-api-client-go/schemes"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const mediaGroupTimeout = 1 * time.Second

// mediaGroupItem хранит данные одного сообщения из альбома TG.
type mediaGroupItem struct {
	photoSizes  []tgbotapi.PhotoSize
	videoFileID string // для видео в альбомах
	caption     string
	replyToMsg  *tgbotapi.Message
	entities    []tgbotapi.MessageEntity
	msg         *tgbotapi.Message
	maxChatID   int64 // если задан — используется напрямую (crosspost)
	crosspost   bool  // кросспостинг: без prefix, другой caption формат
}

// mediaGroupBuffer накапливает сообщения альбома перед отправкой.
type mediaGroupBuffer struct {
	mu    sync.Mutex
	items []mediaGroupItem
	timer *time.Timer
}

// bufferMediaGroup добавляет сообщение в буфер альбома.
// Если это первое сообщение — запускает таймер.
func (b *Bridge) bufferMediaGroup(ctx context.Context, groupID string, item mediaGroupItem) {
	b.mgMu.Lock()

	buf, ok := b.mgBuffers[groupID]
	if !ok {
		buf = &mediaGroupBuffer{}
		b.mgBuffers[groupID] = buf
		// Добавляем первый item до запуска таймера — исключает гонку
		buf.items = append(buf.items, item)
		buf.timer = time.AfterFunc(mediaGroupTimeout, func() {
			b.flushMediaGroup(ctx, groupID)
		})
		b.mgMu.Unlock()
		return
	}

	b.mgMu.Unlock()

	buf.mu.Lock()
	buf.items = append(buf.items, item)
	buf.mu.Unlock()
}

// flushMediaGroup отправляет все накопленные фото/видео альбома одним сообщением в MAX.
func (b *Bridge) flushMediaGroup(ctx context.Context, groupID string) {
	b.mgMu.Lock()
	buf, ok := b.mgBuffers[groupID]
	if !ok {
		b.mgMu.Unlock()
		return
	}
	delete(b.mgBuffers, groupID)
	b.mgMu.Unlock()

	buf.mu.Lock()
	buf.timer.Stop()
	items := buf.items
	buf.mu.Unlock()

	if len(items) == 0 {
		return
	}

	// Определяем maxChatID
	isCrosspost := items[0].crosspost
	maxChatID := items[0].maxChatID
	if maxChatID == 0 {
		var linked bool
		maxChatID, linked = b.repo.GetMaxChat(items[0].msg.Chat.ID)
		if !linked {
			slog.Warn("media group: chat not linked", "tgChat", items[0].msg.Chat.ID)
			return
		}
	}

	uid := tgUserID(items[0].msg)
	prefix := !isCrosspost && b.repo.HasPrefix("tg", items[0].msg.Chat.ID)

	// Caption и entities берём из первого элемента, у которого caption не пустой
	var caption string
	var entities []tgbotapi.MessageEntity
	for _, it := range items {
		if it.caption != "" {
			caption = it.caption
			entities = it.entities
			break
		}
	}

	// Reply ID из первого элемента с reply
	var replyTo string
	for _, it := range items {
		if it.replyToMsg != nil {
			if maxReplyID, ok := b.repo.LookupMaxMsgID(it.msg.Chat.ID, it.replyToMsg.MessageID); ok {
				replyTo = maxReplyID
			}
			break
		}
	}

	// Форматируем caption
	mdCaption := caption
	if entities != nil {
		mdCaption = tgEntitiesToMarkdown(caption, entities)
	}

	m := maxbot.NewMessage().SetChat(maxChatID).SetText(mdCaption)
	if replyTo != "" {
		m.SetReply(mdCaption, replyTo)
	}

	// Загружаем и добавляем все фото
	photosSent := 0
	for _, it := range items {
		if len(it.photoSizes) > 0 {
			photo := it.photoSizes[len(it.photoSizes)-1]
			fileURL, err := b.tgFileURL(photo.FileID)
			if err != nil {
				slog.Error("media group: tgFileURL failed", "err", err)
				continue
			}
			// Если custom TG API — MAX не может скачать по URL, скачиваем сами
			if b.cfg.TgAPIURL != "" {
				uploaded, err := b.uploadTgPhotoToMax(ctx, photo.FileID)
				if err != nil {
					slog.Error("media group: photo upload failed", "err", err)
					continue
				}
				m.AddPhoto(uploaded)
			} else {
				uploaded, err := b.maxApi.Uploads.UploadPhotoFromUrl(ctx, fileURL)
				if err != nil {
					slog.Error("media group: photo upload failed", "err", err)
					continue
				}
				m.AddPhoto(uploaded)
			}
			photosSent++
		}
	}

	// Загружаем видео из альбома через direct API
	videosSent := 0
	var videoTokens []string
	for _, it := range items {
		if it.videoFileID != "" {
			uploaded, err := b.uploadTgMediaToMax(ctx, it.videoFileID, maxschemes.VIDEO, "video.mp4")
			if err != nil {
				slog.Error("media group: video upload failed", "err", err)
				continue
			}
			videoTokens = append(videoTokens, uploaded.Token)
			videosSent++
		}
	}

	totalMedia := photosSent + videosSent
	if totalMedia == 0 {
		slog.Warn("media group: no media uploaded, skipping")
		return
	}

	slog.Info("TG→MAX sending media group", "photos", photosSent, "videos", videosSent, "uid", uid, "tgChat", items[0].msg.Chat.ID, "maxChat", maxChatID)

	// Если есть фото — отправляем через SDK (поддерживает AddPhoto)
	if photosSent > 0 {
		result, err := b.maxApi.Messages.SendWithResult(ctx, m)
		if err != nil {
			slog.Error("TG→MAX media group send failed", "err", err)
			if b.cbFail(maxChatID) {
				b.tgBot.Send(tgbotapi.NewMessage(items[0].msg.Chat.ID,
					fmt.Sprintf("Не удалось переслать альбом в MAX. Пересылка приостановлена на %d мин. Проверьте, что бот добавлен в MAX-чат и является админом.", int(cbCooldown.Minutes()))))
			}
			// Fallback — по одному
			for _, it := range items {
				var cap string
				if isCrosspost {
					cap = formatTgCrosspostCaption(it.msg)
				} else {
					cap = formatTgCaption(it.msg, prefix, b.cfg.MessageNewline)
				}
				go b.forwardTgToMax(ctx, it.msg, maxChatID, cap)
			}
			return
		}
		b.cbSuccess(maxChatID)
		slog.Info("TG→MAX media group sent", "mid", result.Body.Mid, "photos", photosSent)
		b.repo.SaveMsg(items[0].msg.Chat.ID, items[0].msg.MessageID, maxChatID, result.Body.Mid)
	}

	// Видео отправляем отдельно через direct API (SDK не поддерживает AddVideo)
	for i, token := range videoTokens {
		videoCaption := ""
		if i == 0 && photosSent == 0 {
			videoCaption = mdCaption // caption на первое видео если нет фото
		}
		mid, err := b.sendMaxDirectFormatted(ctx, maxChatID, videoCaption, "video", token, "", "")
		if err != nil {
			slog.Error("TG→MAX media group video send failed", "err", err)
			continue
		}
		if i == 0 && photosSent == 0 {
			b.repo.SaveMsg(items[0].msg.Chat.ID, items[0].msg.MessageID, maxChatID, mid)
		}
	}
}
