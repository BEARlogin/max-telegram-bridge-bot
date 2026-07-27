package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"time"

	maxschemes "github.com/max-messenger/max-bot-api-client-go/schemes"
)

// mediaMaxBytes — потолок размера перезаливаемого медиа. Больше — отказ с внятной
// ошибкой вместо OOM (локальный Bot API отдаёт файлы до 2GB).
const mediaMaxBytes = 512 << 20 // 512MB

// mediaUpSem ограничивает ПАРАЛЛЕЛЬНЫЕ перезаливы медиа в MAX: несколько больших
// видео одновременно давали пик памяти и OOM-kill процесса.
var mediaUpSem = make(chan struct{}, 2)

// downloadURL скачивает файл по URL и возвращает bytes (не больше mediaMaxBytes).
func (b *Bridge) downloadURL(url string) ([]byte, error) {
	slog.Debug("downloadURL", "url", url)
	resp, err := b.httpClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	slog.Debug("downloadURL response", "status", resp.StatusCode, "contentLength", resp.ContentLength, "url", url)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("download status %d url: %s", resp.StatusCode, url)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, mediaMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > mediaMaxBytes {
		return nil, &ErrFileTooLarge{Size: int64(len(data)), Name: fileNameFromURL(url)}
	}
	if len(data) == 0 {
		slog.Warn("downloadURL: empty body", "url", url, "contentLength", resp.ContentLength)
		return nil, fmt.Errorf("downloaded 0 bytes from %s", url)
	}
	slog.Debug("downloadURL ok", "size", len(data))
	return data, nil
}

// sendTgMediaFromURL скачивает файл с URL и отправляет в TG как upload.
// maxBytes=0 means no size limit. fileName overrides name extracted from URL.
func (b *Bridge) sendTgMediaFromURL(ctx context.Context, tgChatID int64, mediaURL, mediaType, caption, parseMode string, replyToID, threadID int, maxBytes int64, fileName ...string) (int, error) {
	slog.Debug("sendTgMediaFromURL start", "url", mediaURL, "type", mediaType, "tgChat", tgChatID)
	data, nameFromURL, err := b.downloadURLWithLimit(mediaURL, maxBytes)
	if err == nil {
		slog.Debug("sendTgMediaFromURL downloaded", "size", len(data), "name", nameFromURL)
	}
	if err != nil {
		return 0, fmt.Errorf("download media: %w", err)
	}

	name := nameFromURL
	if len(fileName) > 0 && fileName[0] != "" {
		name = fileName[0]
	}
	file := FileArg{Name: name, Bytes: data}
	detectedType := http.DetectContentType(data)

	switch mediaType {
	case "photo":
		// MAX иногда помечает GIF как photo. Telegram sendPhoto такие файлы не
		// принимает (а локальный Bot API может зависнуть до timeout). Фото свыше
		// 10 МБ Telegram тоже отвергает — отправляем оба случая документом, чтобы
		// медиа не блокировало очередь своего чата бесконечными повторами.
		if photoNeedsDocument(len(data), detectedType) {
			if detectedType == "image/gif" && !strings.HasSuffix(strings.ToLower(file.Name), ".gif") {
				file.Name = "animation.gif"
			}
			return b.tg.SendDocument(ctx, tgChatID, file, &SendOpts{
				Caption: caption, ParseMode: parseMode, ReplyToID: replyToID, ThreadID: threadID,
			})
		}
		return b.tg.SendPhoto(ctx, tgChatID, file, &SendOpts{
			Caption: caption, ParseMode: parseMode, ReplyToID: replyToID, ThreadID: threadID,
		})
	case "video":
		return b.tg.SendVideo(ctx, tgChatID, file, &SendOpts{
			Caption: caption, ParseMode: parseMode, ReplyToID: replyToID, ThreadID: threadID,
		})
	case "audio":
		return b.tg.SendAudio(ctx, tgChatID, file, &SendOpts{
			Caption: caption, ParseMode: parseMode, ReplyToID: replyToID, ThreadID: threadID,
		})
	case "file":
		return b.tg.SendDocument(ctx, tgChatID, file, &SendOpts{
			Caption: caption, ParseMode: parseMode, ReplyToID: replyToID, ThreadID: threadID,
		})
	default:
		// sticker и прочее — как фото
		return b.tg.SendPhoto(ctx, tgChatID, file, &SendOpts{
			Caption: caption, ParseMode: parseMode, ReplyToID: replyToID, ThreadID: threadID,
		})
	}
}

