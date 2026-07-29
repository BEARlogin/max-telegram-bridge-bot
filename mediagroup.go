package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	maxbot "github.com/max-messenger/max-bot-api-client-go"
	maxschemes "github.com/max-messenger/max-bot-api-client-go/schemes"
)

const mediaGroupTimeout = 1 * time.Second

var mediaGroupCaptionVerifyDelays = []time.Duration{2 * time.Second, 6 * time.Second}

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
	items       []mediaGroupItem
	timer       *time.Timer
	manualFlush bool
	generation  uint64
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
	// Один Telegram-канал может публиковаться сразу в несколько MAX-каналов.
	// У всех адресатов элементы одного альбома имеют одинаковый MediaGroupID,
	// поэтому для живого кросспоста буферы должны быть раздельными per-destination.
	// Ручной импорт оставляем на исходном ключе: его явно завершает FlushMediaGroup.
	bufferID := groupID
	if item.crosspost && !manual && item.msg != nil {
		bufferID = fmt.Sprintf("crosspost:%d:%d:%s", item.msg.Chat.ID, item.maxChatID, groupID)
	}

	b.mgMu.Lock()
	defer b.mgMu.Unlock()
	buf, ok := b.mgBuffers[bufferID]
	if !ok {
		buf = &mediaGroupBuffer{manualFlush: manual}
		b.mgBuffers[bufferID] = buf
	}

	buf.items = append(buf.items, item)
	buf.generation++
	if buf.manualFlush {
		return
	}

	// Каждый элемент получает новый timer generation. Stop/Reset недостаточно:
	// callback старого таймера уже может ждать mgMu и после разблокировки успеть
	// отсоединить буфер до поздней части с caption. Устаревший callback сверяет
	// generation и ничего не отправляет.
	if buf.timer != nil {
		buf.timer.Stop()
	}
	generation := buf.generation
	buf.timer = time.AfterFunc(mediaGroupTimeout, func() {
		_, _ = b.flushMediaGroupGeneration(ctx, bufferID, generation)
	})
}

// flushMediaGroup отправляет все накопленные фото/видео альбома одним сообщением в MAX.
func (b *Bridge) flushMediaGroup(ctx context.Context, groupID string) (string, error) {
	return b.flushMediaGroupGeneration(ctx, groupID, 0)
}

// detachMediaGroup атомарно отсоединяет готовый буфер. expectedGeneration=0
// означает явный flush; timer flush проходит только для актуального поколения.
func (b *Bridge) detachMediaGroup(groupID string, expectedGeneration uint64) (*mediaGroupBuffer, bool, error) {
	b.mgMu.Lock()
	defer b.mgMu.Unlock()
	buf, ok := b.mgBuffers[groupID]
	if !ok {
		return nil, false, fmt.Errorf("media group %q is not buffered", groupID)
	}
	if expectedGeneration != 0 && buf.generation != expectedGeneration {
		return nil, false, nil
	}
	delete(b.mgBuffers, groupID)
	if buf.timer != nil {
		buf.timer.Stop()
	}
	return buf, true, nil
}

