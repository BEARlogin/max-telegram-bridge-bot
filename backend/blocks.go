package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"
)

// Журнал заблокированных антиспамом (мут TG / бан MAX) + разбан из кабинета.
// Антиспам пишет блокировки в addon.db (as_blocks). Кабинет показывает их владельцу
// (по antispam_config.enabled_by) и умеет разбанить: TG — снять мут, MAX — вернуть.

func maxAPIToken() string { return os.Getenv("MAX_BOT_TOKEN") }

type blockRow struct {
	Platform string `json:"platform"`
	ChatID   int64  `json:"chat_id"`
	UserID   int64  `json:"user_id"`
	Action   string `json:"action"`
	Reason   string `json:"reason"`
	Text     string `json:"text"`
	At       int64  `json:"at"`
	Title    string `json:"title"`
}

// ownerModeratesChat — владелец ли uid/effTg включал антиспам в этом чате.
func (s *server) ownerModeratesChat(uid, effTg int64, platform string, chatID int64) bool {
	if addonDBPath == "" {
		return false
	}
	db, err := sql.Open("sqlite", "file:"+addonDBPath+"?mode=ro&_pragma=busy_timeout(3000)")
	if err != nil {
		return false
	}
	defer db.Close()
	var by int64
	if db.QueryRow(`SELECT enabled_by FROM antispam_config WHERE platform=? AND chat_id=?`, platform, chatID).Scan(&by) != nil {
		return false
	}
	return by == uid || (effTg != 0 && by == effTg)
}

// handleBlocks — список заблокированных в чатах, где владелец включал антиспам.
func (s *server) handleBlocks(w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	if !u.Valid {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	out := []blockRow{}
	if addonDBPath != "" {
		if db, err := sql.Open("sqlite", "file:"+addonDBPath+"?mode=ro&_pragma=busy_timeout(3000)"); err == nil {
			defer db.Close()
			effTg := s.effectiveTgID(u)
			rows, err := db.Query(`SELECT b.platform, b.chat_id, b.user_id, b.action, b.reason, b.text, b.at
				FROM as_blocks b JOIN antispam_config c ON c.platform=b.platform AND c.chat_id=b.chat_id
				WHERE c.enabled_by=? OR (?!=0 AND c.enabled_by=?) ORDER BY b.at DESC LIMIT 200`, u.ID, effTg, effTg)
			if err == nil {
				for rows.Next() {
					var b blockRow
					if rows.Scan(&b.Platform, &b.ChatID, &b.UserID, &b.Action, &b.Reason, &b.Text, &b.At) == nil {
						out = append(out, b)
					}
				}
				rows.Close()
			}
		}
	}
	// названия чатов из bridge.db (после закрытия rows)
	if bridgeDBPath != "" {
		if bdb, err := sql.Open("sqlite", "file:"+bridgeDBPath+"?mode=ro&_pragma=busy_timeout(3000)"); err == nil {
			defer bdb.Close()
			for i := range out {
				bdb.QueryRow(`SELECT title FROM bot_chats WHERE platform=? AND chat_id=?`, out[i].Platform, out[i].ChatID).Scan(&out[i].Title)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"blocks": out})
}

// handleUnban — разбанить: TG снять мут, MAX вернуть участника. Затем убрать из журнала.
func (s *server) handleUnban(w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	if !u.Valid {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	var in struct {
		Platform string `json:"platform"`
		ChatID   int64  `json:"chat_id"`
		UserID   int64  `json:"user_id"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.ChatID == 0 || in.UserID == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad request"})
		return
	}
	if !s.ownerModeratesChat(u.ID, s.effectiveTgID(u), in.Platform, in.ChatID) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "не ваш чат"})
		return
	}
	var err error
	if in.Platform == "tg" {
		err = tgUnmute(in.ChatID, in.UserID)
	} else {
		err = maxAddMember(in.ChatID, in.UserID)
	}
	if err != nil {
		log.Printf("unban failed %s chat=%d user=%d: %v", in.Platform, in.ChatID, in.UserID, err)
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "не удалось разбанить: " + err.Error()})
		return
	}
	// убрать из журнала
	if addonDBPath != "" {
		if db, e := openRW(addonDBPath); e == nil {
			db.Exec(`DELETE FROM as_blocks WHERE platform=? AND chat_id=? AND user_id=?`, in.Platform, in.ChatID, in.UserID)
			db.Close()
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// tgUnmute снимает мут участника TG (restrictChatMember с полными правами).
func tgUnmute(chatID, userID int64) error {
	if tgAPIURL == "" || tgBotToken == "" {
		return errNoTG
	}
	perms := `{"can_send_messages":true,"can_send_audios":true,"can_send_documents":true,"can_send_photos":true,"can_send_videos":true,"can_send_video_notes":true,"can_send_voice_notes":true,"can_send_polls":true,"can_send_other_messages":true,"can_add_web_page_previews":true,"can_change_info":true,"can_invite_users":true,"can_pin_messages":true,"can_manage_topics":true}`
	u := tgAPIURL + "/bot" + tgBotToken + "/restrictChatMember?chat_id=" + strconv.FormatInt(chatID, 10) +
		"&user_id=" + strconv.FormatInt(userID, 10) + "&until_date=0&permissions=" + url.QueryEscape(perms)
	resp, err := httpShort.Get(u)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return errHTTP(resp.StatusCode)
	}
	return nil
}

// maxAddMember возвращает участника в MAX-чат (снимает бан/блок) — AddMembers.
func maxAddMember(chatID, userID int64) error {
	if maxAPIToken() == "" {
		return errNoMAX
	}
	body, _ := json.Marshal(map[string]any{"user_ids": []int64{userID}})
	u := "https://platform-api.max.ru/chats/" + strconv.FormatInt(chatID, 10) + "/members?v=1.2.5"
	req, err := http.NewRequest(http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", maxAPIToken())
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpShort.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return errHTTP(resp.StatusCode)
	}
	return nil
}

var httpShort = &http.Client{Timeout: 10 * time.Second}

type strErr string

func (e strErr) Error() string { return string(e) }

var errNoTG = strErr("tg api not configured")
var errNoMAX = strErr("max token not configured")

func errHTTP(code int) error { return strErr("http " + strconv.Itoa(code)) }
