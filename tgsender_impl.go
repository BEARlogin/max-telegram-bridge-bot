package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

type tgBotSender struct {
	b        *bot.Bot
	token    string
	username string
	apiURL   string
	updates  chan TGUpdate
}

func NewTGBotSender(ctx context.Context, token, apiURL string) (*tgBotSender, error) {
	s := &tgBotSender{
		token:   token,
		apiURL:  apiURL,
		updates: make(chan TGUpdate, 100),
	}

	opts := []bot.Option{
		bot.WithHTTPClient(25*time.Second, &http.Client{Timeout: 30 * time.Second}),
		bot.WithDefaultHandler(func(ctx context.Context, b *bot.Bot, update *models.Update) {
			tgu := convertUpdate(update)
			select {
			case s.updates <- tgu:
			default:
				slog.Warn("TG update channel full, dropping update")
			}
		}),
	}
	if apiURL != "" {
		opts = append(opts, bot.WithServerURL(apiURL))
	}
	opts = append(opts, bot.WithSkipGetMe())

	b, err := bot.New(token, opts...)
	if err != nil {
		return nil, fmt.Errorf("bot.New: %w", err)
	}
	s.b = b

	me, err := b.GetMe(ctx)
	if err != nil {
		return nil, fmt.Errorf("TG getMe: %w", err)
	}
	s.username = me.Username
	slog.Info("Telegram bot started", "username", me.Username)

	return s, nil
}

func (s *tgBotSender) BotUsername() string { return s.username }
func (s *tgBotSender) BotToken() string    { return s.token }

// --- Updates ---

func (s *tgBotSender) StartPolling(ctx context.Context) <-chan TGUpdate {
	go s.b.Start(ctx)
	return s.updates
}

func (s *tgBotSender) StartWebhook(ctx context.Context, path string) <-chan TGUpdate {
	http.HandleFunc(path, s.b.WebhookHandler())
	go s.b.StartWebhook(ctx) // start workers that dispatch updates to handlers
	return s.updates
}

func (s *tgBotSender) SetWebhook(ctx context.Context, url string) error {
	_, err := s.b.SetWebhook(ctx, &bot.SetWebhookParams{URL: url})
	return wrapErr(err)
}

func (s *tgBotSender) DeleteWebhook(ctx context.Context) error {
	_, err := s.b.DeleteWebhook(ctx, &bot.DeleteWebhookParams{})
	return wrapErr(err)
}

// --- Send ---

func (s *tgBotSender) SendChatAction(ctx context.Context, chatID int64, action string) error {
	_, err := s.b.SendChatAction(ctx, &bot.SendChatActionParams{ChatID: chatID, Action: models.ChatAction(action)})
	return err
}

func (s *tgBotSender) SendMessage(ctx context.Context, chatID int64, text string, opts *SendOpts) (int, error) {
	p := &bot.SendMessageParams{
		ChatID: chatID,
		Text:   text,
	}
	applySendMessageOpts(p, opts)
	var msg *models.Message
	err := retryOn429(ctx, func() error {
		var e error
		msg, e = s.b.SendMessage(ctx, p)
		return e
	})
	if err != nil {
		return 0, wrapErr(err)
	}
	return msg.ID, nil
}

func (s *tgBotSender) SendPhoto(ctx context.Context, chatID int64, file FileArg, opts *SendOpts) (int, error) {
	p := &bot.SendPhotoParams{
		ChatID: chatID,
		Photo:  toInputFile(file),
	}
	applySendPhotoOpts(p, opts)
	msg, err := s.b.SendPhoto(ctx, p)
	if err != nil {
		return 0, wrapErr(err)
	}
	return msg.ID, nil
}

func (s *tgBotSender) SendVideo(ctx context.Context, chatID int64, file FileArg, opts *SendOpts) (int, error) {
	p := &bot.SendVideoParams{
		ChatID: chatID,
		Video:  toInputFile(file),
	}
	applySendVideoOpts(p, opts)
	msg, err := s.b.SendVideo(ctx, p)
	if err != nil {
		return 0, wrapErr(err)
	}
	return msg.ID, nil
}

func (s *tgBotSender) SendAudio(ctx context.Context, chatID int64, file FileArg, opts *SendOpts) (int, error) {
	p := &bot.SendAudioParams{
		ChatID: chatID,
		Audio:  toInputFile(file),
	}
	applySendAudioOpts(p, opts)
	msg, err := s.b.SendAudio(ctx, p)
	if err != nil {
		return 0, wrapErr(err)
	}
	return msg.ID, nil
}