func photoNeedsDocument(size int, contentType string) bool {
	return size > 10*1024*1024 || contentType == "image/gif"
}

// customUploadToMax — обход бага SDK: CDN возвращает XML вместо JSON
func (b *Bridge) customUploadToMax(ctx context.Context, uploadType maxschemes.UploadType, reader io.Reader, fileName string) (*maxschemes.UploadedInfo, error) {
	// Детачим от родительской отмены: в webhook-режиме ctx привязан к HTTP-запросу апдейта и
	// отменяется, как только хендлер ответил 200 — а загрузка большого фото/видео в MAX CDN ещё
	// идёт → «context canceled», кросспост падал (жалоба «не удалось отправить в MAX»). Values
	// (dual-бот токен из ctx) сохраняем через WithoutCancel; свой таймаут 5 мин ограничивает.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
	defer cancel()

	// 1. Получаем URL и token от MAX API
	apiURL := fmt.Sprintf("%suploads?type=%s", maxAPIBaseURL, string(uploadType))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", b.maxTokenCtxOr(ctx)) // дуал-бот: токен релея (ctx) или основной

	resp, err := b.apiClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get upload url: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("upload endpoint status: %d", resp.StatusCode)
	}

	endpointBody, _ := io.ReadAll(resp.Body)
	slog.Debug("MAX upload endpoint response", "status", resp.StatusCode, "body", string(endpointBody))

	var endpoint maxschemes.UploadEndpoint
	if err := json.Unmarshal(endpointBody, &endpoint); err != nil {
		return nil, fmt.Errorf("decode upload endpoint: %w", err)
	}
	slog.Debug("MAX upload endpoint", "url", endpoint.Url, "token", endpoint.Token)

	// Для video/audio: token приходит сразу, но файл ВСЁ РАВНО нужно загрузить на CDN URL.
	// Для file/image: token приходит после загрузки на CDN.
	videoToken := endpoint.Token // сохраняем для video/audio

	if endpoint.Url == "" && videoToken != "" {
		// Нет URL для загрузки, но есть token — file/image (не video/audio)
		slog.Debug("MAX upload ok (endpoint token, no CDN needed)")
		return &maxschemes.UploadedInfo{Token: videoToken}, nil
	}

	if endpoint.Url == "" {
		return nil, fmt.Errorf("upload endpoint returned empty URL and no token")
	}

	// 2. Загружаем файл на CDN (multipart). Тело собираем во ВРЕМЕННОМ ФАЙЛЕ, а не в
	// памяти: видео бывают сотни МБ, полная буферизация в RAM валила процесс по OOM.
	// Заодно ограничиваем размер и число параллельных заливок (mediaUpSem).
	mediaUpSem <- struct{}{}
	defer func() { <-mediaUpSem }()

	tmp, err := os.CreateTemp("", "maxup-*")
	if err != nil {
		return nil, fmt.Errorf("create temp: %w", err)
	}
	defer func() {
		tmp.Close()
		_ = os.Remove(tmp.Name())
	}()
	writer := multipart.NewWriter(tmp)
	part, err := writer.CreateFormFile("data", fileName)
	if err != nil {
		return nil, fmt.Errorf("create form file: %w", err)
	}
	n, err := io.Copy(part, io.LimitReader(reader, mediaMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("copy to form: %w", err)
	}
	if n > mediaMaxBytes {
		return nil, &ErrFileTooLarge{Size: n, Name: fileName}
	}
	writer.Close()

	size, err := tmp.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, fmt.Errorf("temp seek: %w", err)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("temp rewind: %w", err)
	}

	cdnReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.Url, tmp)
	if err != nil {
		return nil, fmt.Errorf("create CDN request: %w", err)
	}
	cdnReq.ContentLength = size
	cdnReq.Header.Set("Content-Type", writer.FormDataContentType())

	cdnResp, err := b.httpClient.Do(cdnReq)
	if err != nil {
		return nil, fmt.Errorf("upload to CDN: %w", err)
	}
	defer cdnResp.Body.Close()

	cdnBody, _ := io.ReadAll(cdnResp.Body)
	slog.Debug("MAX CDN response", "status", cdnResp.StatusCode, "body", string(cdnBody))

	// Проверяем ошибку запрещённого расширения
	var apiErr struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if json.Unmarshal(cdnBody, &apiErr) == nil && apiErr.Code == "upload.error" {
		slog.Warn("MAX upload rejected", "code", apiErr.Code, "message", apiErr.Message, "file", fileName)
		return nil, &ErrForbiddenExtension{Name: fileName}
	}

	// 3. Для video/audio: используем token из шага 1 (CDN возвращает только retval)
	if videoToken != "" {
		slog.Debug("MAX upload ok (video/audio token from endpoint)", "token", videoToken)
		return &maxschemes.UploadedInfo{Token: videoToken}, nil
	}

	// Для file/image: парсим CDN ответ (fileId + token в camelCase)
	var cdnResult struct {
		FileID int64  `json:"fileId"`
		Token  string `json:"token"`
	}
	if err := json.Unmarshal(cdnBody, &cdnResult); err == nil && cdnResult.Token != "" {
		slog.Debug("MAX upload ok", "fileId", cdnResult.FileID)
		return &maxschemes.UploadedInfo{Token: cdnResult.Token, FileID: cdnResult.FileID}, nil
	}
	return nil, fmt.Errorf("no token in CDN response: %s", string(cdnBody))
}

