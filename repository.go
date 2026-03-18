package main

// CrosspostLink — одна связка кросспостинга.
type CrosspostLink struct {
	TgChatID  int64
	MaxChatID int64
	Direction string
}

// Repository — абстракция хранилища для bridge.
type Repository interface {
	// Register обрабатывает /bridge команду.
	// Без ключа — создаёт pending запись и возвращает сгенерированный ключ.
	// С ключом — ищет пару и создаёт связку.
	Register(key, platform string, chatID int64) (paired bool, generatedKey string, err error)

	GetMaxChat(tgChatID int64) (int64, bool)
	GetTgChat(maxChatID int64) (int64, bool)

	SaveMsg(tgChatID int64, tgMsgID int, maxChatID int64, maxMsgID string)
	LookupMaxMsgID(tgChatID int64, tgMsgID int) (string, bool)
	LookupTgMsgID(maxMsgID string) (int64, int, bool)
	CleanOldMessages()

	HasPrefix(platform string, chatID int64) bool
	SetPrefix(platform string, chatID int64, on bool) bool

	Unpair(platform string, chatID int64) bool

	// Crosspost methods
	PairCrosspost(tgChatID, maxChatID int64) error
	GetCrosspostMaxChat(tgChatID int64) (maxChatID int64, direction string, ok bool)
	GetCrosspostTgChat(maxChatID int64) (tgChatID int64, direction string, ok bool)
	ListCrossposts() []CrosspostLink
	SetCrosspostDirection(maxChatID int64, direction string) bool
	UnpairCrosspost(maxChatID int64) bool

	// Send queue (retry при недоступности MAX/TG API)
	EnqueueSend(item *QueueItem) error
	PeekQueue(limit int) ([]QueueItem, error)
	DeleteFromQueue(id int64) error
	IncrementAttempt(id int64, nextRetry int64) error

	Close() error
}

// QueueItem — сообщение в очереди на повторную отправку.
type QueueItem struct {
	ID        int64
	Direction string // "tg2max" or "max2tg"
	SrcChatID int64
	DstChatID int64
	SrcMsgID  string // TG msg ID (as string) or MAX mid
	Text      string
	AttType   string // "video", "file", "audio", ""
	AttToken  string
	ReplyTo   string
	Format    string
	Attempts  int
	CreatedAt int64
	NextRetry int64
}
