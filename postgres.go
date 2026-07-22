package main

import (
	"database/sql"
	"sync"
	"time"

	_ "github.com/lib/pq"
)

type pgRepo struct {
	db *sql.DB
	mu sync.Mutex
}

func NewPostgresRepo(dsn string) (Repository, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		return nil, err
	}

	if err := runMigrations(db, "postgres"); err != nil {
		return nil, err
	}

	return &pgRepo{db: db}, nil
}

func (r *pgRepo) Register(key, platform string, chatID, userID int64) (bool, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if key == "" {
		var existing string
		err := r.db.QueryRow("SELECT key FROM pending WHERE platform = $1 AND chat_id = $2 AND command = 'bridge'", platform, chatID).Scan(&existing)
		if err == nil {
			return false, existing, nil
		}
		generated := genKey()
		_, err = r.db.Exec("INSERT INTO pending (key, platform, chat_id, created_at, command, user_id) VALUES ($1, $2, $3, $4, 'bridge', $5)", generated, platform, chatID, time.Now().Unix(), userID)
		return false, generated, err
	}

	var peerPlatform string
	var peerChatID, peerUserID int64
	err := r.db.QueryRow("SELECT platform, chat_id, COALESCE(user_id,0) FROM pending WHERE key = $1 AND command = 'bridge'", key).Scan(&peerPlatform, &peerChatID, &peerUserID)
	if err != nil {
		return false, "", nil
	}
	if peerPlatform == platform {
		return false, "", nil
	}

	r.db.Exec("DELETE FROM pending WHERE key = $1", key)

	// Сторона, выполнившая /bridge, — владелец своей стороны связки.
	var tgID, maxID, tgOwner, maxOwner int64
	if platform == "tg" {
		tgID, maxID = chatID, peerChatID
		tgOwner, maxOwner = userID, peerUserID
	} else {
		tgID, maxID = peerChatID, chatID
		tgOwner, maxOwner = peerUserID, userID
	}

	_, err = r.db.Exec(
		"INSERT INTO pairs (tg_chat_id, max_chat_id, prefix, created_at, tg_owner_id, max_owner_id) VALUES ($1, $2, 0, $3, $4, $5) ON CONFLICT (tg_chat_id, max_chat_id) DO UPDATE SET prefix = 0, tg_owner_id = excluded.tg_owner_id, max_owner_id = excluded.max_owner_id",
		tgID, maxID, time.Now().Unix(), tgOwner, maxOwner)
	return true, "", err
}

func (r *pgRepo) PeekBridgeKey(key string) (string, int64, int64, bool) {
	var platform string
	var chatID, userID int64
	err := r.db.QueryRow("SELECT platform, chat_id, COALESCE(user_id,0) FROM pending WHERE key = $1 AND command = 'bridge'", key).Scan(&platform, &chatID, &userID)
	return platform, chatID, userID, err == nil
}

func (r *pgRepo) CountPairsByOwner(maxOwner, tgOwner int64) int {
	var n int
	_ = r.db.QueryRow(`SELECT COUNT(*) FROM pairs WHERE (tg_owner_id = $1 AND tg_owner_id != 0) OR (max_owner_id = $2 AND max_owner_id != 0)`,
		tgOwner, maxOwner).Scan(&n)
	return n
}

func (r *pgRepo) MigrateTgChat(oldID, newID int64) error {
	_, err := r.db.Exec("UPDATE pairs SET tg_chat_id = $1 WHERE tg_chat_id = $2", newID, oldID)
	if err == nil {
		r.db.Exec("UPDATE messages SET tg_chat_id = $1 WHERE tg_chat_id = $2", newID, oldID)
	}
	return err
}

func (r *pgRepo) GetMaxChat(tgChatID int64) (int64, bool) {
	var id int64
	err := r.db.QueryRow("SELECT max_chat_id FROM pairs WHERE tg_chat_id = $1", tgChatID).Scan(&id)
	return id, err == nil
}

func (r *pgRepo) GetTgChat(maxChatID int64) (int64, bool) {
	var id int64
	err := r.db.QueryRow("SELECT tg_chat_id FROM pairs WHERE max_chat_id = $1", maxChatID).Scan(&id)
	return id, err == nil
}