// tgPhotoBytes скачивает байты самого крупного варианта фото (для модерации картинки —
// декод QR и vision). Возвращает данные + mime. Берём последний PhotoSize (наибольший).
func (b *Bridge) tgPhotoBytes(ctx context.Context, photos []PhotoSize) ([]byte, string, error) {
	if len(photos) == 0 {
		return nil, "", nil
	}
	// Жёсткий таймаут: модерация в последовательном цикле апдейтов — нельзя залипнуть
	// (обычный downloadURL живёт 5 мин). 10с хватает на фото, при зависании не стопаем бот.
	dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	fileURL, err := b.tgFileURL(dctx, photos[len(photos)-1].FileID)
	if err != nil {
		return nil, "", fmt.Errorf("tg getFileURL: %w", err)
	}
	req, err := http.NewRequestWithContext(dctx, http.MethodGet, fileURL, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := b.apiClient.Do(req) // apiClient: 15с таймаут (а не 5 мин)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, "", fmt.Errorf("tg download status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20)) // лимит 16MB
	if err != nil {
		return nil, "", err
	}
	return data, "image/jpeg", nil
}

// uploadTgPhotoToMax скачивает фото из TG и загружает в MAX (возвращает PhotoTokens).
// Загрузка — в обход SDK (наш customUploadPhotoToMax): SDK добавляет к /uploads
// устаревший параметр v=1.2.5, из-за которого MAX отдаёт 404 (как было с видео).
func (b *Bridge) uploadTgPhotoToMax(ctx context.Context, fileID string) (*maxschemes.PhotoTokens, error) {
	fileURL, err := b.tgFileURL(ctx, fileID)
	if err != nil {
		return nil, fmt.Errorf("tg getFileURL: %w", err)
	}
	dlReq, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create download request: %w", err)
	}
	resp, err := b.httpClient.Do(dlReq)
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("tg download status: %d", resp.StatusCode)
	}
	return b.customUploadPhotoToMax(ctx, resp.Body, "photo.jpg")
}