func (s *tgBotSender) SendDocument(ctx context.Context, chatID int64, file FileArg, opts *SendOpts) (int, error) {
	p := &bot.SendDocumentParams{
		ChatID:   chatID,
		Document: toInputFile(file),
	}
	applySendDocumentOpts(p, opts)
	msg, err := s.b.SendDocument(ctx, p)
	if err != nil {
		return 0, wrapErr(err)
	}
	return msg.ID, nil
}

func (s *tgBotSender) SendMediaGroup(ctx context.Context, chatID int64, media []TGInputMedia, opts *SendOpts) ([]int, error) {
	p := &bot.SendMediaGroupParams{ChatID: chatID}
	if opts != nil {
		if opts.ThreadID != 0 {
			p.MessageThreadID = opts.ThreadID
		}
		if opts.ReplyToID != 0 {
			p.ReplyParameters = &models.ReplyParameters{MessageID: opts.ReplyToID}
		}
	}
	var msgs []*models.Message
	err := retryOn429(ctx, func() error {
		// items пересобираем на каждой попытке: у upload-байтов io.Reader одноразовый.
		items := make([]models.InputMedia, 0, len(media))
		for i, m := range media {
			// Уникальное имя attach:// на элемент. Иначе (одинаковое/пустое имя у всех
			// байтовых аплоадов, напр. фото с MAX-CDN) multipart-парты схлопываются и
			// ВЕСЬ альбом становится первым фото. Только для upload (у URL/file_id имя не нужно).
			if len(m.File.Bytes) > 0 {
				m.File.Name = fmt.Sprintf("m%d_%s", i, m.File.Name)
			}
			items = append(items, toLibInputMedia(m))
		}
		p.Media = items
		var e error
		msgs, e = s.b.SendMediaGroup(ctx, p)
		return e
	})
	if err != nil {
		return nil, wrapErr(err)
	}
	ids := make([]int, len(msgs))
	for i, m := range msgs {
		ids[i] = m.ID
	}
	return ids, nil
}

// --- Edit ---

func (s *tgBotSender) EditMessageText(ctx context.Context, chatID int64, msgID int, text string, opts *SendOpts) error {
	p := &bot.EditMessageTextParams{
		ChatID:    chatID,
		MessageID: msgID,
		Text:      text,
	}
	if opts != nil {
		if opts.ParseMode != "" {
			p.ParseMode = models.ParseMode(opts.ParseMode)
		}
		if opts.ReplyMarkup != nil {
			p.ReplyMarkup = toLibKeyboard(opts.ReplyMarkup)
		}
	}
	_, err := s.b.EditMessageText(ctx, p)
	return wrapErr(err)
}

func (s *tgBotSender) EditMessageCaption(ctx context.Context, chatID int64, msgID int, caption string, opts *SendOpts) error {
	p := &bot.EditMessageCaptionParams{
		ChatID:    chatID,
		MessageID: msgID,
		Caption:   caption,
	}
	if opts != nil {
		if opts.ParseMode != "" {
			p.ParseMode = models.ParseMode(opts.ParseMode)
		}
		if opts.ReplyMarkup != nil {
			p.ReplyMarkup = toLibKeyboard(opts.ReplyMarkup)
		}
	}
	_, err := s.b.EditMessageCaption(ctx, p)
	return wrapErr(err)
}

func (s *tgBotSender) EditMessageMedia(ctx context.Context, chatID int64, msgID int, media TGInputMedia) error {
	p := &bot.EditMessageMediaParams{
		ChatID:    chatID,
		MessageID: msgID,
		Media:     toLibInputMedia(media),
	}
	_, err := s.b.EditMessageMedia(ctx, p)
	return wrapErr(err)
}

// --- Other ---

func (s *tgBotSender) DeleteMessage(ctx context.Context, chatID int64, msgID int) error {
	_, err := s.b.DeleteMessage(ctx, &bot.DeleteMessageParams{
		ChatID:    chatID,
		MessageID: msgID,
	})
	return wrapErr(err)
}

func (s *tgBotSender) AnswerCallback(ctx context.Context, callbackID string, text string) error {
	_, err := s.b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
		CallbackQueryID: callbackID,
		Text:            text,
	})
	return wrapErr(err)
}

func (s *tgBotSender) GetFile(ctx context.Context, fileID string) (string, error) {
	f, err := s.b.GetFile(ctx, &bot.GetFileParams{FileID: fileID})
	if err != nil {
		return "", wrapErr(err)
	}
	return f.FilePath, nil
}

