package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
)

const tgRichMediaID = "bridge_media"

type tgRichInputMedia struct {
	Type              string `json:"type"`
	Media             string `json:"media"`
	SupportsStreaming bool   `json:"supports_streaming,omitempty"`
}

type tgRichMessageMedia struct {
	ID    string           `json:"id"`
	Media tgRichInputMedia `json:"media"`
}

type tgInputRichMessage struct {
	HTML  string               `json:"html"`
	Media []tgRichMessageMedia `json:"media"`
}

type tgSendRichMessageParams struct {
	ChatID          int64              `json:"chat_id"`
	MessageThreadID int                `json:"message_thread_id,omitempty"`
	RichMessage     tgInputRichMessage `json:"rich_message"`
	ReplyParameters *tgReplyParameters `json:"reply_parameters,omitempty"`
}

type tgEditRichMessageParams struct {
	ChatID      int64              `json:"chat_id"`
	MessageID   int                `json:"message_id"`
	RichMessage tgInputRichMessage `json:"rich_message"`
}

type tgReplyParameters struct {
	MessageID int `json:"message_id"`
}

type tgRawAPIResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result,omitempty"`
	Description string          `json:"description,omitempty"`
	ErrorCode   int             `json:"error_code,omitempty"`
	Parameters  struct {
		RetryAfter      int   `json:"retry_after,omitempty"`
		MigrateToChatID int64 `json:"migrate_to_chat_id,omitempty"`
	} `json:"parameters,omitempty"`
}

// SendRichMediaMessage отправляет фото/видео и длинный текст одним сообщением
// через Bot API 10.2. Текущая версия github.com/go-telegram/bot ещё не содержит
// InputRichMessage.media, поэтому этот один метод вызывается напрямую.
func (s *tgBotSender) SendRichMediaMessage(ctx context.Context, chatID int64, richHTML string, media TGRichMedia, opts *SendOpts) (int, error) {
	richMessage, err := makeTGInputRichMessage(richHTML, media)
	if err != nil {
		return 0, err
	}

	params := tgSendRichMessageParams{
		ChatID:      chatID,
		RichMessage: richMessage,
	}
	if opts != nil {
		params.MessageThreadID = opts.ThreadID
		if opts.ReplyToID != 0 {
			params.ReplyParameters = &tgReplyParameters{MessageID: opts.ReplyToID}
		}
	}

	fields, err := tgRichMultipartFields(params)
	if err != nil {
		return 0, err
	}
	return s.doTGRichMediaRequest(ctx, "sendRichMessage", params, fields, media.File)
}

// EditRichMediaMessage меняет Rich Message целиком, сохраняя медиа внутри
// того же message_id. Это не даёт синхронизации правок превратить rich-пост
// обратно в обычный текст и потерять видео/фото.
func (s *tgBotSender) EditRichMediaMessage(ctx context.Context, chatID int64, msgID int, richHTML string, media TGRichMedia) error {
	richMessage, err := makeTGInputRichMessage(richHTML, media)
	if err != nil {
		return err
	}
	params := tgEditRichMessageParams{
		ChatID:      chatID,
		MessageID:   msgID,
		RichMessage: richMessage,
	}
	fields, err := tgRichMultipartFields(params)
	if err != nil {
		return err
	}
	_, err = s.doTGRichMediaRequest(ctx, "editMessageText", params, fields, media.File)
	return err
}

func makeTGInputRichMessage(richHTML string, media TGRichMedia) (tgInputRichMessage, error) {
	if media.Type != "photo" && media.Type != "video" {
		return tgInputRichMessage{}, fmt.Errorf("telegram rich message: unsupported media type %q", media.Type)
	}
	if richHTML == "" {
		return tgInputRichMessage{}, fmt.Errorf("telegram rich message: empty HTML")
	}
	mediaRef := media.File.URL
	if len(media.File.Bytes) > 0 {
		mediaRef = "attach://" + tgRichMediaID
	}
	if mediaRef == "" {
		return tgInputRichMessage{}, fmt.Errorf("telegram rich message: empty media")
	}
	return tgInputRichMessage{
		HTML: richHTML,
		Media: []tgRichMessageMedia{{
			ID: tgRichMediaID,
			Media: tgRichInputMedia{
				Type:              media.Type,
				Media:             mediaRef,
				SupportsStreaming: media.Type == "video",
			},
		}},
	}, nil
}

