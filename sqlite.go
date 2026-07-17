package main

import (
	"database/sql"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type sqliteRepo struct {
	db *sql.DB
	mu sync.Mutex
}

func NewSQLiteRepo(dbPath string) (Repository, error) {
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL")
	if err != nil {
		return nil, err
	}

	if err := runMigrations(db, "sqlite3"); err != nil {
		return nil, err
	}

	return &sqliteRepo{db: db}, nil
}

func (r *sqliteRepo) Register(key, platform string, chatID, userID int64) (bool, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if key == "" {
		var existing string
		err := r.db.QueryRow("SELECT key FROM pending WHERE platform = ? AND chat_id = ? AND command = 'bridge'", platform, chatID).Scan(&existing)
		if err == nil {
			return false, existing, nil
		}
		generated := genKey()
		_, err = r.db.Exec("INSERT INTO pending (key, platform, chat_id, created_at, command, user_id) VALUES (?, ?, ?, ?, 'bridge', ?)", generated, platform, chatID, time.Now().Unix(), userID)
		return false, generated, err
	}

	var peerPlatform string
	var peerChatID, peerUserID int64
	err := r.db.QueryRow("SELECT platform, chat_id, COALESCE(user_id,0) FROM pending WHERE key = ? AND command = 'bridge'", key).Scan(&peerPlatform, &peerChatID, &peerUserID)
	if err != nil {
		return false, "", nil
	}
	if peerPlatform == platform {
		return false, "", nil
	}

	r.db.Exec("DELETE FROM pending WHERE key = ?", key)

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
		`INSERT INTO pairs (tg_chat_id, max_chat_id, prefix, created_at, tg_owner_id, max_owner_id) VALUES (?, ?, 0, ?, ?, ?)
		 ON CONFLICT(tg_chat_id, max_chat_id) DO UPDATE SET prefix = 0, tg_owner_id = excluded.tg_owner_id, max_owner_id = excluded.max_owner_id`,
		tgID, maxID, time.Now().Unix(), tgOwner, maxOwner)
	return true, "", err
}

func (r *sqliteRepo) PeekBridgeKey(key string) (string, int64, int64, bool) {
	var platform string
	var chatID, userID int64
	err := r.db.QueryRow("SELECT platform, chat_id, COALESCE(user_id,0) FROM pending WHERE key = ? AND command = 'bridge'", key).Scan(&platform, &chatID, &userID)
	return platform, chatID, userID, err == nil
}

func (r *sqliteRepo) CountPairsByOwner(maxOwner, tgOwner int64) int {
	var n int
	_ = r.db.QueryRow(`SELECT COUNT(*) FROM pairs WHERE (tg_owner_id = ? AND tg_owner_id != 0) OR (max_owner_id = ? AND max_owner_id != 0)`,
		tgOwner, maxOwner).Scan(&n)
	return n
}

func (r *sqliteRepo) MigrateTgChat(oldID, newID int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, err := r.db.Exec("UPDATE pairs SET tg_chat_id = ? WHERE tg_chat_id = ?", newID, oldID)
	if err == nil {
		r.db.Exec("UPDATE messages SET tg_chat_id = ? WHERE tg_chat_id = ?", newID, oldID)
	}
	return err
}

func (r *sqliteRepo) GetMaxChat(tgChatID int64) (int64, bool) {
	var id int64
	err := r.db.QueryRow("SELECT max_chat_id FROM pairs WHERE tg_chat_id = ?", tgChatID).Scan(&id)
	return id, err == nil
}

func (r *sqliteRepo) GetTgChat(maxChatID int64) (int64, bool) {
	var id int64
	err := r.db.QueryRow("SELECT tg_chat_id FROM pairs WHERE max_chat_id = ?", maxChatID).Scan(&id)
	return id, err == nil
}