func (s *tgBotSender) GetFileDirectURL(filePath string) string {
	// В режиме --local локальный Bot API возвращает АБСОЛЮТНЫЙ путь к файлу
	// (например /var/lib/telegram-bot-api/<token>/videos/file_5.mp4), а nginx
	// раздаёт файлы по относительному пути (videos/file_5.mp4). Срезаем префикс
	// до токена включительно. Для относительных путей (без --local) это no-op.
	if strings.HasPrefix(filePath, "/") {
		if i := strings.LastIndex(filePath, s.token+"/"); i >= 0 {
			filePath = filePath[i+len(s.token)+1:]
		}
	}
	if s.apiURL != "" {
		return s.apiURL + "/" + filePath
	}
	return "https://api.telegram.org/file/bot" + s.token + "/" + filePath
}

func (s *tgBotSender) GetChatMember(ctx context.Context, chatID, userID int64) (string, error) {
	m, err := s.b.GetChatMember(ctx, &bot.GetChatMemberParams{
		ChatID: chatID,
		UserID: userID,
	})
	if err != nil {
		return "", wrapErr(err)
	}
	return string(m.Type), nil
}

// CopyMessages копирует сообщения без плашки «переслано». Один id — copyMessage,
// несколько — copyMessages (части альбома остаются одним альбомом).
func (s *tgBotSender) CopyMessages(ctx context.Context, dstChatID, srcChatID int64, msgIDs []int) error {
	if len(msgIDs) == 0 {
		return nil
	}
	if len(msgIDs) == 1 {
		_, err := s.b.CopyMessage(ctx, &bot.CopyMessageParams{
			ChatID:     dstChatID,
			FromChatID: srcChatID,
			MessageID:  msgIDs[0],
		})
		return wrapErr(err)
	}
	_, err := s.b.CopyMessages(ctx, &bot.CopyMessagesParams{
		ChatID:     dstChatID,
		FromChatID: srcChatID,
		MessageIDs: msgIDs,
	})
	return wrapErr(err)
}

func (s *tgBotSender) RestrictChatMember(ctx context.Context, chatID, userID int64, untilUnix int) error {
	// Все права false = полный мут (не может писать). untilUnix=0 ⇒ навсегда.
	_, err := s.b.RestrictChatMember(ctx, &bot.RestrictChatMemberParams{
		ChatID:      chatID,
		UserID:      userID,
		Permissions: &models.ChatPermissions{},
		UntilDate:   untilUnix,
	})
	return wrapErr(err)
}

// UnrestrictChatMember возвращает базовые права писать (снятие мута капчи).
func (s *tgBotSender) UnrestrictChatMember(ctx context.Context, chatID, userID int64) error {
	_, err := s.b.RestrictChatMember(ctx, &bot.RestrictChatMemberParams{
		ChatID: chatID,
		UserID: userID,
		Permissions: &models.ChatPermissions{
			CanSendMessages:       true,
			CanSendAudios:         true,
			CanSendDocuments:      true,
			CanSendPhotos:         true,
			CanSendVideos:         true,
			CanSendVideoNotes:     true,
			CanSendVoiceNotes:     true,
			CanSendPolls:          true,
			CanSendOtherMessages:  true,
			CanAddWebPagePreviews: true,
			CanInviteUsers:        true,
		},
	})
	return wrapErr(err)
}

func (s *tgBotSender) BanChatMember(ctx context.Context, chatID, userID int64) error {
	_, err := s.b.BanChatMember(ctx, &bot.BanChatMemberParams{
		ChatID:         chatID,
		UserID:         userID,
		RevokeMessages: true,
	})
	return wrapErr(err)
}

// UnbanChatMember — снять бан (OnlyIfBanned: не кикает, если участник в чате).
func (s *tgBotSender) UnbanChatMember(ctx context.Context, chatID, userID int64) error {
	_, err := s.b.UnbanChatMember(ctx, &bot.UnbanChatMemberParams{
		ChatID:       chatID,
		UserID:       userID,
		OnlyIfBanned: true,
	})
	return wrapErr(err)
}