// customUploadPhotoToMax — загрузка фото в MAX в обход SDK (без устаревшего v=1.2.5,
// который вызывает 404). Два шага: получить upload URL (type=image), залить multipart,
// распарсить ответ CDN как PhotoTokens (карта photos).
func (b *Bridge) customUploadPhotoToMax(ctx context.Context, reader io.Reader, fileName string) (*maxschemes.PhotoTokens, error) {
	// 1. URL для загрузки (без v=).
	apiURL := maxAPIBaseURL + "uploads?type=image"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", b.maxTokenCtxOr(ctx)) // дуал-бот: токен релея (ctx) или основной
	resp, err := b.apiClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get upload url: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("photo upload endpoint status: %d", resp.StatusCode)
	}
	endpointBody, _ := io.ReadAll(resp.Body)
	var endpoint maxschemes.UploadEndpoint
	if err := json.Unmarshal(endpointBody, &endpoint); err != nil {
		return nil, fmt.Errorf("decode upload endpoint: %w", err)
	}
	if endpoint.Url == "" {
		return nil, fmt.Errorf("photo upload: empty upload URL")
	}

	// 2. multipart-загрузка файла на CDN.
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	part, err := writer.CreateFormFile("data", fileName)
	if err != nil {
		return nil, fmt.Errorf("create form file: %w", err)
	}
	if _, err := io.Copy(part, reader); err != nil {
		return nil, fmt.Errorf("copy to form: %w", err)
	}
	writer.Close()

	cdnReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.Url, &buf)
	if err != nil {
		return nil, fmt.Errorf("create CDN request: %w", err)
	}
	cdnReq.Header.Set("Content-Type", writer.FormDataContentType())
	cdnResp, err := b.httpClient.Do(cdnReq)
	if err != nil {
		return nil, fmt.Errorf("upload to CDN: %w", err)
	}
	defer cdnResp.Body.Close()
	cdnBody, _ := io.ReadAll(cdnResp.Body)
	slog.Debug("MAX photo CDN response", "status", cdnResp.StatusCode, "body", string(cdnBody))

	// Запрещённое расширение.
	var apiErr struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if json.Unmarshal(cdnBody, &apiErr) == nil && apiErr.Code == "upload.error" {
		return nil, &ErrForbiddenExtension{Name: fileName}
	}

	// 3. Ответ CDN — карта photos с токенами.
	var pt maxschemes.PhotoTokens
	if err := json.Unmarshal(cdnBody, &pt); err != nil || len(pt.Photos) == 0 {
		return nil, fmt.Errorf("photo upload: no tokens in CDN response: %s", string(cdnBody))
	}
	slog.Debug("MAX photo upload ok", "tokens", len(pt.Photos))
	return &pt, nil
}

// uploadTgMediaToMax скачивает файл из TG и загружает в MAX
func (b *Bridge) uploadTgMediaToMax(ctx context.Context, fileID string, uploadType maxschemes.UploadType, fileName string) (*maxschemes.UploadedInfo, error) {
	fileURL, err := b.tgFileURL(ctx, fileID)
	if err != nil {
		return nil, fmt.Errorf("tg getFileURL: %w", err)
	}

	dlReq, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create download request: %w", err)
	}

	resp, err := b.httpClient.Do(dlReq)
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("tg download status: %d url: %s", resp.StatusCode, fileURL)
	}

	// Слишком большой файл — отказ ДО перекачки (иначе зря гоняем сотни МБ).
	if resp.ContentLength > mediaMaxBytes {
		return nil, &ErrFileTooLarge{Size: resp.ContentLength, Name: fileName}
	}

	slog.Debug("TG file downloaded", "size", resp.ContentLength)

	return b.customUploadToMax(ctx, uploadType, resp.Body, fileName)
}

// MAX API режет text по 4000 символам. Берём запас на возможный учёт байт vs рун
// и на forward/реплай-обёртки.
const maxTextLimit = 3900

// splitMaxText режет текст на куски ≤ limit рун, по возможности на границе перевода
// строки или пробела в последних 20% куска (чтобы не рвать слова и абзацы).
func splitMaxText(text string, limit int) []string {
	runes := []rune(text)
	if len(runes) <= limit {
		return []string{text}
	}
	var chunks []string
	for len(runes) > 0 {
		if len(runes) <= limit {
			chunks = append(chunks, strings.TrimSpace(string(runes)))
			break
		}
		cut := limit
		minCut := limit * 4 / 5
		// Сначала ищем перевод строки, потом пробел.
		for i := limit; i > minCut; i-- {
			if runes[i] == '\n' {
				cut = i
				goto found
			}
		}
		for i := limit; i > minCut; i-- {
			if runes[i] == ' ' {
				cut = i
				break
			}
		}
	found:
		chunk := strings.TrimSpace(string(runes[:cut]))
		if chunk != "" {
			chunks = append(chunks, chunk)
		}
		// Сам разделитель пропускаем
		if cut < len(runes) && (runes[cut] == '\n' || runes[cut] == ' ') {
			cut++
		}
		runes = runes[cut:]
	}
	return chunks
}