func (r *pgRepo) GetTgChats(maxChatID int64) []int64 {
	rows, err := r.db.Query("SELECT tg_chat_id FROM pairs WHERE max_chat_id = $1", maxChatID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

func (r *pgRepo) SaveMsg(tgChatID int64, tgMsgID int, maxChatID int64, maxMsgID string, tgThreadID int) {
	r.SaveMsgOrigin(tgChatID, tgMsgID, maxChatID, maxMsgID, tgThreadID, "")
}

func (r *pgRepo) SaveMsgOrigin(tgChatID int64, tgMsgID int, maxChatID int64, maxMsgID string, tgThreadID int, origin string) {
	r.db.Exec(
		`INSERT INTO messages (tg_chat_id, tg_msg_id, max_chat_id, max_msg_id, tg_thread_id, origin, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 ON CONFLICT (tg_chat_id, tg_msg_id) DO UPDATE
		 SET max_chat_id = EXCLUDED.max_chat_id, max_msg_id = EXCLUDED.max_msg_id, tg_thread_id = EXCLUDED.tg_thread_id, origin = EXCLUDED.origin, created_at = EXCLUDED.created_at`,
		tgChatID, tgMsgID, maxChatID, maxMsgID, tgThreadID, origin, time.Now().Unix())
}

func (r *pgRepo) LookupMaxMsgID(tgChatID int64, tgMsgID int) (string, bool) {
	var id string
	err := r.db.QueryRow("SELECT max_msg_id FROM messages WHERE tg_chat_id = $1 AND tg_msg_id = $2", tgChatID, tgMsgID).Scan(&id)
	return id, err == nil
}

func (r *pgRepo) LookupTgMsgID(maxMsgID string) (int64, int, int, bool) {
	var chatID int64
	var msgID, threadID int
	err := r.db.QueryRow("SELECT tg_chat_id, tg_msg_id, COALESCE(tg_thread_id, 0) FROM messages WHERE max_msg_id = $1", maxMsgID).Scan(&chatID, &msgID, &threadID)
	return chatID, msgID, threadID, err == nil
}

func (r *pgRepo) LookupTgMsgOrigin(maxMsgID string) (string, bool) {
	var origin string
	err := r.db.QueryRow("SELECT COALESCE(origin, '') FROM messages WHERE max_msg_id = $1", maxMsgID).Scan(&origin)
	return origin, err == nil
}

func (r *pgRepo) MaxMsgDeliveredTo(maxMsgID string, tgChatID int64) bool {
	if maxMsgID == "" {
		return false
	}
	var one int
	err := r.db.QueryRow("SELECT 1 FROM messages WHERE max_msg_id = $1 AND tg_chat_id = $2 LIMIT 1", maxMsgID, tgChatID).Scan(&one)
	return err == nil
}

func (r *pgRepo) CleanOldMessages() {
	r.db.Exec("DELETE FROM messages WHERE created_at < $1", time.Now().Unix()-48*3600)
	r.db.Exec("DELETE FROM pending WHERE created_at > 0 AND created_at < $1", time.Now().Unix()-3600)
}

func (r *pgRepo) HasPrefix(platform string, chatID int64) bool {
	var v int
	var err error
	if platform == "tg" {
		err = r.db.QueryRow("SELECT prefix FROM pairs WHERE tg_chat_id = $1", chatID).Scan(&v)
	} else {
		err = r.db.QueryRow("SELECT prefix FROM pairs WHERE max_chat_id = $1", chatID).Scan(&v)
	}
	if err != nil {
		// Чат не в pairs (например, thread-bridged) — без префикса по умолчанию.
		return false
	}
	return v == 1
}

func (r *pgRepo) SetPrefix(platform string, chatID int64, on bool) bool {
	v := 0
	if on {
		v = 1
	}
	var res sql.Result
	if platform == "tg" {
		res, _ = r.db.Exec("UPDATE pairs SET prefix = $1 WHERE tg_chat_id = $2", v, chatID)
	} else {
		res, _ = r.db.Exec("UPDATE pairs SET prefix = $1 WHERE max_chat_id = $2", v, chatID)
	}
	if res == nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

func (r *pgRepo) Unpair(platform string, chatID int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	var res sql.Result
	if platform == "tg" {
		res, _ = r.db.Exec("DELETE FROM pairs WHERE tg_chat_id = $1", chatID)
	} else {
		res, _ = r.db.Exec("DELETE FROM pairs WHERE max_chat_id = $1", chatID)
	}
	if res == nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

func (r *pgRepo) SetPairOwner(platform string, chatID, userID int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	var res sql.Result
	if platform == "tg" {
		res, _ = r.db.Exec("UPDATE pairs SET tg_owner_id = $1 WHERE tg_chat_id = $2", userID, chatID)
	} else {
		res, _ = r.db.Exec("UPDATE pairs SET max_owner_id = $1 WHERE max_chat_id = $2", userID, chatID)
	}
	if res == nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

func (r *pgRepo) SetCrosspostOwner(platform string, chatID, userID int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	var res sql.Result
	if platform == "tg" {
		res, _ = r.db.Exec("UPDATE crossposts SET tg_owner_id = $1 WHERE tg_chat_id = $2 AND deleted_at = 0", userID, chatID)
	} else {
		res, _ = r.db.Exec("UPDATE crossposts SET owner_id = $1 WHERE max_chat_id = $2 AND deleted_at = 0", userID, chatID)
	}
	if res == nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

func (r *pgRepo) GetTgThreadID(tgChatID int64) int {
	var id int
	r.db.QueryRow("SELECT COALESCE(tg_thread_id, 0) FROM pairs WHERE tg_chat_id = $1", tgChatID).Scan(&id)
	return id
}

func (r *pgRepo) SetTgThreadID(tgChatID int64, threadID int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, err := r.db.Exec("UPDATE pairs SET tg_thread_id = $1 WHERE tg_chat_id = $2", threadID, tgChatID)
	return err
}

func (r *pgRepo) StartThreadBridge(tgChatID int64, threadID int) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	var existing string
	err := r.db.QueryRow(
		"SELECT key FROM pending WHERE platform = 'tg' AND chat_id = $1 AND thread_id = $2 AND command = 'thread-bridge'",
		tgChatID, threadID).Scan(&existing)
	if err == nil {
		return existing, nil
	}

	generated := genKey()
	_, err = r.db.Exec(
		"INSERT INTO pending (key, platform, chat_id, thread_id, created_at, command) VALUES ($1, 'tg', $2, $3, $4, 'thread-bridge')",
		generated, tgChatID, threadID, time.Now().Unix())
	return generated, err
}

func (r *pgRepo) CompleteThreadBridge(key string, maxChatID int64) (int64, int, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	var tgChatID int64
	var threadID int
	err := r.db.QueryRow(
		"SELECT chat_id, thread_id FROM pending WHERE key = $1 AND platform = 'tg' AND command = 'thread-bridge'",
		key).Scan(&tgChatID, &threadID)
	if err != nil {
		return 0, 0, false, nil
	}

	var cnt int
	r.db.QueryRow("SELECT COUNT(*) FROM pairs WHERE max_chat_id = $1", maxChatID).Scan(&cnt)
	if cnt > 0 {
		return 0, 0, false, errThreadMaxBusy
	}
	cnt = 0
	r.db.QueryRow("SELECT COUNT(*) FROM thread_pairs WHERE max_chat_id = $1", maxChatID).Scan(&cnt)
	if cnt > 0 {
		return 0, 0, false, errThreadMaxBusy
	}

	r.db.Exec("DELETE FROM pending WHERE key = $1", key)
	_, err = r.db.Exec(
		`INSERT INTO thread_pairs (tg_chat_id, tg_thread_id, max_chat_id, created_at)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (tg_chat_id, tg_thread_id) DO UPDATE
		 SET max_chat_id = EXCLUDED.max_chat_id, created_at = EXCLUDED.created_at`,
		tgChatID, threadID, maxChatID, time.Now().Unix())
	if err != nil {
		return 0, 0, false, err
	}
	return tgChatID, threadID, true, nil
}

func (r *pgRepo) GetThreadMaxChat(tgChatID int64, threadID int) (int64, bool) {
	// threadID==0 — General-топик форума (валидная связка), НЕ отбиваем (см. sqlite).
	var id int64
	err := r.db.QueryRow(
		"SELECT max_chat_id FROM thread_pairs WHERE tg_chat_id = $1 AND tg_thread_id = $2",
		tgChatID, threadID).Scan(&id)
	return id, err == nil
}

func (r *pgRepo) GetThreadTgPair(maxChatID int64) (int64, int, bool) {
	var tgChatID int64
	var threadID int
	err := r.db.QueryRow(
		"SELECT tg_chat_id, tg_thread_id FROM thread_pairs WHERE max_chat_id = $1",
		maxChatID).Scan(&tgChatID, &threadID)
	return tgChatID, threadID, err == nil
}

func (r *pgRepo) UnpairThread(tgChatID int64, threadID int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	res, err := r.db.Exec("DELETE FROM thread_pairs WHERE tg_chat_id = $1 AND tg_thread_id = $2", tgChatID, threadID)
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

func (r *pgRepo) UnpairThreadByMax(maxChatID int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	res, err := r.db.Exec("DELETE FROM thread_pairs WHERE max_chat_id = $1", maxChatID)
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

func (r *pgRepo) PairCrosspost(tgChatID, maxChatID, ownerID, tgOwnerID int64) error {
	_, err := r.db.Exec(
		"INSERT INTO crossposts (tg_chat_id, max_chat_id, created_at, owner_id, tg_owner_id) VALUES ($1, $2, $3, $4, $5) ON CONFLICT (tg_chat_id, max_chat_id) DO NOTHING",
		tgChatID, maxChatID, time.Now().Unix(), ownerID, tgOwnerID)
	return err
}

func (r *pgRepo) GetCrosspostOwner(maxChatID int64) (maxOwner, tgOwner int64) {
	r.db.QueryRow("SELECT owner_id, tg_owner_id FROM crossposts WHERE max_chat_id = $1 AND deleted_at = 0", maxChatID).Scan(&maxOwner, &tgOwner)
	return
}

func (r *pgRepo) SaveDiscussionMessage(channelChatID int64, channelMsgID int, discChatID int64, discMsgID int) error {
	_, err := r.db.Exec(`INSERT INTO discussion_map (channel_chat_id, channel_msg_id, disc_chat_id, disc_msg_id, created_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (channel_chat_id, channel_msg_id) DO UPDATE SET disc_chat_id=excluded.disc_chat_id, disc_msg_id=excluded.disc_msg_id`,
		channelChatID, channelMsgID, discChatID, discMsgID, time.Now().Unix())
	return err
}

func (r *pgRepo) RecordBotChat(platform string, chatID int64, title, chatType string) {
	_, _ = r.db.Exec(`INSERT INTO bot_chats (platform, chat_id, title, chat_type, updated_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT(platform, chat_id) DO UPDATE SET title=excluded.title, chat_type=excluded.chat_type, updated_at=excluded.updated_at`,
		platform, chatID, title, chatType, time.Now().Unix())
}

func (r *pgRepo) ListBotChats(platform string, limit int) []BotChatRef {
	out := []BotChatRef{}
	rows, err := r.db.Query(`SELECT chat_id, COALESCE(title,''), COALESCE(chat_type,'')
		FROM bot_chats WHERE platform=$1 ORDER BY updated_at DESC LIMIT $2`, platform, limit)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var c BotChatRef
		if rows.Scan(&c.ChatID, &c.Title, &c.ChatType) == nil {
			out = append(out, c)
		}
	}
	return out
}

func (r *pgRepo) LookupChannelByDiscussion(discChatID int64, discMsgID int) (int64, int, bool) {
	var cc int64
	var cm int
	err := r.db.QueryRow(`SELECT channel_chat_id, channel_msg_id FROM discussion_map WHERE disc_chat_id = $1 AND disc_msg_id = $2`,
		discChatID, discMsgID).Scan(&cc, &cm)
	return cc, cm, err == nil
}

func (r *pgRepo) GetCrosspostMaxChat(tgChatID int64) (int64, string, bool) {
	var id int64
	var dir string
	err := r.db.QueryRow("SELECT max_chat_id, direction FROM crossposts WHERE tg_chat_id = $1 AND deleted_at = 0", tgChatID).Scan(&id, &dir)
	return id, dir, err == nil
}

func (r *pgRepo) GetCrosspostTgChat(maxChatID int64) (int64, string, bool) {
	var id int64
	var dir string
	err := r.db.QueryRow("SELECT tg_chat_id, direction FROM crossposts WHERE max_chat_id = $1 AND deleted_at = 0", maxChatID).Scan(&id, &dir)
	return id, dir, err == nil
}

func (r *pgRepo) ListCrossposts(ownerID int64) []CrosspostLink {
	rows, err := r.db.Query("SELECT tg_chat_id, max_chat_id, direction FROM crossposts WHERE (owner_id = $1 OR tg_owner_id = $1 OR (owner_id = 0 AND tg_owner_id = 0)) AND deleted_at = 0", ownerID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var links []CrosspostLink
	for rows.Next() {
		var l CrosspostLink
		if rows.Scan(&l.TgChatID, &l.MaxChatID, &l.Direction) == nil {
			links = append(links, l)
		}
	}
	return links
}

func (r *pgRepo) CountCrossposts(maxOwner, tgOwner int64) int {
	var n int
	r.db.QueryRow(`SELECT COUNT(*) FROM crossposts WHERE deleted_at=0
		AND ((owner_id=$1 AND owner_id!=0) OR (tg_owner_id=$2 AND tg_owner_id!=0))`, maxOwner, tgOwner).Scan(&n)
	return n
}

func (r *pgRepo) CrosspostRank(maxOwner, tgOwner, maxChatID int64) int {
	var n int
	r.db.QueryRow(`SELECT COUNT(*) FROM crossposts WHERE deleted_at=0
		AND ((owner_id=$1 AND owner_id!=0) OR (tg_owner_id=$2 AND tg_owner_id!=0))
		AND created_at < (SELECT created_at FROM crossposts WHERE max_chat_id=$3 AND deleted_at=0 LIMIT 1)`,
		maxOwner, tgOwner, maxChatID).Scan(&n)
	return n
}

func (r *pgRepo) SetCrosspostDirection(maxChatID int64, direction string) bool {
	res, _ := r.db.Exec("UPDATE crossposts SET direction = $1 WHERE max_chat_id = $2 AND deleted_at = 0", direction, maxChatID)
	if res == nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

func (r *pgRepo) UnpairCrosspost(maxChatID, deletedBy int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	res, _ := r.db.Exec("UPDATE crossposts SET deleted_at = $1, deleted_by = $2 WHERE max_chat_id = $3 AND deleted_at = 0",
		time.Now().Unix(), deletedBy, maxChatID)
	if res == nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

func (r *pgRepo) GetCrosspostReplacements(maxChatID int64) CrosspostReplacements {
	var raw string
	r.db.QueryRow("SELECT replacements FROM crossposts WHERE max_chat_id = $1 AND deleted_at = 0", maxChatID).Scan(&raw)
	return parseCrosspostReplacements(raw)
}

func (r *pgRepo) SetCrosspostReplacements(maxChatID int64, repl CrosspostReplacements) error {
	data := marshalCrosspostReplacements(repl)
	r.mu.Lock()
	defer r.mu.Unlock()
	_, err := r.db.Exec("UPDATE crossposts SET replacements = $1 WHERE max_chat_id = $2 AND deleted_at = 0", data, maxChatID)
	return err
}

func (r *pgRepo) GetCrosspostSyncEdits(maxChatID int64) bool {
	var v bool
	r.db.QueryRow("SELECT COALESCE(sync_edits, FALSE) FROM crossposts WHERE max_chat_id = $1 AND deleted_at = 0", maxChatID).Scan(&v)
	return v
}

func (r *pgRepo) SetCrosspostSyncEdits(maxChatID int64, on bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, err := r.db.Exec("UPDATE crossposts SET sync_edits = $1 WHERE max_chat_id = $2 AND deleted_at = 0", on, maxChatID)
	return err
}

// --- Пауза связок (временно не пересылаем, связку не удаляем) ---

func (r *pgRepo) PairPaused(tgChatID, maxChatID int64) bool {
	var v int
	r.db.QueryRow("SELECT COALESCE(paused, 0) FROM pairs WHERE tg_chat_id = $1 AND max_chat_id = $2", tgChatID, maxChatID).Scan(&v)
	return v != 0
}

func (r *pgRepo) SetPairPaused(tgChatID, maxChatID int64, paused bool) error {
	v := 0
	if paused {
		v = 1
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_, err := r.db.Exec("UPDATE pairs SET paused = $1 WHERE tg_chat_id = $2 AND max_chat_id = $3", v, tgChatID, maxChatID)
	return err
}

func (r *pgRepo) CrosspostPaused(maxChatID int64) bool {
	var v int
	r.db.QueryRow("SELECT COALESCE(paused, 0) FROM crossposts WHERE max_chat_id = $1 AND deleted_at = 0", maxChatID).Scan(&v)
	return v != 0
}

func (r *pgRepo) SetCrosspostPaused(maxChatID int64, paused bool) error {
	v := 0
	if paused {
		v = 1
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_, err := r.db.Exec("UPDATE crossposts SET paused = $1 WHERE max_chat_id = $2 AND deleted_at = 0", v, maxChatID)
	return err
}

func (r *pgRepo) ClaimCrosspost(platform string, chatID int64, msgID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now().Unix()
	r.db.Exec("DELETE FROM crosspost_seen WHERE at < $1", now-3600) // TTL: 1ч
	res, err := r.db.Exec(
		"INSERT INTO crosspost_seen (platform, chat_id, msg_id, at) VALUES ($1, $2, $3, $4) ON CONFLICT (platform, chat_id, msg_id) DO NOTHING",
		platform, chatID, msgID, now)
	if err != nil {
		return true // на ошибке БД не блокируем доставку
	}
	n, _ := res.RowsAffected()
	return n > 0
}

func (r *pgRepo) TgOwnerForChat(chatID int64) int64 {
	var owner int64
	r.db.QueryRow("SELECT tg_owner_id FROM pairs WHERE tg_chat_id = $1 OR max_chat_id = $2", chatID, chatID).Scan(&owner)
	if owner != 0 {
		return owner
	}
	r.db.QueryRow("SELECT tg_owner_id FROM crossposts WHERE (tg_chat_id = $1 OR max_chat_id = $2) AND deleted_at = 0", chatID, chatID).Scan(&owner)
	return owner
}

func (r *pgRepo) TouchUser(userID int64, platform, username, firstName string) {
	now := time.Now().Unix()
	r.db.Exec(`INSERT INTO users (user_id, platform, username, first_name, first_seen, last_seen) VALUES ($1, $2, $3, $4, $5, $5)
		ON CONFLICT(user_id) DO UPDATE SET username=EXCLUDED.username, first_name=EXCLUDED.first_name, last_seen=EXCLUDED.last_seen`,
		userID, platform, username, firstName, now)
}

func (r *pgRepo) FindUserByUsername(platform, username string) (int64, bool) {
	var id int64
	err := r.db.QueryRow(`SELECT user_id FROM users WHERE platform = $1 AND lower(username) = lower($2) ORDER BY last_seen DESC LIMIT 1`,
		platform, username).Scan(&id)
	return id, err == nil
}

func (r *pgRepo) ListUsers(platform string) ([]int64, error) {
	rows, err := r.db.Query("SELECT user_id FROM users WHERE platform = $1", platform)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func (r *pgRepo) EnqueueSend(item *QueueItem) error {
	_, err := r.db.Exec(
		`INSERT INTO send_queue (direction, src_chat_id, dst_chat_id, src_msg_id, text, att_type, att_token, reply_to, format, att_url, parse_mode, attempts, created_at, next_retry)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, 0, $12, $13)`,
		item.Direction, item.SrcChatID, item.DstChatID, item.SrcMsgID,
		item.Text, item.AttType, item.AttToken, item.ReplyTo, item.Format,
		item.AttURL, item.ParseMode,
		item.CreatedAt, item.NextRetry,
	)
	return err
}

func (r *pgRepo) PeekQueue(limit int) ([]QueueItem, error) {
	rows, err := r.db.Query(
		`SELECT id, direction, src_chat_id, dst_chat_id, src_msg_id, text, att_type, att_token, reply_to, format, att_url, parse_mode, attempts, created_at, next_retry
		 FROM send_queue WHERE next_retry <= $1 ORDER BY id ASC LIMIT $2`,
		time.Now().Unix(), limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []QueueItem
	for rows.Next() {
		var q QueueItem
		if err := rows.Scan(&q.ID, &q.Direction, &q.SrcChatID, &q.DstChatID, &q.SrcMsgID,
			&q.Text, &q.AttType, &q.AttToken, &q.ReplyTo, &q.Format,
			&q.AttURL, &q.ParseMode,
			&q.Attempts, &q.CreatedAt, &q.NextRetry); err != nil {
			return nil, err
		}
		items = append(items, q)
	}
	return items, nil
}

func (r *pgRepo) DeleteFromQueue(id int64) error {
	_, err := r.db.Exec("DELETE FROM send_queue WHERE id = $1", id)
	return err
}

func (r *pgRepo) IncrementAttempt(id int64, nextRetry int64) error {
	_, err := r.db.Exec("UPDATE send_queue SET attempts = attempts + 1, next_retry = $1 WHERE id = $2", nextRetry, id)
	return err
}

func (r *pgRepo) HasPendingQueue(direction string, dstChatID int64) bool {
	var count int
	r.db.QueryRow("SELECT COUNT(*) FROM send_queue WHERE direction = $1 AND dst_chat_id = $2", direction, dstChatID).Scan(&count)
	return count > 0
}

func (r *pgRepo) Close() error {
	return r.db.Close()
}