// KickChatMember — выгнать с возможностью вернуться (ban + сразу unban).
func (s *tgBotSender) KickChatMember(ctx context.Context, chatID, userID int64) error {
	if _, err := s.b.BanChatMember(ctx, &bot.BanChatMemberParams{
		ChatID: chatID, UserID: userID, RevokeMessages: true,
	}); err != nil {
		return wrapErr(err)
	}
	_, err := s.b.UnbanChatMember(ctx, &bot.UnbanChatMemberParams{
		ChatID: chatID, UserID: userID, OnlyIfBanned: true,
	})
	return wrapErr(err)
}

func (s *tgBotSender) SetMyCommands(ctx context.Context, commands []BotCommand, scope *CommandScope) error {
	cmds := make([]models.BotCommand, len(commands))
	for i, c := range commands {
		cmds[i] = models.BotCommand{Command: c.Command, Description: c.Description}
	}
	p := &bot.SetMyCommandsParams{Commands: cmds}
	if scope != nil && scope.Type == "all_chat_administrators" {
		p.Scope = &models.BotCommandScopeAllChatAdministrators{}
	}
	_, err := s.b.SetMyCommands(ctx, p)
	return wrapErr(err)
}

func (s *tgBotSender) SetMyDescription(ctx context.Context, description string) error {
	_, err := s.b.SetMyDescription(ctx, &bot.SetMyDescriptionParams{Description: description})
	return wrapErr(err)
}

// SetMenuButtonWebApp ставит кнопку «Меню» бота как открытие веб-аппа (кабинета).
func (s *tgBotSender) SetMenuButtonWebApp(ctx context.Context, text, url string) error {
	_, err := s.b.SetChatMenuButton(ctx, &bot.SetChatMenuButtonParams{
		MenuButton: models.MenuButtonWebApp{
			Type:   models.MenuButtonTypeWebApp,
			Text:   text,
			WebApp: models.WebAppInfo{URL: url},
		},
	})
	return wrapErr(err)
}

func (s *tgBotSender) ForwardMessage(ctx context.Context, fromChatID, toChatID int64, msgID int, silent bool) (*TGMessage, error) {
	m, err := s.b.ForwardMessage(ctx, &bot.ForwardMessageParams{
		ChatID:              toChatID,
		FromChatID:          fromChatID,
		MessageID:           msgID,
		DisableNotification: silent,
	})
	if err != nil {
		return nil, wrapErr(err)
	}
	return convertMsg(m), nil
}

func (s *tgBotSender) GetChat(ctx context.Context, chatID int64) (string, error) {
	chat, err := s.b.GetChat(ctx, &bot.GetChatParams{ChatID: chatID})
	if err != nil {
		return "", wrapErr(err)
	}
	return chat.Title, nil
}

// GetUserPersonalChannel — личный канал из профиля юзера (Bot API personal_chat).
// Описание канала getChat(user) не отдаёт — добираем вторым getChat по id канала.
func (s *tgBotSender) GetUserPersonalChannel(ctx context.Context, userID int64) (string, string, bool) {
	info, err := s.b.GetChat(ctx, &bot.GetChatParams{ChatID: userID})
	if err != nil || info.PersonalChat == nil {
		return "", "", false
	}
	title := info.PersonalChat.Title
	description := ""
	if full, err := s.b.GetChat(ctx, &bot.GetChatParams{ChatID: info.PersonalChat.ID}); err == nil {
		description = full.Description
	}
	return title, description, true
}

// --- Conversion helpers ---

// tgUserRef — читаемая ссылка на юзера: "@username" либо числовой id.
func tgUserRef(username string, id int64) string {
	if username != "" {
		return "@" + username
	}
	return strconv.FormatInt(id, 10)
}

func toInputFile(f FileArg) models.InputFile {
	if f.URL != "" {
		return &models.InputFileString{Data: f.URL}
	}
	name := f.Name
	if name == "" {
		name = "file"
	}
	return &models.InputFileUpload{Filename: name, Data: bytes.NewReader(f.Bytes)}
}