// sendMaxDirect — отправка сообщения в MAX напрямую (обход SDK)
func (b *Bridge) sendMaxDirect(ctx context.Context, chatID int64, text string, attType string, token string, replyTo string) (string, error) {
	return b.sendMaxDirectFormatted(ctx, chatID, text, attType, token, replyTo, "")
}

// editMaxTextOnly updates text/format without sending an attachments field.
// Sending attachments=[] to MAX edit clears media; omitting it preserves photos/videos.
func (b *Bridge) editMaxTextOnly(ctx context.Context, chatID int64, maxMsgID, text, format string) error {
	type msgBody struct {
		Text   string `json:"text,omitempty"`
		Format string `json:"format,omitempty"`
	}
	if text == "" {
		format = ""
	}
	data, err := json.Marshal(msgBody{Text: text, Format: format})
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%smessages?message_id=%s", maxAPIBaseURL, maxMsgID)
	sendTok := b.maxTokenForSend(ctx, chatID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", sendTok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.apiClient.Do(req)
	if err != nil {
		return err
	}
	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("MAX API %d: %s", resp.StatusCode, string(respBody))
	}
	b.markChatBot(chatID, sendTok)
	return nil
}

// sendMaxDirectFormatted шлёт сообщение в MAX. Если текст длиннее лимита MAX —
// режет на части и шлёт несколько сообщений (первое с вложением и replyTo,
// последующие — продолжения как реплаи на первое). Возвращает mid первого.
func (b *Bridge) sendMaxDirectFormatted(ctx context.Context, chatID int64, text string, attType string, token string, replyTo string, format string) (string, error) {
	return b.sendMaxDirectFormattedKb(ctx, chatID, text, attType, token, replyTo, format, nil)
}

// maxOpenApp — спецификация кнопки OPEN_APP (мини-апп) для прикрепления к сообщению.
type maxOpenApp struct {
	Text    string
	AppName string // публичное имя бота (web_app)
	Payload string // идентификатор для мини-аппа (start_param)
}

// maxMediaAtt — одно медиа-вложение MAX (тип + токен загрузки). MAX принимает НЕСКОЛЬКО вложений
// в одном сообщении (альбом), поэтому шлём их списком в один пост, а не по одному.
type maxMediaAtt struct {
	Type  string // "video" | "image" | "file" | ...
	Token string
}

// sendMaxDirectFormattedKb — как sendMaxDirectFormatted, но прикрепляет к ПЕРВОМУ
// чанку inline-кнопку OPEN_APP (если openApp != nil). Кнопка едет на самом сообщении.
func (b *Bridge) sendMaxDirectFormattedKb(ctx context.Context, chatID int64, text string, attType string, token string, replyTo string, format string, openApp *maxOpenApp) (string, error) {
	var media []maxMediaAtt
	if attType != "" && token != "" {
		media = []maxMediaAtt{{Type: attType, Token: token}}
	}
	return b.sendMaxDirectMulti(ctx, chatID, text, media, replyTo, format, openApp)
}

// sendMaxDirectMulti шлёт сообщение с НЕСКОЛЬКИМИ вложениями (альбом) одним постом. Длинный текст
// режется на чанки: первый — с медиа и кнопкой, остальные — реплаи-продолжения. Возвращает mid первого.
func (b *Bridge) sendMaxDirectMulti(ctx context.Context, chatID int64, text string, media []maxMediaAtt, replyTo string, format string, openApp *maxOpenApp) (string, error) {
	chunks := splitMaxText(text, maxTextLimit)
	if len(chunks) <= 1 {
		return b.sendMaxChunk(ctx, chatID, text, media, replyTo, format, openApp)
	}
	firstMid, err := b.sendMaxChunk(ctx, chatID, chunks[0], media, replyTo, format, openApp)
	if err != nil {
		return firstMid, err
	}
	for _, part := range chunks[1:] {
		if _, err := b.sendMaxChunk(ctx, chatID, part, nil, firstMid, format, nil); err != nil {
			slog.Error("MAX send chunk failed", "err", err, "chatID", chatID)
		}
	}
	return firstMid, nil
}

