package main

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
	RegisterCrosspost(key, platform string, chatID int64) (paired bool, generatedKey string, err error)
	GetCrosspostMaxChat(tgChatID int64) (maxChatID int64, direction string, ok bool)
	GetCrosspostTgChat(maxChatID int64) (tgChatID int64, direction string, ok bool)
	SetCrosspostDirection(platform string, chatID int64, direction string) bool
	UnpairCrosspost(platform string, chatID int64) bool
	HasCrosspostPrefix(platform string, chatID int64) bool
	SetCrosspostPrefix(platform string, chatID int64, on bool) bool

	Close() error
}