func toLibInputMedia(m TGInputMedia) models.InputMedia {
	pm := models.ParseMode(m.ParseMode)

	// InputMedia structs use string Media field (URL or file_id) plus
	// an io.Reader MediaAttachment for uploads.
	if m.File.URL != "" {
		// URL or file_id — set Media string directly, no attachment.
		switch m.Type {
		case "video":
			return &models.InputMediaVideo{Media: m.File.URL, Caption: m.Caption, ParseMode: pm}
		case "audio":
			return &models.InputMediaAudio{Media: m.File.URL, Caption: m.Caption, ParseMode: pm}
		case "document":
			return &models.InputMediaDocument{Media: m.File.URL, Caption: m.Caption, ParseMode: pm}
		default:
			return &models.InputMediaPhoto{Media: m.File.URL, Caption: m.Caption, ParseMode: pm}
		}
	}

	// Upload — use attach:// protocol with MediaAttachment reader.
	name := m.File.Name
	if name == "" {
		name = "file"
	}
	media := "attach://" + name
	reader := bytes.NewReader(m.File.Bytes)
	switch m.Type {
	case "video":
		return &models.InputMediaVideo{Media: media, Caption: m.Caption, ParseMode: pm, MediaAttachment: reader}
	case "audio":
		return &models.InputMediaAudio{Media: media, Caption: m.Caption, ParseMode: pm, MediaAttachment: reader}
	case "document":
		return &models.InputMediaDocument{Media: media, Caption: m.Caption, ParseMode: pm, MediaAttachment: reader}
	default:
		return &models.InputMediaPhoto{Media: media, Caption: m.Caption, ParseMode: pm, MediaAttachment: reader}
	}
}