func (r *sqliteRepo) GetTgChats(maxChatID int64) []int64 {
	rows, err := r.db.Query("SELECT tg_chat_id FROM pairs WHERE max_chat_id = ?", maxChatID)
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

func (r *sqliteRepo) SaveMsg(tgChatID int64, tgMsgID int, maxChatID int64, maxMsgID string, tgThreadID int) {
	r.db.Exec("INSERT OR REPLACE INTO messages (tg_chat_id, tg_msg_id, max_chat_id, max_msg_id, tg_thread_id, created_at) VALUES (?, ?, ?, ?, ?, ?)",
		tgChatID, tgMsgID, maxChatID, maxMsgID, tgThreadID, time.Now().Unix())
}

func (r *sqliteRepo) LookupMaxMsgID(tgChatID int64, tgMsgID int) (string, bool) {
	var id string
	err := r.db.QueryRow("SELECT max_msg_id FROM messages WHERE tg_chat_id = ? AND tg_msg_id = ?", tgChatID, tgMsgID).Scan(&id)
	return id, err == nil
}

func (r *sqliteRepo) LookupTgMsgID(maxMsgID string) (int64, int, int, bool) {
	var chatID int64
	var msgID, threadID int
	err := r.db.QueryRow("SELECT tg_chat_id, tg_msg_id, COALESCE(tg_thread_id, 0) FROM messages WHERE max_msg_id = ?", maxMsgID).Scan(&chatID, &msgID, &threadID)
	return chatID, msgID, threadID, err == nil
}

func (r *sqliteRepo) MaxMsgDeliveredTo(maxMsgID string, tgChatID int64) bool {
	if maxMsgID == "" {
		return false
	}
	var one int
	err := r.db.QueryRow("SELECT 1 FROM messages WHERE max_msg_id = ? AND tg_chat_id = ? LIMIT 1", maxMsgID, tgChatID).Scan(&one)
	return err == nil
}

func (r *sqliteRepo) CleanOldMessages() {
	r.db.Exec("DELETE FROM messages WHERE created_at < ?", time.Now().Unix()-48*3600)
	r.db.Exec("DELETE FROM pending WHERE created_at > 0 AND created_at < ?", time.Now().Unix()-3600)
}

func (r *sqliteRepo) HasPrefix(platform string, chatID int64) bool {
	var v int
	var err error
	if platform == "tg" {
		err = r.db.QueryRow("SELECT prefix FROM pairs WHERE tg_chat_id = ?", chatID).Scan(&v)
	} else {
		err = r.db.QueryRow("SELECT prefix FROM pairs WHERE max_chat_id = ?", chatID).Scan(&v)
	}
	if err != nil {
		// Чат не в pairs (например, thread-bridged) — без префикса по умолчанию.
		return false
	}
	return v == 1
}

func (r *sqliteRepo) SetPrefix(platform string, chatID int64, on bool) bool {
	v := 0
	if on {
		v = 1
	}
	var res sql.Result
	if platform == "tg" {
		res, _ = r.db.Exec("UPDATE pairs SET prefix = ? WHERE tg_chat_id = ?", v, chatID)
	} else {
		res, _ = r.db.Exec("UPDATE pairs SET prefix = ? WHERE max_chat_id = ?", v, chatID)
	}
	if res == nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

func (r *sqliteRepo) Unpair(platform string, chatID int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	var res sql.Result
	if platform == "tg" {
		res, _ = r.db.Exec("DELETE FROM pairs WHERE tg_chat_id = ?", chatID)
	} else {
		res, _ = r.db.Exec("DELETE FROM pairs WHERE max_chat_id = ?", chatID)
	}
	if res == nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

func (r *sqliteRepo) SetPairOwner(platform string, chatID, userID int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	var res sql.Result
	if platform == "tg" {
		res, _ = r.db.Exec("UPDATE pairs SET tg_owner_id = ? WHERE tg_chat_id = ?", userID, chatID)
	} else {
		res, _ = r.db.Exec("UPDATE pairs SET max_owner_id = ? WHERE max_chat_id = ?", userID, chatID)
	}
	if res == nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

func (r *sqliteRepo) SetCrosspostOwner(platform string, chatID, userID int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	var res sql.Result
	if platform == "tg" {
		res, _ = r.db.Exec("UPDATE crossposts SET tg_owner_id = ? WHERE tg_chat_id = ? AND deleted_at = 0", userID, chatID)
	} else {
		res, _ = r.db.Exec("UPDATE crossposts SET owner_id = ? WHERE max_chat_id = ? AND deleted_at = 0", userID, chatID)
	}
	if res == nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

func (r *sqliteRepo) GetTgThreadID(tgChatID int64) int {
	var id int
	r.db.QueryRow("SELECT COALESCE(tg_thread_id, 0) FROM pairs WHERE tg_chat_id = ?", tgChatID).Scan(&id)
	return id
}

func (r *sqliteRepo) SetTgThreadID(tgChatID int64, threadID int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, err := r.db.Exec("UPDATE pairs SET tg_thread_id = ? WHERE tg_chat_id = ?", threadID, tgChatID)
	return err
}

func (r *sqliteRepo) StartThreadBridge(tgChatID int64, threadID int) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Уже есть ожидающий ключ для этого треда — возвращаем его.
	var existing string
	err := r.db.QueryRow(
		"SELECT key FROM pending WHERE platform = 'tg' AND chat_id = ? AND thread_id = ? AND command = 'thread-bridge'",
		tgChatID, threadID).Scan(&existing)
	if err == nil {
		return existing, nil
	}

	generated := genKey()
	_, err = r.db.Exec(
		"INSERT INTO pending (key, platform, chat_id, thread_id, created_at, command) VALUES (?, 'tg', ?, ?, ?, 'thread-bridge')",
		generated, tgChatID, threadID, time.Now().Unix())
	return generated, err
}

func (r *sqliteRepo) CompleteThreadBridge(key string, maxChatID int64) (int64, int, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	var tgChatID int64
	var threadID int
	err := r.db.QueryRow(
		"SELECT chat_id, thread_id FROM pending WHERE key = ? AND platform = 'tg' AND command = 'thread-bridge'",
		key).Scan(&tgChatID, &threadID)
	if err != nil {
		return 0, 0, false, nil
	}

	// MAX-чат не должен быть в обычной паре или в другом thread-bridge.
	var cnt int
	r.db.QueryRow("SELECT COUNT(*) FROM pairs WHERE max_chat_id = ?", maxChatID).Scan(&cnt)
	if cnt > 0 {
		return 0, 0, false, errThreadMaxBusy
	}
	cnt = 0
	r.db.QueryRow("SELECT COUNT(*) FROM thread_pairs WHERE max_chat_id = ?", maxChatID).Scan(&cnt)
	if cnt > 0 {
		return 0, 0, false, errThreadMaxBusy
	}

	r.db.Exec("DELETE FROM pending WHERE key = ?", key)
	_, err = r.db.Exec(
		"INSERT OR REPLACE INTO thread_pairs (tg_chat_id, tg_thread_id, max_chat_id, created_at) VALUES (?, ?, ?, ?)",
		tgChatID, threadID, maxChatID, time.Now().Unix())
	if err != nil {
		return 0, 0, false, err
	}
	return tgChatID, threadID, true, nil
}

func (r *sqliteRepo) GetThreadMaxChat(tgChatID int64, threadID int) (int64, bool) {
	// threadID==0 — это General-топик форума (валидная тред-связка), НЕ отбиваем:
	// если для (чат, 0) связки нет — вернётся пусто, и вызывающий уйдёт на групповую.
	var id int64
	err := r.db.QueryRow(
		"SELECT max_chat_id FROM thread_pairs WHERE tg_chat_id = ? AND tg_thread_id = ?",
		tgChatID, threadID).Scan(&id)
	return id, err == nil
}

func (r *sqliteRepo) GetThreadTgPair(maxChatID int64) (int64, int, bool) {
	var tgChatID int64
	var threadID int
	err := r.db.QueryRow(
		"SELECT tg_chat_id, tg_thread_id FROM thread_pairs WHERE max_chat_id = ?",
		maxChatID).Scan(&tgChatID, &threadID)
	return tgChatID, threadID, err == nil
}

func (r *sqliteRepo) UnpairThread(tgChatID int64, threadID int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	res, err := r.db.Exec("DELETE FROM thread_pairs WHERE tg_chat_id = ? AND tg_thread_id = ?", tgChatID, threadID)
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

func (r *sqliteRepo) UnpairThreadByMax(maxChatID int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	res, err := r.db.Exec("DELETE FROM thread_pairs WHERE max_chat_id = ?", maxChatID)
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

func (r *sqliteRepo) PairCrosspost(tgChatID, maxChatID, ownerID, tgOwnerID int64) error {
	_, err := r.db.Exec("INSERT OR REPLACE INTO crossposts (tg_chat_id, max_chat_id, created_at, owner_id, tg_owner_id) VALUES (?, ?, ?, ?, ?)",
		tgChatID, maxChatID, time.Now().Unix(), ownerID, tgOwnerID)
	return err
}

func (r *sqliteRepo) GetCrosspostOwner(maxChatID int64) (maxOwner, tgOwner int64) {
	r.db.QueryRow("SELECT owner_id, tg_owner_id FROM crossposts WHERE max_chat_id = ? AND deleted_at = 0", maxChatID).Scan(&maxOwner, &tgOwner)
	return
}

func (r *sqliteRepo) SaveDiscussionMessage(channelChatID int64, channelMsgID int, discChatID int64, discMsgID int) error {
	_, err := r.db.Exec(`INSERT OR REPLACE INTO discussion_map
		(channel_chat_id, channel_msg_id, disc_chat_id, disc_msg_id, created_at) VALUES (?, ?, ?, ?, ?)`,
		channelChatID, channelMsgID, discChatID, discMsgID, time.Now().Unix())
	return err
}

func (r *sqliteRepo) RecordBotChat(platform string, chatID int64, title, chatType string) {
	_, _ = r.db.Exec(`INSERT INTO bot_chats (platform, chat_id, title, chat_type, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(platform, chat_id) DO UPDATE SET title=excluded.title, chat_type=excluded.chat_type, updated_at=excluded.updated_at`,
		platform, chatID, title, chatType, time.Now().Unix())
}

func (r *sqliteRepo) ListBotChats(platform string, limit int) []BotChatRef {
	out := []BotChatRef{}
	rows, err := r.db.Query(`SELECT chat_id, COALESCE(title,''), COALESCE(chat_type,'')
		FROM bot_chats WHERE platform=? ORDER BY updated_at DESC LIMIT ?`, platform, limit)
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

func (r *sqliteRepo) LookupChannelByDiscussion(discChatID int64, discMsgID int) (int64, int, bool) {
	var cc int64
	var cm int
	err := r.db.QueryRow(`SELECT channel_chat_id, channel_msg_id FROM discussion_map
		WHERE disc_chat_id = ? AND disc_msg_id = ?`, discChatID, discMsgID).Scan(&cc, &cm)
	return cc, cm, err == nil
}

func (r *sqliteRepo) GetCrosspostMaxChat(tgChatID int64) (int64, string, bool) {
	var id int64
	var dir string
	err := r.db.QueryRow("SELECT max_chat_id, direction FROM crossposts WHERE tg_chat_id = ? AND deleted_at = 0", tgChatID).Scan(&id, &dir)
	return id, dir, err == nil
}

func (r *sqliteRepo) GetCrosspostTgChat(maxChatID int64) (int64, string, bool) {
	var id int64
	var dir string
	err := r.db.QueryRow("SELECT tg_chat_id, direction FROM crossposts WHERE max_chat_id = ? AND deleted_at = 0", maxChatID).Scan(&id, &dir)
	return id, dir, err == nil
}

func (r *sqliteRepo) ListCrossposts(ownerID int64) []CrosspostLink {
	rows, err := r.db.Query("SELECT tg_chat_id, max_chat_id, direction FROM crossposts WHERE (owner_id = ? OR tg_owner_id = ? OR (owner_id = 0 AND tg_owner_id = 0)) AND deleted_at = 0", ownerID, ownerID)
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

func (r *sqliteRepo) CountCrossposts(maxOwner, tgOwner int64) int {
	var n int
	r.db.QueryRow(`SELECT COUNT(*) FROM crossposts WHERE deleted_at=0
		AND ((owner_id=? AND owner_id!=0) OR (tg_owner_id=? AND tg_owner_id!=0))`, maxOwner, tgOwner).Scan(&n)
	return n
}

func (r *sqliteRepo) CrosspostRank(maxOwner, tgOwner, maxChatID int64) int {
	var n int
	r.db.QueryRow(`SELECT COUNT(*) FROM crossposts WHERE deleted_at=0
		AND ((owner_id=? AND owner_id!=0) OR (tg_owner_id=? AND tg_owner_id!=0))
		AND created_at < (SELECT created_at FROM crossposts WHERE max_chat_id=? AND deleted_at=0 LIMIT 1)`,
		maxOwner, tgOwner, maxChatID).Scan(&n)
	return n
}

func (r *sqliteRepo) SetCrosspostDirection(maxChatID int64, direction string) bool {
	res, _ := r.db.Exec("UPDATE crossposts SET direction = ? WHERE max_chat_id = ? AND deleted_at = 0", direction, maxChatID)
	if res == nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

func (r *sqliteRepo) UnpairCrosspost(maxChatID, deletedBy int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	res, _ := r.db.Exec("UPDATE crossposts SET deleted_at = ?, deleted_by = ? WHERE max_chat_id = ? AND deleted_at = 0",
		time.Now().Unix(), deletedBy, maxChatID)
	if res == nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

func (r *sqliteRepo) GetCrosspostReplacements(maxChatID int64) CrosspostReplacements {
	var raw string
	r.db.QueryRow("SELECT replacements FROM crossposts WHERE max_chat_id = ? AND deleted_at = 0", maxChatID).Scan(&raw)
	return parseCrosspostReplacements(raw)
}

func (r *sqliteRepo) SetCrosspostReplacements(maxChatID int64, repl CrosspostReplacements) error {
	data := marshalCrosspostReplacements(repl)
	r.mu.Lock()
	defer r.mu.Unlock()
	_, err := r.db.Exec("UPDATE crossposts SET replacements = ? WHERE max_chat_id = ? AND deleted_at = 0", data, maxChatID)
	return err
}

func (r *sqliteRepo) GetCrosspostSyncEdits(maxChatID int64) bool {
	var v int
	r.db.QueryRow("SELECT COALESCE(sync_edits, 0) FROM crossposts WHERE max_chat_id = ? AND deleted_at = 0", maxChatID).Scan(&v)
	return v != 0
}

func (r *sqliteRepo) SetCrosspostSyncEdits(maxChatID int64, on bool) error {
	v := 0
	if on {
		v = 1
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_, err := r.db.Exec("UPDATE crossposts SET sync_edits = ? WHERE max_chat_id = ? AND deleted_at = 0", v, maxChatID)
	return err
}

// --- Пауза связок (временно не пересылаем, связку не удаляем) ---

func (r *sqliteRepo) PairPaused(tgChatID, maxChatID int64) bool {
	var v int
	r.db.QueryRow("SELECT COALESCE(paused, 0) FROM pairs WHERE tg_chat_id = ? AND max_chat_id = ?", tgChatID, maxChatID).Scan(&v)
	return v != 0
}

func (r *sqliteRepo) SetPairPaused(tgChatID, maxChatID int64, paused bool) error {
	v := 0
	if paused {
		v = 1
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_, err := r.db.Exec("UPDATE pairs SET paused = ? WHERE tg_chat_id = ? AND max_chat_id = ?", v, tgChatID, maxChatID)
	return err
}

func (r *sqliteRepo) CrosspostPaused(maxChatID int64) bool {
	var v int
	r.db.QueryRow("SELECT COALESCE(paused, 0) FROM crossposts WHERE max_chat_id = ? AND deleted_at = 0", maxChatID).Scan(&v)
	return v != 0
}

func (r *sqliteRepo) SetCrosspostPaused(maxChatID int64, paused bool) error {
	v := 0
	if paused {
		v = 1
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_, err := r.db.Exec("UPDATE crossposts SET paused = ? WHERE max_chat_id = ? AND deleted_at = 0", v, maxChatID)
	return err
}

func (r *sqliteRepo) ClaimCrosspost(platform string, chatID int64, msgID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now().Unix()
	r.db.Exec("DELETE FROM crosspost_seen WHERE at < ?", now-3600) // TTL: 1ч
	res, err := r.db.Exec(
		"INSERT OR IGNORE INTO crosspost_seen (platform, chat_id, msg_id, at) VALUES (?, ?, ?, ?)",
		platform, chatID, msgID, now)
	if err != nil {
		return true // на ошибке БД не блокируем доставку (лучше риск дубля, чем потеря)
	}
	n, _ := res.RowsAffected()
	return n > 0
}

func (r *sqliteRepo) TgOwnerForChat(chatID int64) int64 {
	var owner int64
	r.db.QueryRow("SELECT tg_owner_id FROM pairs WHERE tg_chat_id = ? OR max_chat_id = ?", chatID, chatID).Scan(&owner)
	if owner != 0 {
		return owner
	}
	r.db.QueryRow("SELECT tg_owner_id FROM crossposts WHERE (tg_chat_id = ? OR max_chat_id = ?) AND deleted_at = 0", chatID, chatID).Scan(&owner)
	return owner
}

func (r *sqliteRepo) TouchUser(userID int64, platform, username, firstName string) {
	now := time.Now().Unix()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.db.Exec(`INSERT INTO users (user_id, platform, username, first_name, first_seen, last_seen) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET username=excluded.username, first_name=excluded.first_name, last_seen=excluded.last_seen`,
		userID, platform, username, firstName, now, now)
}

func (r *sqliteRepo) FindUserByUsername(platform, username string) (int64, bool) {
	var id int64
	err := r.db.QueryRow(`SELECT user_id FROM users WHERE platform = ? AND username = ? COLLATE NOCASE ORDER BY last_seen DESC LIMIT 1`,
		platform, username).Scan(&id)
	return id, err == nil
}

func (r *sqliteRepo) ListUsers(platform string) ([]int64, error) {
	rows, err := r.db.Query("SELECT user_id FROM users WHERE platform = ?", platform)
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

func (r *sqliteRepo) EnqueueSend(item *QueueItem) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, err := r.db.Exec(
		`INSERT INTO send_queue (direction, src_chat_id, dst_chat_id, src_msg_id, text, att_type, att_token, reply_to, format, att_url, parse_mode, attempts, created_at, next_retry)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?)`,
		item.Direction, item.SrcChatID, item.DstChatID, item.SrcMsgID,
		item.Text, item.AttType, item.AttToken, item.ReplyTo, item.Format,
		item.AttURL, item.ParseMode,
		item.CreatedAt, item.NextRetry,
	)
	return err
}

func (r *sqliteRepo) PeekQueue(limit int) ([]QueueItem, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rows, err := r.db.Query(
		`SELECT id, direction, src_chat_id, dst_chat_id, src_msg_id, text, att_type, att_token, reply_to, format, att_url, parse_mode, attempts, created_at, next_retry
		 FROM send_queue WHERE next_retry <= ? ORDER BY id ASC LIMIT ?`,
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

func (r *sqliteRepo) DeleteFromQueue(id int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, err := r.db.Exec("DELETE FROM send_queue WHERE id = ?", id)
	return err
}

func (r *sqliteRepo) IncrementAttempt(id int64, nextRetry int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, err := r.db.Exec("UPDATE send_queue SET attempts = attempts + 1, next_retry = ? WHERE id = ?", nextRetry, id)
	return err
}

func (r *sqliteRepo) HasPendingQueue(direction string, dstChatID int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	var count int
	r.db.QueryRow("SELECT COUNT(*) FROM send_queue WHERE direction = ? AND dst_chat_id = ?", direction, dstChatID).Scan(&count)
	return count > 0
}

func (r *sqliteRepo) Close() error {
	return r.db.Close()
}
