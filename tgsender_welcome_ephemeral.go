package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// SendWelcomeEphemeral — единственная разрешённая точка использования
// receiver_user_id. Она намеренно не входит в SendMessage/SendOpts и не связана
// с командами: скрытое сообщение можно отправить только как приветствие.
func (s *tgBotSender) SendWelcomeEphemeral(ctx context.Context, chatID, receiverUserID int64, text string) error {
	if chatID == 0 || receiverUserID <= 0 || strings.TrimSpace(text) == "" {
		return fmt.Errorf("telegram welcome ephemeral: invalid target")
	}
	params := struct {
		ChatID         int64  `json:"chat_id"`
		ReceiverUserID int64  `json:"receiver_user_id"`
		Text           string `json:"text"`
	}{
		ChatID:         chatID,
		ReceiverUserID: receiverUserID,
		Text:           text,
	}
	var result struct {
		MessageID          int `json:"message_id"`
		EphemeralMessageID int `json:"ephemeral_message_id"`
	}
	if err := s.doTGJSONRequest(ctx, "sendMessage", params, &result); err != nil {
		return err
	}
	if result.MessageID == 0 && result.EphemeralMessageID == 0 {
		return fmt.Errorf("telegram welcome ephemeral: empty result")
	}
	return nil
}

func (s *tgBotSender) doTGJSONRequest(ctx context.Context, method string, params, result any) error {
	data, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("telegram %s: encode request: %w", method, err)
	}
	apiURL := strings.TrimRight(s.apiURL, "/")
	if apiURL == "" {
		apiURL = "https://api.telegram.org"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL+"/bot"+s.token+"/"+method, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("telegram %s: create request: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := s.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("telegram %s: request failed: %s", method,
			strings.ReplaceAll(err.Error(), s.token, "***"))
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return fmt.Errorf("telegram %s: read response: %w", method, err)
	}
	var apiResp tgRawAPIResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return fmt.Errorf("telegram %s: decode response: %w", method, err)
	}
	if !apiResp.OK {
		description := apiResp.Description
		if apiResp.Parameters.RetryAfter > 0 {
			description += fmt.Sprintf(": retry_after %d", apiResp.Parameters.RetryAfter)
		}
		return &TGError{
			Code:            apiResp.ErrorCode,
			Description:     description,
			MigrateToChatID: apiResp.Parameters.MigrateToChatID,
		}
	}
	if result != nil && len(apiResp.Result) != 0 {
		if err := json.Unmarshal(apiResp.Result, result); err != nil {
			return fmt.Errorf("telegram %s: decode result: %w", method, err)
		}
	}
	return nil
}
