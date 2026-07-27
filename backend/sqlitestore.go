package main

import (
	"database/sql"
	"time"

	_ "modernc.org/sqlite" // pure-Go драйвер (без cgo, кросс-компилится с CGO_ENABLED=0)
)

const sqliteSchema = `
CREATE TABLE IF NOT EXISTS comments (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	post_id    TEXT NOT NULL,
	author     TEXT NOT NULL DEFAULT '',
	author_id  INTEGER NOT NULL DEFAULT 0,
	source     TEXT NOT NULL DEFAULT 'max',
	text       TEXT NOT NULL,
	reply_to   INTEGER NOT NULL DEFAULT 0,
	tg_msg_id  INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_comments_post ON comments(post_id, created_at);

-- Антиспам мини-апп-комментов, по TG-каналу связки (ключ из post_id "<tgChat>_<msg>").
CREATE TABLE IF NOT EXISTS cp_antispam (
	tg_chat_id INTEGER PRIMARY KEY,
	enabled    INTEGER NOT NULL DEFAULT 0,
	mode       TEXT NOT NULL DEFAULT 'enforce',
	updated_at INTEGER NOT NULL DEFAULT 0
);

-- Связка MAX-аккаунта с TG-аккаунтом по одноразовому коду (баланс постов/PRO — по TG-id).
CREATE TABLE IF NOT EXISTS account_links (
	max_id  INTEGER PRIMARY KEY,
	tg_id   INTEGER NOT NULL DEFAULT 0,
	code    TEXT NOT NULL DEFAULT '',
	code_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_links_code ON account_links(code);

-- Одноразовый вход в полноценный браузерный кабинет. Секреты храним только
-- в виде SHA-256: утечка БД не превращает незаконченные входы и сессии в ключи.
CREATE TABLE IF NOT EXISTS cabinet_login_tokens (
	token_hash TEXT PRIMARY KEY,
	user_id    INTEGER NOT NULL,
	platform   TEXT NOT NULL,
	name       TEXT NOT NULL DEFAULT '',
	expires_at INTEGER NOT NULL,
	used_at    INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_cabinet_login_expiry ON cabinet_login_tokens(expires_at);

CREATE TABLE IF NOT EXISTS cabinet_sessions (
	session_hash TEXT PRIMARY KEY,
	user_id      INTEGER NOT NULL,
	platform     TEXT NOT NULL,
	name         TEXT NOT NULL DEFAULT '',
	expires_at   INTEGER NOT NULL,
	created_at   INTEGER NOT NULL,
	last_seen_at INTEGER NOT NULL,
	revoked_at   INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_cabinet_session_expiry ON cabinet_sessions(expires_at);
`

type sqliteStore struct {
	db *sql.DB
}

