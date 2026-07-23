package main

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"
)

const groupWelcomeMaxLen = 1000

func readGroupWelcome(tgChatID int64) string {
	if addonDBPath == "" || tgChatID == 0 {
		return ""
	}
	db, err := sql.Open("sqlite", "file:"+addonDBPath+"?mode=ro&_pragma=busy_timeout(3000)")
	if err != nil {
		return ""
	}
	defer db.Close()

	var text string
	_ = db.QueryRow(`SELECT COALESCE(welcome_text,'') FROM antispam_config
		WHERE platform='tg' AND chat_id=?`, tgChatID).Scan(&text)
	return text
}

type welcomeExecer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func writeGroupWelcomeExec(db welcomeExecer, tgChatID, ownerID int64, text string) error {
	if strings.TrimSpace(text) == "" {
		text = ""
		ownerID = 0
	}
	_, err := db.Exec(`INSERT INTO antispam_config
			(platform, chat_id, welcome_text, welcome_by, updated_at)
		VALUES ('tg', ?, ?, ?, ?)
		ON CONFLICT(platform, chat_id) DO UPDATE SET
			welcome_text=excluded.welcome_text,
			welcome_by=excluded.welcome_by,
			updated_at=excluded.updated_at`,
		tgChatID, text, ownerID, time.Now().Unix())
	return err
}

func (s *server) handleSetGroupWelcome(w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	if !u.Valid {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	var in struct {
		TgChatID int64  `json:"tg_chat_id"`
		Enabled  bool   `json:"enabled"`
		Text     string `json:"text"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.TgChatID == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "tg_chat_id required"})
		return
	}
	if !ownsGroup(u.ID, in.TgChatID) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "вы не админ этой группы"})
		return
	}

	text := strings.TrimSpace(in.Text)
	if in.Enabled {
		if !s.userIsPro(u.ID) {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "приветствие — PRO-функция"})
			return
		}
		if text == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "введите текст приветствия"})
			return
		}
		if len([]rune(text)) > groupWelcomeMaxLen {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "максимум 1000 символов"})
			return
		}
	} else {
		text = ""
	}

	if addonDBPath == "" {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "db"})
		return
	}
	db, err := openRW(addonDBPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "db"})
		return
	}
	defer db.Close()
	if err := writeGroupWelcomeExec(db, in.TgChatID, u.ID, text); err != nil {
		log.Printf("group welcome write err uid=%d tg=%d: %v", u.ID, in.TgChatID, err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "не удалось сохранить приветствие"})
		return
	}
	log.Printf("group welcome uid=%d tg=%d enabled=%v", u.ID, in.TgChatID, text != "")
	writeJSON(w, http.StatusOK, map[string]any{"ok": text != "", "welcome_text": text})
}
