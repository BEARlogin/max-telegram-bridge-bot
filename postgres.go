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

func (r *pgRepo) Register(key, platform string, chatID int64, threadID int) (bool, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if key == "" {
		var existing string
		err := r.db.QueryRow("SELECT key FROM pending WHERE platform = $1 AND chat_id = $2 AND command = 'bridge'", platform, chatID).Scan(&existing)
		if err == nil {
			return false, existing, nil
		}
		generated := genKey()
		_, err = r.db.Exec("INSERT INTO pending (key, platform, chat_id, created_at, command) VALUES ($1, $2, $3, $4, 'bridge')", generated, platform, chatID, time.Now().Unix())
		return false, generated, err
	}

	var peerPlatform string
	var peerChatID int64
	err := r.db.QueryRow("SELECT platform, chat_id FROM pending WHERE key = $1 AND command = 'bridge'", key).Scan(&peerPlatform, &peerChatID)
	if err != nil {
		return false, "", nil
	}
	if peerPlatform == platform {
		return false, "", nil
	}

	r.db.Exec("DELETE FROM pending WHERE key = $1", key)

	var tgID, maxID int64
	if platform == "tg" {
		tgID, maxID = chatID, peerChatID
	} else {
		tgID, maxID = peerChatID, chatID
	}

	tgThreadID := 0
	if platform == "tg" {
		tgThreadID = threadID
	}
	_, err = r.db.Exec(
		"INSERT INTO pairs (tg_chat_id, max_chat_id, tg_thread_id) VALUES ($1, $2, $3) ON CONFLICT (tg_chat_id, max_chat_id) DO UPDATE SET tg_thread_id = EXCLUDED.tg_thread_id",
		tgID, maxID, tgThreadID)
	return true, "", err
}

func (r *pgRepo) GetTgThreadID(tgChatID int64) int {
	var id int
	r.db.QueryRow("SELECT tg_thread_id FROM pairs WHERE tg_chat_id = $1", tgChatID).Scan(&id)
	return id
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

func (r *pgRepo) SaveMsg(tgChatID int64, tgMsgID int, maxChatID int64, maxMsgID string) {
	r.db.Exec(
		`INSERT INTO messages (tg_chat_id, tg_msg_id, max_chat_id, max_msg_id, created_at)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (tg_chat_id, tg_msg_id) DO UPDATE
		 SET max_chat_id = EXCLUDED.max_chat_id, max_msg_id = EXCLUDED.max_msg_id, created_at = EXCLUDED.created_at`,
		tgChatID, tgMsgID, maxChatID, maxMsgID, time.Now().Unix())
}

func (r *pgRepo) LookupMaxMsgID(tgChatID int64, tgMsgID int) (string, bool) {
	var id string
	err := r.db.QueryRow("SELECT max_msg_id FROM messages WHERE tg_chat_id = $1 AND tg_msg_id = $2", tgChatID, tgMsgID).Scan(&id)
	return id, err == nil
}

func (r *pgRepo) LookupTgMsgID(maxMsgID string) (int64, int, bool) {
	var chatID int64
	var msgID int
	err := r.db.QueryRow("SELECT tg_chat_id, tg_msg_id FROM messages WHERE max_msg_id = $1", maxMsgID).Scan(&chatID, &msgID)
	return chatID, msgID, err == nil
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
		return true
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

func (r *pgRepo) TouchUser(userID int64, platform, username, firstName string) {
	now := time.Now().Unix()
	r.db.Exec(`INSERT INTO users (user_id, platform, username, first_name, first_seen, last_seen) VALUES ($1, $2, $3, $4, $5, $5)
		ON CONFLICT(user_id) DO UPDATE SET username=EXCLUDED.username, first_name=EXCLUDED.first_name, last_seen=EXCLUDED.last_seen`,
		userID, platform, username, firstName, now)
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

func (r *pgRepo) Close() error {
	return r.db.Close()
}