func tgRichMultipartFields(params any) (map[string]string, error) {
	var raw map[string]json.RawMessage
	data, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("telegram rich message: encode request: %w", err)
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("telegram rich message: encode fields: %w", err)
	}
	fields := make(map[string]string, len(raw))
	for key, value := range raw {
		if len(value) > 0 && string(value) != "null" {
			fields[key] = string(value)
		}
	}
	return fields, nil
}

func (s *tgBotSender) doTGRichMediaRequest(
	ctx context.Context,
	method string,
	params any,
	multipartFields map[string]string,
	file FileArg,
) (int, error) {
	var body io.Reader
	contentType := "application/json"
	if len(file.Bytes) == 0 {
		data, err := json.Marshal(params)
		if err != nil {
			return 0, fmt.Errorf("telegram rich message: encode request: %w", err)
		}
		body = bytes.NewReader(data)
	} else {
		pr, pw := io.Pipe()
		form := multipart.NewWriter(pw)
		contentType = form.FormDataContentType()
		go writeTGRichMultipart(pw, form, multipartFields, file)
		body = pr
	}

	apiURL := strings.TrimRight(s.apiURL, "/")
	if apiURL == "" {
		apiURL = "https://api.telegram.org"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL+"/bot"+s.token+"/"+method, body)
	if err != nil {
		return 0, fmt.Errorf("telegram rich message: create request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)

	client := s.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		// Не отдаём токен наружу через url.Error.
		return 0, fmt.Errorf("telegram rich message: request failed: %s",
			strings.ReplaceAll(err.Error(), s.token, "***"))
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return 0, fmt.Errorf("telegram rich message: read response: %w", err)
	}
	var apiResp tgRawAPIResponse
	if err := json.Unmarshal(responseBody, &apiResp); err != nil {
		return 0, fmt.Errorf("telegram rich message: decode response: %w", err)
	}
	if !apiResp.OK {
		description := apiResp.Description
		if apiResp.Parameters.RetryAfter > 0 {
			description += fmt.Sprintf(": retry_after %d", apiResp.Parameters.RetryAfter)
		}
		return 0, &TGError{
			Code:            apiResp.ErrorCode,
			Description:     description,
			MigrateToChatID: apiResp.Parameters.MigrateToChatID,
		}
	}

	var msg struct {
		ID int `json:"message_id"`
	}
	if err := json.Unmarshal(apiResp.Result, &msg); err != nil {
		return 0, fmt.Errorf("telegram rich message: decode result: %w", err)
	}
	if msg.ID == 0 {
		return 0, fmt.Errorf("telegram rich message: empty message_id")
	}
	return msg.ID, nil
}

func writeTGRichMultipart(pw *io.PipeWriter, form *multipart.Writer, fields map[string]string, file FileArg) {
	var writeErr error
	defer func() {
		if writeErr == nil {
			writeErr = form.Close()
		}
		if writeErr != nil {
			_ = pw.CloseWithError(writeErr)
			return
		}
		_ = pw.Close()
	}()

	for name, value := range fields {
		if writeErr == nil && value != "" {
			// Числовые JSON-значения уже представлены десятичной строкой;
			// вложенные объекты Bot API принимает как JSON-поле multipart.
			writeErr = form.WriteField(name, strings.Trim(value, `"`))
		}
	}
	if writeErr != nil {
		return
	}

	name := file.Name
	if name == "" {
		name = "media"
	}
	part, err := form.CreateFormFile(tgRichMediaID, name)
	if err != nil {
		writeErr = err
		return
	}
	_, writeErr = part.Write(file.Bytes)
}
