package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	maxbot "github.com/max-messenger/max-bot-api-client-go"
	maxschemes "github.com/max-messenger/max-bot-api-client-go/schemes"
)

const mediaGroupTimeout = 1 * time.Second

// mediaGroupItem хранит данные одного сообщения из альбома TG.
type mediaGroupItem struct {
	photoSizes     []PhotoSize
	videoFileID    string // для видео в альбомах
	documentFileID string // Telegram объединяет несколько документов в MediaGroupID
	documentName   string
	caption        string
	replyToMsg     *TGMessage
	entities       []Entity
	msg            *TGMessage
	maxChatID      int64 // если задан — используется напрямую (crosspost)
	crosspost      bool  // кросспостинг: без prefix, другой caption формат
}

// mediaGroupBuffer накапливает сообщения альбома перед отправкой.
type mediaGroupBuffer struct {
	mu          sync.Mutex
	items       []mediaGroupItem
	timer       *time.Timer
	manualFlush bool
}

// bufferMediaGroup добавляет сообщение в буфер альбома.
// Если это первое сообщение — запускает таймер.
func (b *Bridge) bufferMediaGroup(ctx context.Context, groupID string, item mediaGroupItem) {
	b.bufferMediaGroupMode(ctx, groupID, item, false)
}

// bufferMediaGroupManual накапливает заранее известный альбом без таймера.
// Вызывающий обязан завершить его через flushMediaGroup; это сохраняет порядок
// при пакетном переносе, где получение следующей части может занять больше секунды.
func (b *Bridge) bufferMediaGroupManual(ctx context.Context, groupID string, item mediaGroupItem) {
	b.bufferMediaGroupMode(ctx, groupID, item, true)
}

func (b *Bridge) bufferMediaGroupMode(ctx context.Context, groupID string, item mediaGroupItem, manual bool) {
	b.mgMu.Lock()

	buf, ok := b.mgBuffers[groupID]
	if !ok {
		buf = &mediaGroupBuffer{manualFlush: manual}
		b.mgBuffers[groupID] = buf
		// Добавляем первый item до запуска таймера — исключает гонку
		buf.items = append(buf.items, item)
		if !manual {
			buf.timer = time.AfterFunc(mediaGroupTimeout, func() {
				_, _ = b.flushMediaGroup(ctx, groupID)
			})
		}
		b.mgMu.Unlock()
		return
	}

	b.mgMu.Unlock()

	buf.mu.Lock()
	buf.items = append(buf.items, item)
	// Debounce: продлеваем окно сбора на каждый новый элемент. Если части
	// альбома приходят с задержкой (форвард+загрузка), и фиксированный таймер от
	// первого элемента флашил бы группу до прихода остальных, разбивая альбом.
	if buf.timer != nil {
		buf.timer.Reset(mediaGroupTimeout)
	}
	buf.mu.Unlock()
}