func newSQLiteStore(path string) (*sqliteStore, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // сериализуем записи — исключаем "database is locked"
	if _, err := db.Exec(sqliteSchema); err != nil {
		db.Close()
		return nil, err
	}
	// Идемпотентная миграция для БД, созданных до колонки tg_msg_id (ДО создания
	// индекса по ней — иначе на старой таблице индекс падает «no such column»).
	_, _ = db.Exec(`ALTER TABLE comments ADD COLUMN tg_msg_id INTEGER NOT NULL DEFAULT 0`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_comments_tgmsg ON comments(post_id, tg_msg_id)`)
	// Доливка mode для cp_antispam, созданной до колонки (идемпотентно).
	_, _ = db.Exec(`ALTER TABLE cp_antispam ADD COLUMN mode TEXT NOT NULL DEFAULT 'enforce'`)
	browserAuthDB = db
	return &sqliteStore{db: db}, nil
}

func (s *sqliteStore) List(postID string) []Comment {
	rows, err := s.db.Query(`SELECT id, post_id, author, author_id, source, text, reply_to, tg_msg_id, created_at
		FROM comments WHERE post_id = ? ORDER BY created_at, id`, postID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Comment
	for rows.Next() {
		var c Comment
		if err := rows.Scan(&c.ID, &c.PostID, &c.Author, &c.AuthorID, &c.Source, &c.Text, &c.ReplyTo, &c.TgMsgID, &c.CreatedAt); err != nil {
			continue
		}
		out = append(out, c)
	}
	return out
}

func (s *sqliteStore) Add(c Comment) Comment {
	if c.CreatedAt == 0 {
		c.CreatedAt = time.Now().Unix()
	}
	res, err := s.db.Exec(`INSERT INTO comments (post_id, author, author_id, source, text, reply_to, tg_msg_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, c.PostID, c.Author, c.AuthorID, c.Source, c.Text, c.ReplyTo, c.TgMsgID, c.CreatedAt)
	if err == nil {
		c.ID, _ = res.LastInsertId()
	}
	return c
}

func (s *sqliteStore) FindByTgMsg(postID string, tgMsgID int64) (int64, bool) {
	if tgMsgID == 0 {
		return 0, false
	}
	var id int64
	err := s.db.QueryRow(`SELECT id FROM comments WHERE post_id = ? AND tg_msg_id = ? LIMIT 1`, postID, tgMsgID).Scan(&id)
	return id, err == nil
}

func (s *sqliteStore) SetTgMsg(commentID, tgMsgID int64) {
	_, _ = s.db.Exec(`UPDATE comments SET tg_msg_id = ? WHERE id = ?`, tgMsgID, commentID)
}

func (s *sqliteStore) SetAntispam(tgChatID int64, on bool, mode string) {
	v := 0
	if on {
		v = 1
	}
	if mode == "" {
		mode = "enforce"
	}
	_, _ = s.db.Exec(`INSERT INTO cp_antispam (tg_chat_id, enabled, mode, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(tg_chat_id) DO UPDATE SET enabled=excluded.enabled, mode=excluded.mode, updated_at=excluded.updated_at`,
		tgChatID, v, mode, time.Now().Unix())
}

func (s *sqliteStore) GetAntispam(tgChatID int64) (bool, string) {
	var v int
	var mode string
	s.db.QueryRow(`SELECT enabled, mode FROM cp_antispam WHERE tg_chat_id=?`, tgChatID).Scan(&v, &mode)
	if mode == "" {
		mode = "enforce"
	}
	return v != 0, mode
}

func (s *sqliteStore) LinkNewCode(maxID int64, code string) {
	_, _ = s.db.Exec(`INSERT INTO account_links (max_id, code, code_at) VALUES (?, ?, strftime('%s','now'))
		ON CONFLICT(max_id) DO UPDATE SET code=excluded.code, code_at=excluded.code_at`, maxID, code)
}

// LinkRedeem погашает одноразовый код: привязывает tgID к max_id кода (если код свежий),
// очищает код. Возвращает max_id и успех.
func (s *sqliteStore) LinkRedeem(code string, tgID, ttlSec int64) (int64, bool) {
	var maxID int64
	err := s.db.QueryRow(`SELECT max_id FROM account_links WHERE code=? AND code!=''
		AND code_at > strftime('%s','now') - ?`, code, ttlSec).Scan(&maxID)
	if err != nil {
		return 0, false
	}
	res, _ := s.db.Exec(`UPDATE account_links SET tg_id=?, code='', code_at=0 WHERE max_id=?`, tgID, maxID)
	if res == nil {
		return 0, false
	}
	n, _ := res.RowsAffected()
	return maxID, n > 0
}

func (s *sqliteStore) LinkedTg(maxID int64) int64 {
	var tg int64
	s.db.QueryRow(`SELECT tg_id FROM account_links WHERE max_id=? AND tg_id!=0`, maxID).Scan(&tg)
	return tg
}

func (s *sqliteStore) LinkedMax(tgID int64) int64 {
	var mx int64
	s.db.QueryRow(`SELECT max_id FROM account_links WHERE tg_id=? AND max_id!=0`, tgID).Scan(&mx)
	return mx
}

// AutoLink — автопривязка MAX↔TG (бридж зовёт при создании bridge-связки, где известны
// владельцы обеих сторон). Создаёт связь ТОЛЬКО если ни один из аккаунтов ещё не привязан —
// существующие связи не перезаписываем (иначе можно завладеть чужой подпиской).
func (s *sqliteStore) AutoLink(maxID, tgID int64) bool {
	if maxID == 0 || tgID == 0 {
		return false
	}
	if s.LinkedTg(maxID) != 0 || s.LinkedMax(tgID) != 0 {
		return false
	}
	res, err := s.db.Exec(`INSERT INTO account_links (max_id, tg_id) VALUES (?, ?)
		ON CONFLICT(max_id) DO UPDATE SET tg_id=excluded.tg_id WHERE account_links.tg_id=0`, maxID, tgID)
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}