func (b *Bridge) flushMediaGroupGeneration(ctx context.Context, groupID string, expectedGeneration uint64) (string, error) {
	buf, detached, err := b.detachMediaGroup(groupID, expectedGeneration)
	if err != nil {
		return "", err
	}
	if !detached {
		return "", nil
	}
	items := buf.items
	manualFlush := buf.manualFlush
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
	} else {
		if !b.pairDeliverable(ctx, items[0].msg.Chat.ID, maxChatID) {
			return "", nil
		}
		if b.repo.PairPaused(items[0].msg.Chat.ID, maxChatID) {
			return "", fmt.Errorf("bridge to MAX chat %d is paused", maxChatID)
		}
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

	// MAX иногда подтверждает создание медиаальбома, но сохраняет его без текста.
	// Проверяем созданный объект и восстанавливаем только text/format через PUT:
	// attachments при этом не передаются и уже опубликованное медиа не затирается.
	captionRepaired := false
	if strings.TrimSpace(mdCaption) != "" {
		persisted, getErr := mc.Messages.GetMessage(ctx, result.Body.Mid)
		if getErr != nil {
			slog.Warn("TG→MAX media group caption verification failed",
				"err", getErr, "mid", result.Body.Mid, "captionBytes", len(mdCaption))
		}
		if maxAlbumCaptionMissing(mdCaption, result, persisted, getErr) {
			format := ""
			if isCrosspost || mdCaption != caption {
				format = "html"
			}
			if editErr := b.editMaxTextOnly(ctx, maxChatID, result.Body.Mid, mdCaption, format); editErr != nil {
				slog.Error("TG→MAX media group caption repair failed",
					"err", editErr, "mid", result.Body.Mid, "captionBytes", len(mdCaption))
				b.notifyTgUser(ctx, items[0].msg, maxChatID,
					"Альбом отправлен в MAX, но подпись не сохранилась. Повторите отправку текста отдельным сообщением.", isCrosspost)
			} else {
				captionRepaired = true
				slog.Info("TG→MAX media group caption repaired",
					"mid", result.Body.Mid, "captionBytes", len(mdCaption))
			}
		}

		// Первый GET иногда возвращает текст из свежего ответа, после чего MAX
		// заканчивает обработку вложений и сохраняет альбом уже без подписи.
		// Повторно проверяем объект после стабилизации и при необходимости
		// редактируем только text/format, не затрагивая attachments.
		format := ""
		if isCrosspost || mdCaption != caption {
			format = "html"
		}
		go b.verifyMaxAlbumCaptionEventually(ctx, mc, maxChatID, result.Body.Mid, mdCaption, format)
	}

	slog.Info("TG→MAX media group sent",
		"mid", result.Body.Mid,
		"photos", photosSent,
		"videos", videosSent,
		"files", filesSent,
		"captionBytes", len(mdCaption),
		"captionRepaired", captionRepaired)
	for _, it := range items {
		b.repo.SaveMsgOrigin(it.msg.Chat.ID, it.msg.MessageID, maxChatID, result.Body.Mid, it.msg.MessageThreadID, "tg")
		b.saveTgMediaState(it.msg)
	}
	return result.Body.Mid, nil
}

func (b *Bridge) verifyMaxAlbumCaptionEventually(
	ctx context.Context,
	mc *maxbot.Api,
	maxChatID int64,
	maxMsgID, caption, format string,
) {
	verifyCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()

	repaired, err := verifyCaptionAfterDelays(
		verifyCtx,
		mediaGroupCaptionVerifyDelays,
		func(fetchCtx context.Context) (string, error) {
			persisted, fetchErr := mc.Messages.GetMessage(fetchCtx, maxMsgID)
			if fetchErr != nil {
				return "", fetchErr
			}
			if persisted == nil {
				return "", fmt.Errorf("MAX returned an empty message")
			}
			return persisted.Body.Text, nil
		},
		func(repairCtx context.Context) error {
			return b.editMaxTextOnly(repairCtx, maxChatID, maxMsgID, caption, format)
		},
	)
	if err != nil {
		slog.Warn("TG→MAX delayed media group caption verification failed",
			"err", err, "mid", maxMsgID, "captionBytes", len(caption))
		return
	}
	if repaired {
		slog.Info("TG→MAX delayed media group caption repaired",
			"mid", maxMsgID, "captionBytes", len(caption))
	}
}

func verifyCaptionAfterDelays(
	ctx context.Context,
	delays []time.Duration,
	fetch func(context.Context) (string, error),
	repair func(context.Context) error,
) (bool, error) {
	repaired := false
	var lastErr error
	for _, delay := range delays {
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return repaired, ctx.Err()
		case <-timer.C:
		}

		text, err := fetch(ctx)
		if err != nil {
			lastErr = err
			continue
		}
		lastErr = nil
		if strings.TrimSpace(text) != "" {
			continue
		}
		if err := repair(ctx); err != nil {
			lastErr = err
			continue
		}
		repaired = true
	}
	return repaired, lastErr
}

func maxAlbumCaptionMissing(expected string, sent, persisted *maxschemes.Message, fetchErr error) bool {
	if strings.TrimSpace(expected) == "" {
		return false
	}
	if fetchErr == nil && persisted != nil {
		return strings.TrimSpace(persisted.Body.Text) == ""
	}
	return sent == nil || strings.TrimSpace(sent.Body.Text) == ""
}