func (b *Bridge) sendMaxChunk(ctx context.Context, chatID int64, text string, media []maxMediaAtt, replyTo string, format string, openApp *maxOpenApp) (string, error) {
	type attachment struct {
		Type    string `json:"type"`
		Payload any    `json:"payload"`
	}
	type msgBody struct {
		Text        string       `json:"text,omitempty"`
		Attachments []attachment `json:"attachments,omitempty"`
		Format      string       `json:"format,omitempty"`
		Link        *struct {
			Type string `json:"type"`
			Mid  string `json:"mid"`
		} `json:"link,omitempty"`
	}

	// format применяется только к тексту — при пустом тексте MAX отклоняет payload.
	if text == "" {
		format = ""
	}
	body := msgBody{Text: text, Format: format}
	for _, mm := range media {
		if mm.Type != "" && mm.Token != "" {
			body.Attachments = append(body.Attachments, attachment{
				Type:    mm.Type,
				Payload: map[string]string{"token": mm.Token},
			})
		}
	}
	if replyTo != "" {
		body.Link = &struct {
			Type string `json:"type"`
			Mid  string `json:"mid"`
		}{Type: "reply", Mid: replyTo}
	}
	// Inline-кнопка OPEN_APP (мини-апп) — едет на самом сообщении.
	if openApp != nil && openApp.AppName != "" {
		body.Attachments = append(body.Attachments, attachment{
			Type: "inline_keyboard",
			Payload: map[string]any{
				"buttons": [][]map[string]any{{{
					"type":    "open_app",
					"text":    openApp.Text,
					"web_app": openApp.AppName,
					"payload": openApp.Payload,
				}}},
			},
		})
	}

	data, err := json.Marshal(body)
	if err != nil {
		return "", err
	}

	url := fmt.Sprintf("%smessages?chat_id=%d", maxAPIBaseURL, chatID)

	// Пауза перед первой отправкой если есть медиа (MAX CDN нужно время на обработку)
	if len(media) > 0 {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}

	// Токен отправки фиксируем до цикла: дуал-бот: ctx-токен релея или по членству.
	sendTok := b.maxTokenForSend(ctx, chatID)
	triedAlt := false // пробовали ли уже второго бота (fallback при chat.not.found)
	flipped := false  // только что переключили бота — повторяем без задержки

	// Retry при attachment.not.ready (файл ещё обрабатывается)
	const attachmentReadyAttempts = 8
	for attempt := 0; attempt < attachmentReadyAttempts; attempt++ {
		if attempt > 0 && !flipped {
			delay := time.Duration(3+attempt*2) * time.Second
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(delay):
			}
			slog.Warn("MAX retry", "attempt", attempt+1, "maxAttempts", attachmentReadyAttempts)
		}
		flipped = false

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
		if err != nil {
			return "", err
		}
		req.Header.Set("Authorization", sendTok)
		req.Header.Set("Content-Type", "application/json")

		resp, err := b.apiClient.Do(req)
		if err != nil {
			return "", err
		}

		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == 200 {
			var result struct {
				Message struct {
					Body struct {
						Mid string `json:"mid"`
					} `json:"body"`
				} `json:"message"`
			}
			if err := json.Unmarshal(respBody, &result); err != nil {
				return "", err
			}
			b.markChatBot(chatID, sendTok) // запомним рабочего бота для будущих отправок
			return result.Message.Body.Mid, nil
		}

		// Проверяем attachment.not.ready — ретраим
		if resp.StatusCode == 400 && strings.Contains(string(respBody), "attachment.not.ready") {
			slog.Warn("MAX attachment not ready, waiting")
			continue
		}

		// Дуал-бот fallback: бот не в чате (chat.not.found/chat.denied) — пробуем второго
		// бота и чиним кэш роутинга (бота могли добавить позже / первая проверка промахнулась).
		bodyStr := string(respBody)
		if !triedAlt && (strings.Contains(bodyStr, "chat.not.found") || strings.Contains(bodyStr, "chat.denied")) {
			if alt := b.altMaxToken(sendTok); alt != "" && alt != sendTok {
				slog.Warn("MAX chat.not.found — пробую другого бота", "maxChat", chatID)
				sendTok = alt
				triedAlt = true
				flipped = true
				b.markChatBot(chatID, alt) // future-отправки сразу пойдут рабочим
				continue
			}
		}

		return "", fmt.Errorf("MAX API %d: %s", resp.StatusCode, bodyStr)
	}
	return "", fmt.Errorf("MAX attachment not ready after %d retries", attachmentReadyAttempts)
}

