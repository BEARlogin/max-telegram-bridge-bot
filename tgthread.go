package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// tgSendText отправляет текст в TG с поддержкой message_thread_id.
func (b *Bridge) tgSendText(chatID int64, text, parseMode string, replyToID, threadID int) (tgbotapi.Message, error) {
	if threadID == 0 {
		msg := tgbotapi.NewMessage(chatID, text)
		msg.ParseMode = parseMode
		msg.ReplyToMessageID = replyToID
		return b.tgBot.Send(msg)
	}

	params := map[string]interface{}{
		"chat_id":           chatID,
		"text":              text,
		"message_thread_id": threadID,
	}
	if parseMode != "" {
		params["parse_mode"] = parseMode
	}
	if replyToID != 0 {
		params["reply_to_message_id"] = replyToID
	}

	return b.tgRawRequest("sendMessage", params)
}

// tgRawRequest выполняет запрос к TG Bot API и парсит результат.
func (b *Bridge) tgRawRequest(method string, params map[string]interface{}) (tgbotapi.Message, error) {
	data, err := json.Marshal(params)
	if err != nil {
		return tgbotapi.Message{}, err
	}

	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/%s", b.tgBot.Token, method)
	if b.cfg.TgAPIURL != "" {
		apiURL = fmt.Sprintf("%s/bot%s/%s", b.cfg.TgAPIURL, b.tgBot.Token, method)
	}

	resp, err := b.apiClient.Post(apiURL, "application/json", strings.NewReader(string(data)))
	if err != nil {
		return tgbotapi.Message{}, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var result struct {
		OK     bool             `json:"ok"`
		Result tgbotapi.Message `json:"result"`
		Desc   string           `json:"description"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return tgbotapi.Message{}, fmt.Errorf("TG API parse error: %w", err)
	}
	if !result.OK {
		return tgbotapi.Message{}, fmt.Errorf("Bad Request: %s", result.Desc)
	}
	return result.Result, nil
}

// tgSendMediaToThread отправляет медиа в TG с поддержкой thread_id.
func (b *Bridge) tgSendMediaToThread(chatID int64, fileBytes []byte, fileName, mediaType, caption, parseMode string, replyToID, threadID int) (tgbotapi.Message, error) {
	if threadID == 0 {
		// Без thread — используем стандартный SDK
		fb := tgbotapi.FileBytes{Name: fileName, Bytes: fileBytes}
		switch mediaType {
		case "photo":
			msg := tgbotapi.NewPhoto(chatID, fb)
			msg.Caption = caption
			if parseMode != "" {
				msg.ParseMode = parseMode
			}
			msg.ReplyToMessageID = replyToID
			return b.tgBot.Send(msg)
		case "video":
			msg := tgbotapi.NewVideo(chatID, fb)
			msg.Caption = caption
			if parseMode != "" {
				msg.ParseMode = parseMode
			}
			msg.ReplyToMessageID = replyToID
			return b.tgBot.Send(msg)
		case "audio":
			msg := tgbotapi.NewAudio(chatID, fb)
			msg.Caption = caption
			if parseMode != "" {
				msg.ParseMode = parseMode
			}
			msg.ReplyToMessageID = replyToID
			return b.tgBot.Send(msg)
		case "file":
			msg := tgbotapi.NewDocument(chatID, fb)
			msg.Caption = caption
			if parseMode != "" {
				msg.ParseMode = parseMode
			}
			msg.ReplyToMessageID = replyToID
			return b.tgBot.Send(msg)
		default:
			msg := tgbotapi.NewPhoto(chatID, fb)
			msg.Caption = caption
			return b.tgBot.Send(msg)
		}
	}

	// С thread — через multipart (SDK не поддерживает thread_id)
	return b.tgMultipartUpload(chatID, fileBytes, fileName, mediaType, caption, parseMode, replyToID, threadID)
}

// tgMultipartUpload загружает файл через multipart с message_thread_id.
func (b *Bridge) tgMultipartUpload(chatID int64, fileBytes []byte, fileName, mediaType, caption, parseMode string, replyToID, threadID int) (tgbotapi.Message, error) {
	method := "sendDocument"
	fieldName := "document"
	switch mediaType {
	case "photo":
		method = "sendPhoto"
		fieldName = "photo"
	case "video":
		method = "sendVideo"
		fieldName = "video"
	case "audio":
		method = "sendAudio"
		fieldName = "audio"
	}

	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/%s", b.tgBot.Token, method)
	if b.cfg.TgAPIURL != "" {
		apiURL = fmt.Sprintf("%s/bot%s/%s", b.cfg.TgAPIURL, b.tgBot.Token, method)
	}

	var buf strings.Builder
	boundary := "----BridgeBoundary"
	w := func(s string) { buf.WriteString(s) }

	addField := func(name, value string) {
		w(fmt.Sprintf("--%s\r\nContent-Disposition: form-data; name=\"%s\"\r\n\r\n%s\r\n", boundary, name, value))
	}

	addField("chat_id", fmt.Sprintf("%d", chatID))
	addField("message_thread_id", fmt.Sprintf("%d", threadID))
	if caption != "" {
		addField("caption", caption)
	}
	if parseMode != "" {
		addField("parse_mode", parseMode)
	}
	if replyToID != 0 {
		addField("reply_to_message_id", fmt.Sprintf("%d", replyToID))
	}

	// File part
	w(fmt.Sprintf("--%s\r\nContent-Disposition: form-data; name=\"%s\"; filename=\"%s\"\r\nContent-Type: application/octet-stream\r\n\r\n", boundary, fieldName, fileName))
	prefix := buf.String()
	suffix := fmt.Sprintf("\r\n--%s--\r\n", boundary)

	bodyReader := io.MultiReader(
		strings.NewReader(prefix),
		strings.NewReader(string(fileBytes)),
		strings.NewReader(suffix),
	)

	req, err := http.NewRequest("POST", apiURL, bodyReader)
	if err != nil {
		return tgbotapi.Message{}, err
	}
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)

	resp, err := b.httpClient.Do(req)
	if err != nil {
		return tgbotapi.Message{}, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	var result struct {
		OK     bool             `json:"ok"`
		Result tgbotapi.Message `json:"result"`
		Desc   string           `json:"description"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return tgbotapi.Message{}, fmt.Errorf("TG API parse error: %w", err)
	}
	if !result.OK {
		return tgbotapi.Message{}, fmt.Errorf("Bad Request: %s", result.Desc)
	}
	return result.Result, nil
}
