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

func (r *sqliteRepo) Register(key, platform string, chatID int64) (bool, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if key == "" {
		var existing string
		err := r.db.QueryRow("SELECT key FROM pending WHERE platform = ? AND chat_id = ? AND command = 'bridge'", platform, chatID).Scan(&existing)
		if err == nil {
			return false, existing, nil
		}
		generated := genKey()
		_, err = r.db.Exec("INSERT INTO pending (key, platform, chat_id, created_at, command) VALUES (?, ?, ?, ?, 'bridge')", generated, platform, chatID, time.Now().Unix())
		return false, generated, err
	}

	var peerPlatform string
	var peerChatID int64
	err := r.db.QueryRow("SELECT platform, chat_id FROM pending WHERE key = ? AND command = 'bridge'", key).Scan(&peerPlatform, &peerChatID)
	if err != nil {
		return false, "", nil
	}
	if peerPlatform == platform {
		return false, "", nil
	}

	r.db.Exec("DELETE FROM pending WHERE key = ?", key)

	var tgID, maxID int64
	if platform == "tg" {
		tgID, maxID = chatID, peerChatID
	} else {
		tgID, maxID = peerChatID, chatID
	}

	_, err = r.db.Exec("INSERT OR REPLACE INTO pairs (tg_chat_id, max_chat_id) VALUES (?, ?)", tgID, maxID)
	return true, "", err
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

func (r *sqliteRepo) SaveMsg(tgChatID int64, tgMsgID int, maxChatID int64, maxMsgID string) {
	r.db.Exec("INSERT OR REPLACE INTO messages (tg_chat_id, tg_msg_id, max_chat_id, max_msg_id, created_at) VALUES (?, ?, ?, ?, ?)",
		tgChatID, tgMsgID, maxChatID, maxMsgID, time.Now().Unix())
}

func (r *sqliteRepo) LookupMaxMsgID(tgChatID int64, tgMsgID int) (string, bool) {
	var id string
	err := r.db.QueryRow("SELECT max_msg_id FROM messages WHERE tg_chat_id = ? AND tg_msg_id = ?", tgChatID, tgMsgID).Scan(&id)
	return id, err == nil
}

func (r *sqliteRepo) LookupTgMsgID(maxMsgID string) (int64, int, bool) {
	var chatID int64
	var msgID int
	err := r.db.QueryRow("SELECT tg_chat_id, tg_msg_id FROM messages WHERE max_msg_id = ?", maxMsgID).Scan(&chatID, &msgID)
	return chatID, msgID, err == nil
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
		return true
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

func (r *sqliteRepo) RegisterCrosspost(key, platform string, chatID int64) (bool, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if key == "" {
		var existing string
		err := r.db.QueryRow("SELECT key FROM pending WHERE platform = ? AND chat_id = ? AND command = 'crosspost'", platform, chatID).Scan(&existing)
		if err == nil {
			return false, existing, nil
		}
		generated := genKey()
		_, err = r.db.Exec("INSERT INTO pending (key, platform, chat_id, created_at, command) VALUES (?, ?, ?, ?, 'crosspost')", generated, platform, chatID, time.Now().Unix())
		return false, generated, err
	}

	var peerPlatform string
	var peerChatID int64
	err := r.db.QueryRow("SELECT platform, chat_id FROM pending WHERE key = ? AND command = 'crosspost'", key).Scan(&peerPlatform, &peerChatID)
	if err != nil {
		return false, "", nil
	}
	if peerPlatform == platform {
		return false, "", nil
	}

	r.db.Exec("DELETE FROM pending WHERE key = ?", key)

	var tgID, maxID int64
	if platform == "tg" {
		tgID, maxID = chatID, peerChatID
	} else {
		tgID, maxID = peerChatID, chatID
	}

	_, err = r.db.Exec("INSERT OR REPLACE INTO crossposts (tg_chat_id, max_chat_id, created_at) VALUES (?, ?, ?)", tgID, maxID, time.Now().Unix())
	return true, "", err
}

func (r *sqliteRepo) GetCrosspostMaxChat(tgChatID int64) (int64, string, bool) {
	var id int64
	var dir string
	err := r.db.QueryRow("SELECT max_chat_id, direction FROM crossposts WHERE tg_chat_id = ?", tgChatID).Scan(&id, &dir)
	return id, dir, err == nil
}

func (r *sqliteRepo) GetCrosspostTgChat(maxChatID int64) (int64, string, bool) {
	var id int64
	var dir string
	err := r.db.QueryRow("SELECT tg_chat_id, direction FROM crossposts WHERE max_chat_id = ?", maxChatID).Scan(&id, &dir)
	return id, dir, err == nil
}

func (r *sqliteRepo) SetCrosspostDirection(platform string, chatID int64, direction string) bool {
	var res sql.Result
	if platform == "tg" {
		res, _ = r.db.Exec("UPDATE crossposts SET direction = ? WHERE tg_chat_id = ?", direction, chatID)
	} else {
		res, _ = r.db.Exec("UPDATE crossposts SET direction = ? WHERE max_chat_id = ?", direction, chatID)
	}
	if res == nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

func (r *sqliteRepo) UnpairCrosspost(platform string, chatID int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	var res sql.Result
	if platform == "tg" {
		res, _ = r.db.Exec("DELETE FROM crossposts WHERE tg_chat_id = ?", chatID)
	} else {
		res, _ = r.db.Exec("DELETE FROM crossposts WHERE max_chat_id = ?", chatID)
	}
	if res == nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

func (r *sqliteRepo) HasCrosspostPrefix(platform string, chatID int64) bool {
	var v int
	var err error
	if platform == "tg" {
		err = r.db.QueryRow("SELECT prefix FROM crossposts WHERE tg_chat_id = ?", chatID).Scan(&v)
	} else {
		err = r.db.QueryRow("SELECT prefix FROM crossposts WHERE max_chat_id = ?", chatID).Scan(&v)
	}
	if err != nil {
		return true
	}
	return v == 1
}

func (r *sqliteRepo) SetCrosspostPrefix(platform string, chatID int64, on bool) bool {
	v := 0
	if on {
		v = 1
	}
	var res sql.Result
	if platform == "tg" {
		res, _ = r.db.Exec("UPDATE crossposts SET prefix = ? WHERE tg_chat_id = ?", v, chatID)
	} else {
		res, _ = r.db.Exec("UPDATE crossposts SET prefix = ? WHERE max_chat_id = ?", v, chatID)
	}
	if res == nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

func (r *sqliteRepo) Close() error {
	return r.db.Close()
}
