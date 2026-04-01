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
	"strings"
	"time"

	maxschemes "github.com/max-messenger/max-bot-api-client-go/schemes"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// downloadURL скачивает файл по URL и возвращает bytes.
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
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
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
func (b *Bridge) sendTgMediaFromURL(tgChatID int64, mediaURL, mediaType, caption, parseMode string, replyToID int, maxBytes int64, fileName ...string) (tgbotapi.Message, error) {
	slog.Debug("sendTgMediaFromURL start", "url", mediaURL, "type", mediaType, "tgChat", tgChatID)
	data, nameFromURL, err := b.downloadURLWithLimit(mediaURL, maxBytes)
	if err == nil {
		slog.Debug("sendTgMediaFromURL downloaded", "size", len(data), "name", nameFromURL)
	}
	if err != nil {
		return tgbotapi.Message{}, fmt.Errorf("download media: %w", err)
	}

	name := nameFromURL
	if len(fileName) > 0 && fileName[0] != "" {
		name = fileName[0]
	}
	fb := tgbotapi.FileBytes{Name: name, Bytes: data}

	switch mediaType {
	case "photo":
		msg := tgbotapi.NewPhoto(tgChatID, fb)
		msg.Caption = caption
		if parseMode != "" {
			msg.ParseMode = parseMode
		}
		msg.ReplyToMessageID = replyToID
		return b.tgBot.Send(msg)
	case "video":
		msg := tgbotapi.NewVideo(tgChatID, fb)
		msg.Caption = caption
		if parseMode != "" {
			msg.ParseMode = parseMode
		}
		msg.ReplyToMessageID = replyToID
		return b.tgBot.Send(msg)
	case "audio":
		msg := tgbotapi.NewAudio(tgChatID, fb)
		msg.Caption = caption
		if parseMode != "" {
			msg.ParseMode = parseMode
		}
		msg.ReplyToMessageID = replyToID
		return b.tgBot.Send(msg)
	case "file":
		msg := tgbotapi.NewDocument(tgChatID, fb)
		msg.Caption = caption
		if parseMode != "" {
			msg.ParseMode = parseMode
		}
		msg.ReplyToMessageID = replyToID
		return b.tgBot.Send(msg)
	default:
		// sticker и прочее — как фото
		msg := tgbotapi.NewPhoto(tgChatID, fb)
		msg.Caption = caption
		return b.tgBot.Send(msg)
	}
}

// customUploadToMax — обход бага SDK: CDN возвращает XML вместо JSON
func (b *Bridge) customUploadToMax(ctx context.Context, uploadType maxschemes.UploadType, reader io.Reader, fileName string) (*maxschemes.UploadedInfo, error) {
	// 1. Получаем URL и token от MAX API
	apiURL := fmt.Sprintf("https://platform-api.max.ru/uploads?type=%s&v=1.2.5", string(uploadType))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", b.cfg.MaxToken)

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

	// 2. Загружаем файл на CDN (multipart)
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

// uploadTgPhotoToMax скачивает фото из TG и загружает в MAX через SDK (возвращает PhotoTokens).
func (b *Bridge) uploadTgPhotoToMax(ctx context.Context, fileID string) (*maxschemes.PhotoTokens, error) {
	fileURL, err := b.tgFileURL(fileID)
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
	return b.maxApi.Uploads.UploadPhotoFromReader(ctx, resp.Body)
}

// uploadTgMediaToMax скачивает файл из TG и загружает в MAX
func (b *Bridge) uploadTgMediaToMax(ctx context.Context, fileID string, uploadType maxschemes.UploadType, fileName string) (*maxschemes.UploadedInfo, error) {
	fileURL, err := b.tgFileURL(fileID)
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

	slog.Debug("TG file downloaded", "size", resp.ContentLength)

	return b.customUploadToMax(ctx, uploadType, resp.Body, fileName)
}

// sendMaxDirect — отправка сообщения в MAX напрямую (обход SDK)
func (b *Bridge) sendMaxDirect(ctx context.Context, chatID int64, text string, attType string, token string, replyTo string) (string, error) {
	return b.sendMaxDirectFormatted(ctx, chatID, text, attType, token, replyTo, "")
}

func (b *Bridge) sendMaxDirectFormatted(ctx context.Context, chatID int64, text string, attType string, token string, replyTo string, format string) (string, error) {
	type attachment struct {
		Type    string            `json:"type"`
		Payload map[string]string `json:"payload"`
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

	body := msgBody{Text: text, Format: format}
	if attType != "" && token != "" {
		body.Attachments = []attachment{{
			Type:    attType,
			Payload: map[string]string{"token": token},
		}}
	}
	if replyTo != "" {
		body.Link = &struct {
			Type string `json:"type"`
			Mid  string `json:"mid"`
		}{Type: "reply", Mid: replyTo}
	}

	data, err := json.Marshal(body)
	if err != nil {
		return "", err
	}

	url := fmt.Sprintf("https://platform-api.max.ru/messages?chat_id=%d&v=1.2.5", chatID)

	// Пауза перед первой отправкой если есть вложение (MAX CDN нужно время на обработку)
	if attType != "" && token != "" {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}

	// Retry при attachment.not.ready (файл ещё обрабатывается)
	for attempt := 0; attempt < 20; attempt++ {
		if attempt > 0 {
			delay := time.Duration(3+attempt*2) * time.Second
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(delay):
			}
			slog.Warn("MAX retry", "attempt", attempt+1, "maxAttempts", 20)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
		if err != nil {
			return "", err
		}
		req.Header.Set("Authorization", b.cfg.MaxToken)
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
			return result.Message.Body.Mid, nil
		}

		// Проверяем attachment.not.ready — ретраим
		if resp.StatusCode == 400 && strings.Contains(string(respBody), "attachment.not.ready") {
			slog.Warn("MAX attachment not ready, waiting")
			continue
		}

		return "", fmt.Errorf("MAX API %d: %s", resp.StatusCode, string(respBody))
	}
	return "", fmt.Errorf("MAX attachment not ready after 10 retries")
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