// formatFileSize formats file size in human-readable form.
func formatFileSize(size int) string {
	switch {
	case size >= 1024*1024:
		return fmt.Sprintf("%.1f МБ", float64(size)/1024/1024)
	case size >= 1024:
		return fmt.Sprintf("%.1f КБ", float64(size)/1024)
	default:
		return fmt.Sprintf("%d Б", size)
	}
}

// ErrFileTooLarge is returned when file exceeds the configured size limit.
type ErrFileTooLarge struct {
	Size int64
	Name string
}

func (e *ErrFileTooLarge) Error() string {
	return fmt.Sprintf("file too large: %s (%s)", e.Name, formatFileSize(int(e.Size)))
}

// ErrForbiddenExtension is returned when MAX API rejects the file extension.
type ErrForbiddenExtension struct {
	Name string
}

func (e *ErrForbiddenExtension) Error() string {
	return fmt.Sprintf("file extension forbidden by MAX: %s", e.Name)
}

// downloadURLWithLimit downloads a file from URL with an optional size limit.
// maxBytes=0 means no limit. Returns bytes and filename from Content-Disposition or URL.
func (b *Bridge) downloadURLWithLimit(url string, maxBytes int64) ([]byte, string, error) {
	slog.Debug("downloadURLWithLimit start", "url", url, "maxBytes", maxBytes)
	resp, err := b.httpClient.Get(url)
	if err != nil {
		slog.Error("downloadURLWithLimit failed", "err", err, "url", url)
		return nil, "", err
	}
	defer resp.Body.Close()
	slog.Debug("downloadURLWithLimit response", "status", resp.StatusCode, "contentLength", resp.ContentLength, "url", url)
	if resp.StatusCode != 200 {
		return nil, "", fmt.Errorf("download status %d", resp.StatusCode)
	}

	// Extract filename from Content-Disposition
	name := ""
	if cd := resp.Header.Get("Content-Disposition"); cd != "" {
		if i := strings.Index(cd, "filename=\""); i >= 0 {
			rest := cd[i+len("filename=\""):]
			if j := strings.Index(rest, "\""); j >= 0 {
				name = rest[:j]
			}
		}
		if name == "" {
			if i := strings.Index(cd, "filename="); i >= 0 {
				rest := strings.TrimSpace(cd[i+len("filename="):])
				if j := strings.IndexAny(rest, "; \t"); j >= 0 {
					name = rest[:j]
				} else {
					name = rest
				}
			}
		}
	}
	if name == "" {
		name = fileNameFromURL(url)
	}

	// Fast check via Content-Length
	if maxBytes > 0 && resp.ContentLength > maxBytes {
		return nil, name, &ErrFileTooLarge{Size: resp.ContentLength, Name: name}
	}

	// Read body
	var data []byte
	if maxBytes > 0 {
		data, err = io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	} else {
		data, err = io.ReadAll(resp.Body)
	}
	if err != nil {
		return nil, "", err
	}
	if maxBytes > 0 && int64(len(data)) > maxBytes {
		return nil, name, &ErrFileTooLarge{Size: int64(len(data)), Name: name}
	}

	return data, name, nil
}
