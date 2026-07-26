package main

import "errors"

// errThreadMaxBusy — попытка thread-bridge на MAX-чат, уже участвующий в какой-то связке.
var errThreadMaxBusy = errors.New("max chat is already linked (bridge or thread-bridge)")

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

// BotChatRef — чат, где присутствует бот (для пикера групп).
type BotChatRef struct {
	ChatID   int64
	Title    string
	ChatType string
}

// Repository — абстракция хранилища для bridge.
type Repository interface {
	// Register обрабатывает /bridge команду.
	// Без ключа — создаёт pending запись и возвращает сгенерированный ключ.
	// С ключом — ищет пару и создаёт связку.
	// userID — кто отправил команду (владелец стороны; пишется в pairs.{tg,max}_owner_id).
	Register(key, platform string, chatID, userID int64) (paired bool, generatedKey string, err error)

	// PeekBridgeKey возвращает pending-заявку /bridge по ключу, НЕ потребляя её —
	// для гейта free-лимита связок ДО фактического паринга (владелец второй стороны).
	PeekBridgeKey(key string) (platform string, chatID, userID int64, ok bool)

	// CountPairsByOwner — число активных связок, принадлежащих любому из владельцев
	// (tg_owner_id=tgOwner ИЛИ max_owner_id=maxOwner; нулевые id не считаются).
	CountPairsByOwner(maxOwner, tgOwner int64) int

	GetMaxChat(tgChatID int64) (int64, bool)
	GetTgChat(maxChatID int64) (int64, bool)
	// GetTgChats — все TG-чаты, связанные с MAX-чатом (несколько TG-групп → одна MAX-группа).
	GetTgChats(maxChatID int64) []int64
	MigrateTgChat(oldID, newID int64) error

	SaveMsg(tgChatID int64, tgMsgID int, maxChatID int64, maxMsgID string, tgThreadID int)
	SaveMsgOrigin(tgChatID int64, tgMsgID int, maxChatID int64, maxMsgID string, tgThreadID int, origin string)
	LookupMaxMsgID(tgChatID int64, tgMsgID int) (string, bool)
	LookupTgMsgID(maxMsgID string) (tgChatID int64, tgMsgID int, tgThreadID int, ok bool)
	ListTgMsgIDs(maxMsgID string, tgChatID int64) []int
	DeleteTgMsgMapping(tgChatID int64, tgMsgID int)
	LookupTgMsgOrigin(maxMsgID string) (origin string, ok bool)
	// MaxMsgDeliveredTo — было ли это MAX-сообщение уже доставлено именно в этот TG-чат
	// (пер-чат дедуп: при фан-ауте одного MAX-сообщения в несколько TG-групп нельзя
	// глушить доставку глобально по mid).
	MaxMsgDeliveredTo(maxMsgID string, tgChatID int64) bool
	CleanOldMessages()

	HasPrefix(platform string, chatID int64) bool
	SetPrefix(platform string, chatID int64, on bool) bool

	Unpair(platform string, chatID int64) bool

	// SetPairOwner проставляет владельца стороны существующей связки (для /bridge_update,
	// чтобы старые группы появлялись в кабинете). Возвращает true, если пара найдена.
	SetPairOwner(platform string, chatID, userID int64) bool

	// SetCrosspostOwner проставляет владельца стороны существующей crosspost-связки
	// (аналог /bridge_update для каналов: клейм по форварду поста админом канала,
	// чтобы старые связки появлялись в /crosspost и кабинете).
	// platform "tg" → tg_owner_id по tg_chat_id, "max" → owner_id по max_chat_id.
	SetCrosspostOwner(platform string, chatID, userID int64) bool

	GetTgThreadID(tgChatID int64) int
	SetTgThreadID(tgChatID int64, threadID int) error

	// Thread-bridge: связка отдельного TG-треда с отдельным MAX-чатом.
	// Ключ выдаётся в TG-треде (StartThreadBridge), принимается в MAX (CompleteThreadBridge).
	StartThreadBridge(tgChatID int64, threadID int) (key string, err error)
	CompleteThreadBridge(key string, maxChatID int64) (tgChatID int64, threadID int, ok bool, err error)
	GetThreadMaxChat(tgChatID int64, threadID int) (maxChatID int64, ok bool)
	GetThreadTgPair(maxChatID int64) (tgChatID int64, threadID int, ok bool)
	UnpairThread(tgChatID int64, threadID int) bool
	UnpairThreadByMax(maxChatID int64) bool

	// Crosspost methods
	PairCrosspost(tgChatID, maxChatID, ownerID, tgOwnerID int64) error
	GetCrosspostOwner(maxChatID int64) (maxOwner, tgOwner int64)
	GetCrosspostOwnerPair(tgChatID, maxChatID int64) (maxOwner, tgOwner int64)

	// SaveDiscussionMessage — связь поста канала с авто-форвардом в группе обсуждения.
	SaveDiscussionMessage(channelChatID int64, channelMsgID int, discChatID int64, discMsgID int) error
	// LookupChannelByDiscussion — по сообщению/треду группы обсуждения найти пост канала.
	LookupChannelByDiscussion(discChatID int64, discMsgID int) (channelChatID int64, channelMsgID int, ok bool)

	// RecordBotChat запоминает чат, где присутствует бот (для мастера линковки в кабинете).
	RecordBotChat(platform string, chatID int64, title, chatType string)
	// ListBotChats — чаты платформы, где есть бот (новые сверху), для пикера групп.
	ListBotChats(platform string, limit int) []BotChatRef
	// DoctorConnections returns delivery metadata for connections explicitly owned
	// by this platform user. It must not select message bodies or attachment data.
	DoctorConnections(platform string, userID, dayStart int64) ([]DoctorConnection, error)
	GetCrosspostMaxChat(tgChatID int64) (maxChatID int64, direction string, ok bool)
	GetCrosspostMaxChats(tgChatID int64) []CrosspostLink
	GetCrosspostTgChat(maxChatID int64) (tgChatID int64, direction string, ok bool)
	GetCrosspostTgChats(maxChatID int64) []CrosspostLink
	ListCrossposts(ownerID int64) []CrosspostLink
	// CountCrossposts — число активных связок, принадлежащих юзеру (для аддона).
	// Легаси-связки без владельца (0,0) не считаем.
	CountCrossposts(maxOwner, tgOwner int64) int
	// CrosspostRank — 0-based позиция связки maxChatID среди связок владельца по дате
	// создания (0 = самая старая). Используется аддоном.
	CrosspostRank(maxOwner, tgOwner, maxChatID int64) int
	SetCrosspostDirection(maxChatID int64, direction string) bool
	UnpairCrosspost(maxChatID, deletedBy int64) bool
	GetCrosspostReplacements(maxChatID int64) CrosspostReplacements
	SetCrosspostReplacements(maxChatID int64, repl CrosspostReplacements) error
	GetCrosspostSyncEdits(maxChatID int64) bool
	SetCrosspostSyncEdits(maxChatID int64, on bool) error

	// Пауза связок (временно не пересылаем, не удаляя)
	PairPaused(tgChatID, maxChatID int64) bool
	SetPairPaused(tgChatID, maxChatID int64, paused bool) error
	CrosspostPaused(maxChatID int64) bool
	SetCrosspostPaused(maxChatID int64, paused bool) error
	// TgOwnerForChat — TG-владелец связки (пары или кросспоста), куда входит chatID
	// (как TG- или MAX-сторона). 0 если не найден. Для DM-уведомлений о паузе.
	TgOwnerForChat(chatID int64) int64
	// ClaimCrosspost атомарно застолбляет (platform, chatID, msgID) как «уже кросспостим».
	// true — застолбили ВПЕРВЫЕ (кросспостить можно); false — уже было (дубль, пропустить).
	// Идемпотентность против повторной доставки/дубль-строк/петель. Старьё чистится по TTL.
	ClaimCrosspost(platform string, chatID int64, msgID string) bool

	// Users
	TouchUser(userID int64, platform, username, firstName string)

	// FindUserByUsername — id юзера по @username из таблицы users (виденные ботом).
	// Для модер-команд /unban @user и т.п. Регистронезависимо, username без «@».
	FindUserByUsername(platform, username string) (int64, bool)
	ListUsers(platform string) ([]int64, error)

	// Send queue (retry при недоступности MAX/TG API)
	EnqueueSend(item *QueueItem) error
	PeekQueue(limit int) ([]QueueItem, error)
	DeleteFromQueue(id int64) error
	IncrementAttempt(id int64, nextRetry int64) error
	// HasPendingQueue возвращает true если для данного dst-чата есть незавершённые элементы.
	// Используется для сохранения порядка: новые сообщения тоже идут через очередь,
	// пока предыдущие не доставлены.
	HasPendingQueue(direction string, dstChatID int64) bool

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
