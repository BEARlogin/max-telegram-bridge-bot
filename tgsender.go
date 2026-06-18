package main

import (
	"context"
	"fmt"
)

// --- Custom types for TG adapter ---

type ChatInfo struct {
	ID    int64
	Type  string
	Title string
}

type UserInfo struct {
	ID        int64
	IsBot     bool
	UserName  string
	FirstName string
	LastName  string
}

type PhotoSize struct {
	FileID   string
	FileSize int
}

type FileInfo struct {
	FileID   string
	FileName string
	FileSize int
}

type DocInfo struct {
	FileID   string
	FileName string
	FileSize int
	MimeType string
}

type AudioInfo struct {
	FileID   string
	FileName string
	FileSize int
}

type StickerInfo struct {
	FileID     string
	FileSize   int
	IsAnimated bool
}

type Entity struct {
	Type   string
	Offset int
	Length int
	URL    string
}

type TGMessage struct {
	MessageID       int
	MessageThreadID int
	Chat            ChatInfo
	From            *UserInfo
	SenderChat      *ChatInfo
	Text            string
	Caption         string
	Photo           []PhotoSize
	Video           *FileInfo
	Document        *DocInfo
	Animation       *FileInfo
	Sticker         *StickerInfo
	Voice           *FileInfo
	Audio           *AudioInfo
	VideoNote       *FileInfo
	MediaGroupID    string
	ReplyToMessage  *TGMessage
	ForwardOriginChat  *ChatInfo // replaces ForwardFromChat, from forward_origin
	ForwardOriginMsgID int       // msg_id оригинала в канале-источнике (для старта импорта с поста)
	MigrateToChatID    int64
	Entities        []Entity
	CaptionEntities []Entity
}

type TGCallback struct {
	ID      string
	From    *UserInfo
	Message *TGMessage
	Data    string
}

type TGUpdate struct {
	Message           *TGMessage
	EditedMessage     *TGMessage
	ChannelPost       *TGMessage
	EditedChannelPost *TGMessage
	CallbackQuery     *TGCallback
}

// SendOpts — optional parameters for send methods.
type SendOpts struct {
	ThreadID    int
	ReplyToID   int
	ParseMode   string
	Caption     string
	ReplyMarkup *InlineKeyboardMarkup
}

type InlineKeyboardMarkup struct {
	Rows [][]InlineKeyboardButton
}

type InlineKeyboardButton struct {
	Text         string
	CallbackData string
	URL          string // если задан — кнопка-ссылка (вместо callback)
}

// FileArg — source for file upload: either Bytes (upload) or URL (send from URL).
type FileArg struct {
	Name  string
	Bytes []byte
	URL   string
}

// TGInputMedia — item for media groups and edit-media.
type TGInputMedia struct {
	Type      string // "photo", "video", "audio", "document"
	File      FileArg
	Caption   string
	ParseMode string
}

type BotCommand struct {
	Command     string
	Description string
}

type CommandScope struct {
	Type string // "", "all_chat_administrators"
}

// TGError represents a Telegram API error.
type TGError struct {
	Code            int
	Description     string
	MigrateToChatID int64
}

func (e *TGError) Error() string {
	return fmt.Sprintf("telegram: %s (%d)", e.Description, e.Code)
}

// --- Keyboard helpers ---

func NewInlineKeyboard(rows ...[]InlineKeyboardButton) *InlineKeyboardMarkup {
	return &InlineKeyboardMarkup{Rows: rows}
}

func NewInlineRow(buttons ...InlineKeyboardButton) []InlineKeyboardButton {
	return buttons
}

func NewInlineButton(text, data string) InlineKeyboardButton {
	return InlineKeyboardButton{Text: text, CallbackData: data}
}

// --- Interface ---

// TGSender abstracts Telegram Bot API. All TG calls go through this interface.
type TGSender interface {
	// Send methods return message ID.
	SendMessage(ctx context.Context, chatID int64, text string, opts *SendOpts) (int, error)
	SendPhoto(ctx context.Context, chatID int64, file FileArg, opts *SendOpts) (int, error)
	SendVideo(ctx context.Context, chatID int64, file FileArg, opts *SendOpts) (int, error)
	SendAudio(ctx context.Context, chatID int64, file FileArg, opts *SendOpts) (int, error)
	SendDocument(ctx context.Context, chatID int64, file FileArg, opts *SendOpts) (int, error)
	SendMediaGroup(ctx context.Context, chatID int64, media []TGInputMedia, opts *SendOpts) ([]int, error)

	EditMessageText(ctx context.Context, chatID int64, msgID int, text string, opts *SendOpts) error
	EditMessageMedia(ctx context.Context, chatID int64, msgID int, media TGInputMedia) error

	DeleteMessage(ctx context.Context, chatID int64, msgID int) error
	AnswerCallback(ctx context.Context, callbackID string, text string) error

	// ForwardMessage пересылает сообщение и возвращает полный Message с контентом.
	// Используется аддоном импорта канала для извлечения контента старых постов
	// (Bot API не имеет getMessage, но forwardMessage в ответе отдаёт весь Message).
	ForwardMessage(ctx context.Context, fromChatID, toChatID int64, msgID int, silent bool) (*TGMessage, error)

	GetFile(ctx context.Context, fileID string) (filePath string, err error)
	GetFileDirectURL(filePath string) string
	GetChatMember(ctx context.Context, chatID, userID int64) (status string, err error)
	SetMyCommands(ctx context.Context, commands []BotCommand, scope *CommandScope) error
	GetChat(ctx context.Context, chatID int64) (title string, err error)

	SetWebhook(ctx context.Context, url string) error
	DeleteWebhook(ctx context.Context) error
	StartWebhook(ctx context.Context, path string) <-chan TGUpdate
	StartPolling(ctx context.Context) <-chan TGUpdate

	BotUsername() string
	BotToken() string
}
