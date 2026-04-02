package main

// Replacement — одно правило замены текста.
// Target: "" или "all" — весь текст, "links" — только ссылки.
type Replacement struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Regex  bool   `json:"regex"`
	Target string `json:"target,omitempty"`
}

// CrosspostReplacements — замены по направлениям.
type CrosspostReplacements struct {
	TgToMax []Replacement `json:"tg>max,omitempty"`
	MaxToTg []Replacement `json:"max>tg,omitempty"`
}

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
	Register(key, platform string, chatID int64, threadID int) (paired bool, generatedKey string, err error)

	GetMaxChat(tgChatID int64) (int64, bool)
	GetTgChat(maxChatID int64) (int64, bool)
	GetTgThreadID(tgChatID int64) int

	SaveMsg(tgChatID int64, tgMsgID int, maxChatID int64, maxMsgID string)
	LookupMaxMsgID(tgChatID int64, tgMsgID int) (string, bool)
	LookupTgMsgID(maxMsgID string) (int64, int, bool)
	CleanOldMessages()

	HasPrefix(platform string, chatID int64) bool
	SetPrefix(platform string, chatID int64, on bool) bool

	Unpair(platform string, chatID int64) bool

	// Crosspost methods
	PairCrosspost(tgChatID, maxChatID, ownerID, tgOwnerID int64) error
	GetCrosspostOwner(maxChatID int64) (maxOwner, tgOwner int64)
	GetCrosspostMaxChat(tgChatID int64) (maxChatID int64, direction string, ok bool)
	GetCrosspostTgChat(maxChatID int64) (tgChatID int64, direction string, ok bool)
	ListCrossposts(ownerID int64) []CrosspostLink
	SetCrosspostDirection(maxChatID int64, direction string) bool
	UnpairCrosspost(maxChatID, deletedBy int64) bool
	GetCrosspostReplacements(maxChatID int64) CrosspostReplacements
	SetCrosspostReplacements(maxChatID int64, repl CrosspostReplacements) error

	// Users
	TouchUser(userID int64, platform, username, firstName string)
	ListUsers(platform string) ([]int64, error)

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
	AttURL    string // URL медиа (для MAX→TG)
	ParseMode string // "HTML" или ""
	Attempts  int
	CreatedAt int64
	NextRetry int64
}
