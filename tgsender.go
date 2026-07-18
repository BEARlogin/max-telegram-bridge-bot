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

// ContactInfo — контакт (sharing телефона).
type ContactInfo struct {
	PhoneNumber string
	FirstName   string
	LastName    string
	UserID      int64
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
	MessageID          int
	MessageThreadID    int
	Chat               ChatInfo
	From               *UserInfo
	SenderChat         *ChatInfo
	Text               string
	Caption            string
	Photo              []PhotoSize
	Video              *FileInfo
	Document           *DocInfo
	Animation          *FileInfo
	Sticker            *StickerInfo
	Voice              *FileInfo
	Audio              *AudioInfo
	VideoNote          *FileInfo
	MediaGroupID       string
	ReplyToMessage     *TGMessage
	ForwardOriginChat  *ChatInfo // replaces ForwardFromChat, from forward_origin
	ForwardFrom        string    // отображаемое имя источника форварда (канал/группа/юзер/скрытый), "" если не форвард
	ForwardOriginMsgID int       // msg_id оригинала в канале-источнике
	ForwardOriginDate  int       // дата оригинала (unix) — у постов одного альбома совпадает
	// BotForward — сообщение отправлено через инлайн-бота (via_bot) или переслано ОТ бота
	// (forward_origin user с is_bot). Ссылка вида "@username"/id, "" если не бот. Типовой
	// канал происхождения автоматического сообщения — сигнал внешней проверке.
	BotForward      string
	MigrateToChatID int64
	Entities        []Entity
	CaptionEntities []Entity
	// IsService — служебное сообщение (вступил/вышел из чата, смена названия,
	// закреп и т.п.). Такие не зеркалим в MAX (иначе летит пустое сообщение).
	IsService bool
	// IsAutomaticForward — авто-форвард поста канала в его группу обсуждения
	// (Telegram ставит is_automatic_forward). По нему строим маппинг пост↔тред.
	IsAutomaticForward bool
	// HasExternalReply — сообщение цитирует пост из ДРУГОГО чата/канала (external_reply).
	// Частый приём спама: чистая наживка + цитата скам-канала со ссылкой (текст чистый,
	// пейлоад в цитате). ExternalReplyText — текст цитаты + название источника + ссылка.
	HasExternalReply  bool
	ExternalReplyText string
	// NewChatMembers — вступившие в группу (для капчи/анти-рейда).
	NewChatMembers []UserInfo
	// LeftChatMember — вышедший/удалённый участник (чистим его pending-капчу сразу).
	LeftChatMember *UserInfo
	// Contact — пересланный/отправленный контакт (телефон). Без текста — иначе зеркало пустое.
	Contact *ContactInfo
	// ChatShared — пользователь выбрал чат через нативную кнопку KeyboardButtonRequestChat
	// (флоу выбора группы из лички). Содержит chat_id выбранной группы.
	ChatShared *ChatSharedInfo
}

// ChatSharedInfo — результат нативного выбора чата (chat_shared service message).
type ChatSharedInfo struct {
	RequestID int
	ChatID    int64
	Title     string
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
	// RequestChat — если задан, к сообщению крепится reply-клавиатура с нативной
	// кнопкой выбора группы (KeyboardButtonRequestChat). После выбора Telegram
	// присылает chat_shared с chat_id.
	RequestChat *RequestChatSpec
	// RemoveKeyboard — убрать reply-клавиатуру (ReplyKeyboardRemove).
	RemoveKeyboard bool
}

// RequestChatSpec — параметры нативной кнопки выбора группы. Просим бота сделать
// админом с правами модерации.
type RequestChatSpec struct {
	Text         string // подпись кнопки
	RequestID    int    // эхо в chat_shared.request_id (различать запросы)
	RequireAdmin bool   // пользователь и бот должны стать администраторами
	BotIsMember  bool   // показывать только группы, где бот уже состоит
}

type InlineKeyboardMarkup struct {
	Rows [][]InlineKeyboardButton
}

type InlineKeyboardButton struct {
	Text         string
	CallbackData string
	URL          string // если задан — кнопка-ссылка (вместо callback)
	WebAppURL    string // если задан — кнопка открывает web_app (мини-апп с initData)
}

// NewWebAppButton — кнопка, открывающая веб-приложение (кабинет) с авторизацией initData.
func NewWebAppButton(text, url string) InlineKeyboardButton {
	return InlineKeyboardButton{Text: text, WebAppURL: url}
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
	// SendChatAction — индикатор действия ("typing" и т.п.). Живёт ~5с, повторять при долгой работе.
	SendChatAction(ctx context.Context, chatID int64, action string) error

	EditMessageText(ctx context.Context, chatID int64, msgID int, text string, opts *SendOpts) error
	EditMessageCaption(ctx context.Context, chatID int64, msgID int, caption string, opts *SendOpts) error
	EditMessageMedia(ctx context.Context, chatID int64, msgID int, media TGInputMedia) error

	DeleteMessage(ctx context.Context, chatID int64, msgID int) error
	AnswerCallback(ctx context.Context, callbackID string, text string) error

	// ForwardMessage пересылает сообщение и возвращает полный Message с контентом.
	// Используется расширением для извлечения контента пересланных постов
	// (Bot API не имеет getMessage, но forwardMessage в ответе отдаёт весь Message).
	ForwardMessage(ctx context.Context, fromChatID, toChatID int64, msgID int, silent bool) (*TGMessage, error)

	// CopyMessages копирует сообщения из srcChatID в dstChatID (copyMessage/copyMessages:
	// контент без плашки «переслано»; несколько id одного альбома сохраняют группировку).
	CopyMessages(ctx context.Context, dstChatID, srcChatID int64, msgIDs []int) error

	GetFile(ctx context.Context, fileID string) (filePath string, err error)
	GetFileDirectURL(filePath string) string
	GetChatMember(ctx context.Context, chatID, userID int64) (status string, err error)
	// RestrictChatMember мутит участника (запрет писать) до untilUnix (0 = навсегда).
	RestrictChatMember(ctx context.Context, chatID, userID int64, untilUnix int) error
	// UnrestrictChatMember снимает мут (возвращает базовые права писать) — для капчи.
	UnrestrictChatMember(ctx context.Context, chatID, userID int64) error
	// BanChatMember банит участника и удаляет его сообщения.
	BanChatMember(ctx context.Context, chatID, userID int64) error
	// UnbanChatMember снимает бан (участник сможет вернуться по ссылке/приглашению).
	UnbanChatMember(ctx context.Context, chatID, userID int64) error
	// KickChatMember выгоняет участника с возможностью вернуться (ban+unban) — для капчи-таймаута.
	KickChatMember(ctx context.Context, chatID, userID int64) error
	// GetUserPersonalChannel — личный канал из профиля юзера (Bot API personal_chat):
	// название + описание. ok=false — канала нет или юзер боту неизвестен.
	GetUserPersonalChannel(ctx context.Context, userID int64) (title, description string, ok bool)
	SetMyCommands(ctx context.Context, commands []BotCommand, scope *CommandScope) error
	SetMyDescription(ctx context.Context, description string) error
	SetMenuButtonWebApp(ctx context.Context, text, url string) error
	GetChat(ctx context.Context, chatID int64) (title string, err error)

	SetWebhook(ctx context.Context, url string) error
	DeleteWebhook(ctx context.Context) error
	StartWebhook(ctx context.Context, path string) <-chan TGUpdate
	StartPolling(ctx context.Context) <-chan TGUpdate

	BotUsername() string
	BotToken() string
}