// flushMediaGroup отправляет все накопленные фото/видео альбома одним сообщением в MAX.
func (b *Bridge) flushMediaGroup(ctx context.Context, groupID string) (string, error) {
	b.mgMu.Lock()
	buf, ok := b.mgBuffers[groupID]
	if !ok {
		b.mgMu.Unlock()
		return "", fmt.Errorf("media group %q is not buffered", groupID)
	}
	delete(b.mgBuffers, groupID)
	b.mgMu.Unlock()

	buf.mu.Lock()
	if buf.timer != nil {
		buf.timer.Stop()
	}
	items := buf.items
	manualFlush := buf.manualFlush
	buf.mu.Unlock()

	if len(items) == 0 {
		return "", fmt.Errorf("media group %q is empty", groupID)
	}

	// Дедуп альбома: если первый элемент уже доставлен в MAX — пропускаем
	// (защита от повторной обработки/реплея после рестарта).
	if b.alreadyDeliveredToMax(items[0].msg.Chat.ID, items[0].msg.MessageID) {
		slog.Info("skip duplicate media group", "tgChat", items[0].msg.Chat.ID, "tgMsg", items[0].msg.MessageID)
		mid, _ := b.repo.LookupMaxMsgID(items[0].msg.Chat.ID, items[0].msg.MessageID)
		return mid, nil
	}

	// Определяем maxChatID
	isCrosspost := items[0].crosspost
	maxChatID := items[0].maxChatID
	if maxChatID == 0 {
		var linked bool
		maxChatID, linked = b.repo.GetMaxChat(items[0].msg.Chat.ID)
		if !linked {
			slog.Warn("media group: chat not linked", "tgChat", items[0].msg.Chat.ID)
			return "", fmt.Errorf("media group: chat is not linked")
		}
	}
	// Пауза связки — альбом тоже не пересылаем.
	if isCrosspost {
		if b.repo.CrosspostPaused(maxChatID) {
			return "", fmt.Errorf("crosspost to MAX chat %d is paused", maxChatID)
		}
	} else if b.repo.PairPaused(items[0].msg.Chat.ID, maxChatID) {
		return "", fmt.Errorf("bridge to MAX chat %d is paused", maxChatID)
	}
	// Дуал-бот: токен бота этого чата в ctx (аплоады + sendMaxDirect альбома идут им).
	ctx = b.withMaxToken(ctx, b.maxTokenFor(ctx, maxChatID))
	mc := b.maxClientFor(ctx, maxChatID) // SDK-клиент бота этого чата (дуал)

	uid := tgUserID(items[0].msg)
	prefix := !isCrosspost && b.hasPrefix("tg", items[0].msg.Chat.ID)

	// Опциональная кнопка мини-аппа под кросспостом-альбомом.
	var openApp *maxOpenApp
	if isCrosspost {
		openApp = b.crosspostOpenApp(ctx, items[0].msg.Chat.ID, items[0].msg.MessageID, maxChatID)
	}

	// Caption и entities берём из первого элемента, у которого caption не пустой
	var caption string
	var entities []Entity
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

	// Форматируем caption.
	// Для crosspost caption уже в markdown (см. formatTgCrosspostCaption),
	// повторно конвертировать нельзя — entities ссылаются на offsets сырого текста.
	mdCaption := caption
	if entities != nil && !isCrosspost {
		mdCaption = tgEntitiesToHTML(caption, entities)
	}

	m := maxbot.NewMessage().SetChat(maxChatID).SetText(mdCaption)
	// Для crosspost caption уже markdown; для bridge — markdown если были entities.
	if isCrosspost || mdCaption != caption {
		m.SetFormat("html")
	}
	if replyTo != "" {
		m.SetReply(mdCaption, replyTo)
	}
	if kb := b.openAppKeyboard(openApp); kb != nil {
		m.AddKeyboard(kb)
	}

	// Загружаем и добавляем все фото
	photosSent := 0
	var photoFailErr error
	for _, it := range items {
		if len(it.photoSizes) > 0 {
			photo := it.photoSizes[len(it.photoSizes)-1]
			fileURL, err := b.tgFileURL(ctx, photo.FileID)
			if err != nil {
				slog.Error("media group: tgFileURL failed", "err", err)
				photoFailErr = err
				continue
			}
			// Если custom TG API — MAX не может скачать по URL, скачиваем сами
			if b.cfg.TgAPIURL != "" {
				uploaded, err := b.uploadTgPhotoToMax(ctx, photo.FileID)
				if err != nil {
					slog.Error("media group: photo upload failed", "err", err)
					photoFailErr = err
					continue
				}
				m.AddPhoto(uploaded)
			} else {
				uploaded, err := mc.Uploads.UploadPhotoFromUrl(ctx, fileURL)
				if err != nil {
					slog.Error("media group: photo upload failed", "err", err)
					photoFailErr = err
					continue
				}
				m.AddPhoto(uploaded)
			}
			photosSent++
		}
	}
	if photoFailErr != nil && photosSent == 0 {
		b.notifyTgUser(ctx, items[0].msg, maxChatID,
			uploadErrMsg("Не удалось отправить альбом в MAX", photoFailErr), isCrosspost)
	}

	// Видео из альбома — добавляем в ТО ЖЕ сообщение (MAX принимает микс фото+видео в одном
	// посте). Раньше видео уходило отдельным сообщением → «видео пришло отдельно».
	videosSent := 0
	for _, it := range items {
		if it.videoFileID != "" {
			uploaded, err := b.uploadTgMediaToMax(ctx, it.videoFileID, maxschemes.VIDEO, "video.mp4")
			if err != nil {
				slog.Error("media group: video upload failed", "err", err)
				continue
			}
			m.AddVideo(uploaded)
			videosSent++
		}
	}

	filesSent := 0
	for _, it := range items {
		if it.documentFileID == "" {
			continue
		}
		name := it.documentName
		if name == "" {
			name = "document"
		}
		uploaded, err := b.uploadTgMediaToMax(ctx, it.documentFileID, maxschemes.FILE, name)
		if err != nil {
			slog.Error("media group: document upload failed", "err", err, "name", name)
			continue
		}
		m.AddFile(uploaded)
		filesSent++
	}

	totalMedia := photosSent + videosSent + filesSent
	if totalMedia == 0 {
		slog.Warn("media group: no media uploaded, skipping", "items", len(items))
		return "", fmt.Errorf("media group: no media uploaded")
	}

	slog.Info("TG→MAX sending media group", "photos", photosSent, "videos", videosSent, "files", filesSent, "uid", uid, "tgChat", items[0].msg.Chat.ID, "maxChat", maxChatID)

	// Фото + видео — ОДНИМ сообщением (m содержит и AddPhoto, и AddVideo). MAX принимает микс
	// вложений в одном посте, поэтому альбом не разваливается на «фото + отдельное видео».
	result, err := mc.Messages.SendWithResult(ctx, m)
	if err != nil {
		slog.Error("TG→MAX media group send failed", "err", err)
		if isChatUnavailable(err.Error()) {
			b.cbPermanentFail(ctx, maxChatID) // бот недоступен в чате → связка на паузу + DM
		} else if b.cbFail(maxChatID) {
			b.notifyTgUser(ctx, items[0].msg, maxChatID,
				fmt.Sprintf("Не удалось переслать альбом в MAX. Пересылка приостановлена на %d мин. Проверьте, что бот добавлен в MAX-чат и является админом.", int(cbCooldown.Minutes())), isCrosspost)
		}
		// Для живого потока сохраняем прежний фолбэк. Пакетный перенос должен
		// получить ошибку синхронно, иначе он отметит не подтверждённый альбом.
		for _, it := range items {
			if manualFlush {
				break
			}
			var cap string
			if isCrosspost {
				// Замены на уровне (текст+entities) до HTML, схлопывание — после.
				repl := b.repo.GetCrosspostReplacements(maxChatID)
				cap = formatTgCrosspostCaptionRepl(it.msg, repl.TgToMax)
				cap = collapseWhitespace(cap)
			} else {
				cap = formatTgCaption(it.msg, prefix, b.cfg.MessageNewline)
			}
			go b.forwardTgToMax(ctx, it.msg, maxChatID, cap, isCrosspost, false)
		}
		return "", err
	}
	b.cbSuccess(maxChatID)
	slog.Info("TG→MAX media group sent", "mid", result.Body.Mid, "photos", photosSent, "videos", videosSent, "files", filesSent)
	for _, it := range items {
		b.repo.SaveMsgOrigin(it.msg.Chat.ID, it.msg.MessageID, maxChatID, result.Body.Mid, it.msg.MessageThreadID, "tg")
	}
	return result.Body.Mid, nil
}
