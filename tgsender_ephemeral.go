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

// tgEphemeralTarget is carried only while one Telegram group command or
// callback is being handled. SendMessage uses it to keep every answer private
// to the member who initiated the interaction.
type tgEphemeralTarget struct {
	ChatID                     int64
	ReceiverUserID             int64
	IncomingEphemeralMessageID int
	CallbackQueryID            string
}

type tgEphemeralContextKey struct{}

func withTGEphemeralTarget(ctx context.Context, target tgEphemeralTarget) context.Context {
	if ctx == nil || target.ChatID == 0 || target.ReceiverUserID == 0 {
		return ctx
	}
	return context.WithValue(ctx, tgEphemeralContextKey{}, target)
}

func tgEphemeralTargetForSend(ctx context.Context, chatID int64, opts *SendOpts) (tgEphemeralTarget, bool) {
	if opts != nil && opts.ReceiverUserID != 0 {
		return tgEphemeralTarget{
			ChatID:                     chatID,
			ReceiverUserID:             opts.ReceiverUserID,
			IncomingEphemeralMessageID: opts.EphemeralReplyID,
			CallbackQueryID:            opts.CallbackQueryID,
		}, true
	}
	if ctx == nil {
		return tgEphemeralTarget{}, false
	}
	target, ok := ctx.Value(tgEphemeralContextKey{}).(tgEphemeralTarget)
	return target, ok && target.ChatID == chatID && target.ReceiverUserID != 0
}

type tgEphemeralReplyParameters struct {
	MessageID          int `json:"message_id,omitempty"`
	EphemeralMessageID int `json:"ephemeral_message_id,omitempty"`
}

type tgSendEphemeralMessageParams struct {
	ChatID          int64                       `json:"chat_id"`
	MessageThreadID int                         `json:"message_thread_id,omitempty"`
	ReceiverUserID  int64                       `json:"receiver_user_id"`
	CallbackQueryID string                      `json:"callback_query_id,omitempty"`
	Text            string                      `json:"text"`
	ParseMode       string                      `json:"parse_mode,omitempty"`
	ReplyParameters *tgEphemeralReplyParameters `json:"reply_parameters,omitempty"`
	ReplyMarkup     any                         `json:"reply_markup,omitempty"`
}

func (s *tgBotSender) sendEphemeralMessage(
	ctx context.Context,
	chatID int64,
	text string,
	opts *SendOpts,
	target tgEphemeralTarget,
) (int, error) {
	params := tgSendEphemeralMessageParams{
		ChatID:          chatID,
		ReceiverUserID:  target.ReceiverUserID,
		CallbackQueryID: target.CallbackQueryID,
		Text:            text,
	}
	if opts != nil {
		params.MessageThreadID = opts.ThreadID
		params.ParseMode = opts.ParseMode
		if opts.ReplyMarkup != nil {
			params.ReplyMarkup = toLibKeyboard(opts.ReplyMarkup)
		}
	}
	switch {
	case target.CallbackQueryID != "":
		// callback_query_id itself authorizes the whisper for 15 seconds.
	case target.IncomingEphemeralMessageID != 0:
		params.ReplyParameters = &tgEphemeralReplyParameters{
			EphemeralMessageID: target.IncomingEphemeralMessageID,
		}
	case opts != nil && opts.ReplyToID != 0:
		params.ReplyParameters = &tgEphemeralReplyParameters{MessageID: opts.ReplyToID}
	}

	var result struct {
		MessageID          int `json:"message_id"`
		EphemeralMessageID int `json:"ephemeral_message_id"`
	}
	if err := s.doTGJSONRequest(ctx, "sendMessage", params, &result); err != nil {
		return 0, err
	}
	// EphemeralMessageID belongs to the receiver-specific namespace and cannot
	// be passed to ordinary editMessage/deleteMessage. Returning zero prevents
	// callers from accidentally treating it as a normal message id.
	if result.EphemeralMessageID != 0 {
		return 0, nil
	}
	return result.MessageID, nil
}

type tgEditEphemeralMessageTextParams struct {
	ChatID             int64  `json:"chat_id"`
	ReceiverUserID     int64  `json:"receiver_user_id"`
	EphemeralMessageID int    `json:"ephemeral_message_id"`
	Text               string `json:"text"`
	ParseMode          string `json:"parse_mode,omitempty"`
	ReplyMarkup        any    `json:"reply_markup,omitempty"`
}

func (s *tgBotSender) editEphemeralMessageText(
	ctx context.Context,
	chatID int64,
	text string,
	opts *SendOpts,
	target tgEphemeralTarget,
) error {
	params := tgEditEphemeralMessageTextParams{
		ChatID:             chatID,
		ReceiverUserID:     target.ReceiverUserID,
		EphemeralMessageID: target.IncomingEphemeralMessageID,
		Text:               text,
	}
	if opts != nil {
		params.ParseMode = opts.ParseMode
		if opts.ReplyMarkup != nil {
			params.ReplyMarkup = toLibKeyboard(opts.ReplyMarkup)
		}
	}
	var result bool
	if err := s.doTGJSONRequest(ctx, "editEphemeralMessageText", params, &result); err != nil {
		return err
	}
	if !result {
		return fmt.Errorf("telegram: editEphemeralMessageText returned false")
	}
	return nil
}

