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

func (r *pgRepo) Register(key, platform string, chatID int64) (bool, string, error) {
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

	_, err = r.db.Exec(
		"INSERT INTO pairs (tg_chat_id, max_chat_id) VALUES ($1, $2) ON CONFLICT (tg_chat_id, max_chat_id) DO NOTHING",
		tgID, maxID)
	return true, "", err
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

func (r *pgRepo) RegisterCrosspost(key, platform string, chatID int64) (bool, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if key == "" {
		var existing string
		err := r.db.QueryRow("SELECT key FROM pending WHERE platform = $1 AND chat_id = $2 AND command = 'crosspost'", platform, chatID).Scan(&existing)
		if err == nil {
			return false, existing, nil
		}
		generated := genKey()
		_, err = r.db.Exec("INSERT INTO pending (key, platform, chat_id, created_at, command) VALUES ($1, $2, $3, $4, 'crosspost')", generated, platform, chatID, time.Now().Unix())
		return false, generated, err
	}

	var peerPlatform string
	var peerChatID int64
	err := r.db.QueryRow("SELECT platform, chat_id FROM pending WHERE key = $1 AND command = 'crosspost'", key).Scan(&peerPlatform, &peerChatID)
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

	_, err = r.db.Exec(
		"INSERT INTO crossposts (tg_chat_id, max_chat_id, created_at) VALUES ($1, $2, $3) ON CONFLICT (tg_chat_id, max_chat_id) DO NOTHING",
		tgID, maxID, time.Now().Unix())
	return true, "", err
}

func (r *pgRepo) GetCrosspostMaxChat(tgChatID int64) (int64, string, bool) {
	var id int64
	var dir string
	err := r.db.QueryRow("SELECT max_chat_id, direction FROM crossposts WHERE tg_chat_id = $1", tgChatID).Scan(&id, &dir)
	return id, dir, err == nil
}

func (r *pgRepo) GetCrosspostTgChat(maxChatID int64) (int64, string, bool) {
	var id int64
	var dir string
	err := r.db.QueryRow("SELECT tg_chat_id, direction FROM crossposts WHERE max_chat_id = $1", maxChatID).Scan(&id, &dir)
	return id, dir, err == nil
}

func (r *pgRepo) SetCrosspostDirection(platform string, chatID int64, direction string) bool {
	var res sql.Result
	if platform == "tg" {
		res, _ = r.db.Exec("UPDATE crossposts SET direction = $1 WHERE tg_chat_id = $2", direction, chatID)
	} else {
		res, _ = r.db.Exec("UPDATE crossposts SET direction = $1 WHERE max_chat_id = $2", direction, chatID)
	}
	if res == nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

func (r *pgRepo) UnpairCrosspost(platform string, chatID int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	var res sql.Result
	if platform == "tg" {
		res, _ = r.db.Exec("DELETE FROM crossposts WHERE tg_chat_id = $1", chatID)
	} else {
		res, _ = r.db.Exec("DELETE FROM crossposts WHERE max_chat_id = $1", chatID)
	}
	if res == nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

func (r *pgRepo) Close() error {
	return r.db.Close()
}