func toLibKeyboard(kb *InlineKeyboardMarkup) *models.InlineKeyboardMarkup {
	if kb == nil {
		return nil
	}
	rows := make([][]models.InlineKeyboardButton, len(kb.Rows))
	for i, row := range kb.Rows {
		btns := make([]models.InlineKeyboardButton, len(row))
		for j, b := range row {
			switch {
			case b.WebAppURL != "":
				btns[j] = models.InlineKeyboardButton{Text: b.Text, WebApp: &models.WebAppInfo{URL: b.WebAppURL}}
			case b.URL != "":
				btns[j] = models.InlineKeyboardButton{Text: b.Text, URL: b.URL}
			default:
				btns[j] = models.InlineKeyboardButton{Text: b.Text, CallbackData: b.CallbackData}
			}
		}
		rows[i] = btns
	}
	return &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// --- Apply opts helpers ---

func applySendMessageOpts(p *bot.SendMessageParams, opts *SendOpts) {
	if opts == nil {
		return
	}
	if opts.ThreadID != 0 {
		p.MessageThreadID = opts.ThreadID
	}
	if opts.ParseMode != "" {
		p.ParseMode = models.ParseMode(opts.ParseMode)
	}
	if opts.ReplyToID != 0 {
		p.ReplyParameters = &models.ReplyParameters{MessageID: opts.ReplyToID}
	}
	if opts.ReplyMarkup != nil {
		p.ReplyMarkup = toLibKeyboard(opts.ReplyMarkup)
	}
	if opts.RemoveKeyboard {
		p.ReplyMarkup = &models.ReplyKeyboardRemove{RemoveKeyboard: true}
	}
	if opts.RequestChat != nil {
		var userRights, botRights *models.ChatAdministratorRights
		if opts.RequestChat.RequireAdmin {
			rights := &models.ChatAdministratorRights{CanDeleteMessages: true, CanRestrictMembers: true}
			userRights, botRights = rights, rights
		}
		p.ReplyMarkup = &models.ReplyKeyboardMarkup{
			ResizeKeyboard:  true,
			OneTimeKeyboard: true,
			Keyboard: [][]models.KeyboardButton{{{
				Text: opts.RequestChat.Text,
				RequestChat: &models.KeyboardButtonRequestChat{
					RequestID:               int32(opts.RequestChat.RequestID),
					ChatIsChannel:           false,
					UserAdministratorRights: userRights,
					BotAdministratorRights:  botRights,
					BotIsMember:             opts.RequestChat.BotIsMember,
					RequestTitle:            true,
				},
			}}},
		}
	}
}

func applySendPhotoOpts(p *bot.SendPhotoParams, opts *SendOpts) {
	if opts == nil {
		return
	}
	if opts.ThreadID != 0 {
		p.MessageThreadID = opts.ThreadID
	}
	if opts.Caption != "" {
		p.Caption = opts.Caption
	}
	if opts.ParseMode != "" {
		p.ParseMode = models.ParseMode(opts.ParseMode)
	}
	if opts.ReplyToID != 0 {
		p.ReplyParameters = &models.ReplyParameters{MessageID: opts.ReplyToID}
	}
	if opts.ReplyMarkup != nil {
		p.ReplyMarkup = toLibKeyboard(opts.ReplyMarkup)
	}
}

func applySendVideoOpts(p *bot.SendVideoParams, opts *SendOpts) {
	if opts == nil {
		return
	}
	if opts.ThreadID != 0 {
		p.MessageThreadID = opts.ThreadID
	}
	if opts.Caption != "" {
		p.Caption = opts.Caption
	}
	if opts.ParseMode != "" {
		p.ParseMode = models.ParseMode(opts.ParseMode)
	}
	if opts.ReplyToID != 0 {
		p.ReplyParameters = &models.ReplyParameters{MessageID: opts.ReplyToID}
	}
	if opts.ReplyMarkup != nil {
		p.ReplyMarkup = toLibKeyboard(opts.ReplyMarkup)
	}
}

func applySendAudioOpts(p *bot.SendAudioParams, opts *SendOpts) {
	if opts == nil {
		return
	}
	if opts.ThreadID != 0 {
		p.MessageThreadID = opts.ThreadID
	}
	if opts.Caption != "" {
		p.Caption = opts.Caption
	}
	if opts.ParseMode != "" {
		p.ParseMode = models.ParseMode(opts.ParseMode)
	}
	if opts.ReplyToID != 0 {
		p.ReplyParameters = &models.ReplyParameters{MessageID: opts.ReplyToID}
	}
	if opts.ReplyMarkup != nil {
		p.ReplyMarkup = toLibKeyboard(opts.ReplyMarkup)
	}
}

func applySendDocumentOpts(p *bot.SendDocumentParams, opts *SendOpts) {
	if opts == nil {
		return
	}
	if opts.ThreadID != 0 {
		p.MessageThreadID = opts.ThreadID
	}
	if opts.Caption != "" {
		p.Caption = opts.Caption
	}
	if opts.ParseMode != "" {
		p.ParseMode = models.ParseMode(opts.ParseMode)
	}
	if opts.ReplyToID != 0 {
		p.ReplyParameters = &models.ReplyParameters{MessageID: opts.ReplyToID}
	}
	if opts.ReplyMarkup != nil {
		p.ReplyMarkup = toLibKeyboard(opts.ReplyMarkup)
	}
}

// --- Error wrapping ---

// retryOn429 выполняет send, повторяя при 429 с учётом retry_after (Telegram при
// массовом постинге лимитит ~20 сообщений/мин на чат). send пересобирает параметры
// внутри себя, если в них есть одноразовые io.Reader (upload-байты media group).
func retryOn429(ctx context.Context, send func() error) error {
	const maxTries = 4
	for attempt := 0; ; attempt++ {
		err := send()
		if err == nil {
			return nil
		}
		var tmr *bot.TooManyRequestsError
		if !errors.As(err, &tmr) || attempt >= maxTries {
			return err
		}
		wait := time.Duration(tmr.RetryAfter)*time.Second + 300*time.Millisecond
		if wait <= 0 {
			wait = time.Second
		}
		if wait > 30*time.Second {
			wait = 30 * time.Second
		}
		slog.Warn("TG 429 rate-limit, backing off", "retryAfter", tmr.RetryAfter, "attempt", attempt+1)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

func wrapErr(err error) error {
	if err == nil {
		return nil
	}
	var me *bot.MigrateError
	if errors.As(err, &me) {
		return &TGError{
			Code:            400,
			Description:     me.Message,
			MigrateToChatID: int64(me.MigrateToChatID),
		}
	}
	if errors.Is(err, bot.ErrorForbidden) {
		return &TGError{Code: 403, Description: err.Error()}
	}
	if errors.Is(err, bot.ErrorBadRequest) {
		return &TGError{Code: 400, Description: err.Error()}
	}
	if errors.Is(err, bot.ErrorNotFound) {
		return &TGError{Code: 404, Description: err.Error()}
	}
	var tmr *bot.TooManyRequestsError
	if errors.As(err, &tmr) {
		return &TGError{Code: 429, Description: tmr.Error()}
	}
	return err
}

// --- Update conversion ---

func convertUpdate(u *models.Update) TGUpdate {
	return TGUpdate{
		Message:           convertMsg(u.Message),
		EditedMessage:     convertMsg(u.EditedMessage),
		ChannelPost:       convertMsg(u.ChannelPost),
		EditedChannelPost: convertMsg(u.EditedChannelPost),
		CallbackQuery:     convertCallback(u.CallbackQuery),
	}
}

func convertMsg(m *models.Message) *TGMessage {
	if m == nil {
		return nil
	}
	msg := &TGMessage{
		MessageID:       m.ID,
		MessageThreadID: m.MessageThreadID,
		Chat: ChatInfo{
			ID:    m.Chat.ID,
			Type:  string(m.Chat.Type),
			Title: m.Chat.Title,
		},
		Text:            m.Text,
		Caption:         m.Caption,
		MediaGroupID:    m.MediaGroupID,
		MigrateToChatID: m.MigrateToChatID,
	}

	// Служебные сообщения (вступил/вышел/смена названия/закреп и т.п.) — без
	// контента; не зеркалим их в MAX (иначе летит пустое сообщение «Имя:»).
	if len(m.NewChatMembers) > 0 || m.LeftChatMember != nil || m.NewChatTitle != "" ||
		len(m.NewChatPhoto) > 0 || m.DeleteChatPhoto || m.GroupChatCreated ||
		m.SupergroupChatCreated || m.ChannelChatCreated ||
		m.MessageAutoDeleteTimerChanged != nil || m.PinnedMessage != nil ||
		m.ForumTopicCreated != nil || m.ForumTopicEdited != nil ||
		m.ForumTopicClosed != nil || m.ForumTopicReopened != nil ||
		m.GeneralForumTopicHidden != nil || m.GeneralForumTopicUnhidden != nil ||
		m.GiveawayCreated != nil || m.GiveawayCompleted != nil ||
		m.BoostAdded != nil || m.WriteAccessAllowed != nil || m.ProximityAlertTriggered != nil {
		msg.IsService = true
	}
	msg.IsAutomaticForward = m.IsAutomaticForward

	if m.ChatShared != nil {
		msg.ChatShared = &ChatSharedInfo{
			RequestID: m.ChatShared.RequestID,
			ChatID:    m.ChatShared.ChatID,
			Title:     m.ChatShared.Title,
		}
		msg.IsService = true // не зеркалим служебное chat_shared
	}

	if m.From != nil {
		msg.From = &UserInfo{
			ID:        m.From.ID,
			IsBot:     m.From.IsBot,
			UserName:  m.From.Username,
			FirstName: m.From.FirstName,
			LastName:  m.From.LastName,
		}
	}

	if m.SenderChat != nil {
		msg.SenderChat = &ChatInfo{
			ID:    m.SenderChat.ID,
			Type:  string(m.SenderChat.Type),
			Title: m.SenderChat.Title,
		}
	}

	// ForwardOrigin -> ForwardOriginChat (for channel forwards)
	if m.ForwardOrigin != nil && m.ForwardOrigin.MessageOriginChannel != nil {
		ch := m.ForwardOrigin.MessageOriginChannel.Chat
		msg.ForwardOriginChat = &ChatInfo{
			ID:    ch.ID,
			Type:  string(ch.Type),
			Title: ch.Title,
		}
		msg.ForwardOriginMsgID = m.ForwardOrigin.MessageOriginChannel.MessageID
		msg.ForwardOriginDate = m.ForwardOrigin.MessageOriginChannel.Date
	}

	// Имя источника форварда (для метки «Переслано из X») — все типы origin.
	if m.ForwardOrigin != nil {
		switch {
		case m.ForwardOrigin.MessageOriginChannel != nil:
			msg.ForwardFrom = m.ForwardOrigin.MessageOriginChannel.Chat.Title
		case m.ForwardOrigin.MessageOriginChat != nil:
			msg.ForwardFrom = m.ForwardOrigin.MessageOriginChat.SenderChat.Title
		case m.ForwardOrigin.MessageOriginUser != nil:
			u := m.ForwardOrigin.MessageOriginUser.SenderUser
			msg.ForwardFrom = strings.TrimSpace(u.FirstName + " " + u.LastName)
		case m.ForwardOrigin.MessageOriginHiddenUser != nil:
			msg.ForwardFrom = m.ForwardOrigin.MessageOriginHiddenUser.SenderUserName
		}
	}

	// Через/от бота: via_bot либо forward_origin от юзера-бота (скам-карточки «опрос за 2000₽»).
	if m.ViaBot != nil {
		msg.BotForward = tgUserRef(m.ViaBot.Username, m.ViaBot.ID)
	}
	if msg.BotForward == "" && m.ForwardOrigin != nil && m.ForwardOrigin.MessageOriginUser != nil &&
		m.ForwardOrigin.MessageOriginUser.SenderUser.IsBot {
		u := m.ForwardOrigin.MessageOriginUser.SenderUser
		msg.BotForward = tgUserRef(u.Username, u.ID)
	}

	// Photo
	for _, p := range m.Photo {
		msg.Photo = append(msg.Photo, PhotoSize{
			FileID:   p.FileID,
			FileSize: p.FileSize,
		})
	}

	if m.Video != nil {
		msg.Video = &FileInfo{FileID: m.Video.FileID, FileName: m.Video.FileName, FileSize: int(m.Video.FileSize)}
	}
	if m.Document != nil {
		msg.Document = &DocInfo{FileID: m.Document.FileID, FileName: m.Document.FileName, FileSize: int(m.Document.FileSize), MimeType: m.Document.MimeType}
	}
	if m.Animation != nil {
		msg.Animation = &FileInfo{FileID: m.Animation.FileID, FileName: m.Animation.FileName, FileSize: int(m.Animation.FileSize)}
	}
	if m.Sticker != nil {
		msg.Sticker = &StickerInfo{FileID: m.Sticker.FileID, FileSize: m.Sticker.FileSize, IsAnimated: m.Sticker.IsAnimated}
	}
	if m.Voice != nil {
		msg.Voice = &FileInfo{FileID: m.Voice.FileID, FileSize: int(m.Voice.FileSize)}
	}
	if m.Audio != nil {
		msg.Audio = &AudioInfo{FileID: m.Audio.FileID, FileName: m.Audio.FileName, FileSize: int(m.Audio.FileSize)}
	}
	if m.VideoNote != nil {
		msg.VideoNote = &FileInfo{FileID: m.VideoNote.FileID, FileSize: m.VideoNote.FileSize}
	}

	if m.ReplyToMessage != nil {
		msg.ReplyToMessage = convertMsg(m.ReplyToMessage)
	}

	for _, u := range m.NewChatMembers {
		msg.NewChatMembers = append(msg.NewChatMembers, UserInfo{
			ID: u.ID, IsBot: u.IsBot, UserName: u.Username, FirstName: u.FirstName, LastName: u.LastName,
		})
	}
	if m.LeftChatMember != nil {
		msg.LeftChatMember = &UserInfo{
			ID: m.LeftChatMember.ID, IsBot: m.LeftChatMember.IsBot, UserName: m.LeftChatMember.Username,
			FirstName: m.LeftChatMember.FirstName, LastName: m.LeftChatMember.LastName,
		}
	}

	if m.Contact != nil {
		msg.Contact = &ContactInfo{
			PhoneNumber: m.Contact.PhoneNumber,
			FirstName:   m.Contact.FirstName,
			LastName:    m.Contact.LastName,
			UserID:      m.Contact.UserID,
		}
	}

	// Цитата на сообщение из другого чата/канала (external_reply) — приём спама.
	// Собираем пейлоад (цитата + название источника + ссылка) для внешней проверки.
	if m.Quote != nil {
		msg.ExternalReplyText = m.Quote.Text
	}
	if m.ExternalReply != nil {
		msg.HasExternalReply = true
		parts := []string{msg.ExternalReplyText}
		if m.ExternalReply.Chat != nil && m.ExternalReply.Chat.Title != "" {
			parts = append(parts, m.ExternalReply.Chat.Title)
		}
		if ch := m.ExternalReply.Origin.MessageOriginChannel; ch != nil {
			parts = append(parts, ch.Chat.Title)
			if ch.AuthorSignature != nil {
				parts = append(parts, *ch.AuthorSignature)
			}
		}
		if lp := m.ExternalReply.LinkPreviewOptions; lp != nil && lp.URL != nil {
			parts = append(parts, *lp.URL)
		}
		msg.ExternalReplyText = strings.TrimSpace(strings.Join(parts, " "))
	}

	for _, e := range m.Entities {
		msg.Entities = append(msg.Entities, Entity{Type: string(e.Type), Offset: e.Offset, Length: e.Length, URL: e.URL})
	}
	for _, e := range m.CaptionEntities {
		msg.CaptionEntities = append(msg.CaptionEntities, Entity{Type: string(e.Type), Offset: e.Offset, Length: e.Length, URL: e.URL})
	}

	return msg
}

func convertCallback(cb *models.CallbackQuery) *TGCallback {
	if cb == nil {
		return nil
	}
	c := &TGCallback{
		ID:   cb.ID,
		Data: cb.Data,
	}
	if cb.From.ID != 0 {
		c.From = &UserInfo{
			ID:        cb.From.ID,
			IsBot:     cb.From.IsBot,
			UserName:  cb.From.Username,
			FirstName: cb.From.FirstName,
			LastName:  cb.From.LastName,
		}
	}
	if cb.Message.Message != nil {
		c.Message = convertMsg(cb.Message.Message)
	}
	return c
}