type tgDeleteEphemeralMessageParams struct {
	ChatID             int64 `json:"chat_id"`
	ReceiverUserID     int64 `json:"receiver_user_id"`
	EphemeralMessageID int   `json:"ephemeral_message_id"`
}

func (s *tgBotSender) deleteEphemeralMessage(
	ctx context.Context,
	chatID int64,
	target tgEphemeralTarget,
) error {
	var result bool
	err := s.doTGJSONRequest(ctx, "deleteEphemeralMessage", tgDeleteEphemeralMessageParams{
		ChatID:             chatID,
		ReceiverUserID:     target.ReceiverUserID,
		EphemeralMessageID: target.IncomingEphemeralMessageID,
	}, &result)
	if err != nil {
		return err
	}
	if !result {
		return fmt.Errorf("telegram: deleteEphemeralMessage returned false")
	}
	return nil
}

type tgRawBotCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
	IsEphemeral bool   `json:"is_ephemeral,omitempty"`
}

type tgRawCommandScope struct {
	Type string `json:"type"`
}

type tgSetMyCommandsParams struct {
	Commands []tgRawBotCommand  `json:"commands"`
	Scope    *tgRawCommandScope `json:"scope,omitempty"`
}

func (s *tgBotSender) setMyCommandsRaw(ctx context.Context, commands []BotCommand, scope *CommandScope) error {
	params := tgSetMyCommandsParams{
		Commands: make([]tgRawBotCommand, len(commands)),
	}
	for i, command := range commands {
		params.Commands[i] = tgRawBotCommand{
			Command:     command.Command,
			Description: command.Description,
			IsEphemeral: command.IsEphemeral,
		}
	}
	if scope != nil && scope.Type != "" {
		params.Scope = &tgRawCommandScope{Type: scope.Type}
	}
	var result bool
	if err := s.doTGJSONRequest(ctx, "setMyCommands", params, &result); err != nil {
		return err
	}
	if !result {
		return fmt.Errorf("telegram: setMyCommands returned false")
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

// The Telegram library used by the bridge predates Bot API 10.2 and discards
// ephemeral_message_id while decoding webhook updates. Preserve this one field
// from the raw webhook and merge it into the adapter model in the handler.
type tgRawEphemeralUpdate struct {
	MessageEphemeralID  int
	CallbackEphemeralID int
}

func (s *tgBotSender) captureRawEphemeralUpdate(body []byte) {
	var raw struct {
		UpdateID int64 `json:"update_id"`
		Message  *struct {
			EphemeralMessageID int `json:"ephemeral_message_id"`
		} `json:"message"`
		CallbackQuery *struct {
			Message *struct {
				EphemeralMessageID int `json:"ephemeral_message_id"`
			} `json:"message"`
		} `json:"callback_query"`
	}
	if json.Unmarshal(body, &raw) != nil || raw.UpdateID == 0 {
		return
	}
	value := tgRawEphemeralUpdate{}
	if raw.Message != nil {
		value.MessageEphemeralID = raw.Message.EphemeralMessageID
	}
	if raw.CallbackQuery != nil && raw.CallbackQuery.Message != nil {
		value.CallbackEphemeralID = raw.CallbackQuery.Message.EphemeralMessageID
	}
	if value.MessageEphemeralID == 0 && value.CallbackEphemeralID == 0 {
		return
	}

	s.ephemeralMu.Lock()
	if s.rawEphemeralByID == nil {
		s.rawEphemeralByID = make(map[int64]tgRawEphemeralUpdate)
	}
	// A malformed/unhandled webhook must not grow this small compatibility map
	// without bound.
	if len(s.rawEphemeralByID) >= 2048 {
		for id := range s.rawEphemeralByID {
			delete(s.rawEphemeralByID, id)
			if len(s.rawEphemeralByID) < 1024 {
				break
			}
		}
	}
	s.rawEphemeralByID[raw.UpdateID] = value
	s.ephemeralMu.Unlock()
}

func (s *tgBotSender) applyRawEphemeralUpdate(updateID int64, update *TGUpdate) {
	if update == nil || updateID == 0 {
		return
	}
	s.ephemeralMu.Lock()
	value, ok := s.rawEphemeralByID[updateID]
	if ok {
		delete(s.rawEphemeralByID, updateID)
	}
	s.ephemeralMu.Unlock()
	if !ok {
		return
	}
	if update.Message != nil {
		update.Message.EphemeralMessageID = value.MessageEphemeralID
	}
	if update.CallbackQuery != nil && update.CallbackQuery.Message != nil {
		update.CallbackQuery.Message.EphemeralMessageID = value.CallbackEphemeralID
	}
}
