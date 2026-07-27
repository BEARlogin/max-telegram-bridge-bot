package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	cabinetCookieName = "bridge_cabinet_session"
	cabinetLoginTTL   = 10 * time.Minute
	cabinetSessionTTL = 30 * 24 * time.Hour
)

// browserAuthDB указывает на ту же SQLite, что и commenter. В аварийном
// in-memory режиме браузерный вход намеренно недоступен: сессии обязаны переживать рестарт.
var browserAuthDB *sql.DB

func randomCabinetToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func cabinetTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func cleanCabinetName(name string) string {
	name = strings.TrimSpace(name)
	if len([]rune(name)) > 100 {
		name = string([]rune(name)[:100])
	}
	if name == "" {
		return "Пользователь"
	}
	return name
}

func cabinetPublicURL() string {
	if v := strings.TrimRight(os.Getenv("CABINET_PUBLIC_URL"), "/"); v != "" {
		return v
	}
	return "https://maxtelegrambridge.ru/cabinet"
}

func cabinetCookiePath() string {
	if v := strings.TrimSpace(os.Getenv("CABINET_COOKIE_PATH")); v != "" {
		return v
	}
	return "/commenter"
}

func browserSessionUser(r *http.Request) user {
	if browserAuthDB == nil {
		return user{}
	}
	c, err := r.Cookie(cabinetCookieName)
	if err != nil || c.Value == "" {
		return user{}
	}
	var u user
	var expires int64
	err = browserAuthDB.QueryRow(`SELECT user_id, name, platform, expires_at
		FROM cabinet_sessions
		WHERE session_hash=? AND revoked_at=0 AND expires_at>?`,
		cabinetTokenHash(c.Value), time.Now().Unix()).Scan(&u.ID, &u.Name, &u.Platform, &expires)
	if err != nil {
		return user{}
	}
	u.Valid = true
	if u.Name == "" {
		u.Name = "Пользователь"
	}
	// Не пишем на каждый API-запрос: одной отметки в час достаточно.
	_, _ = browserAuthDB.Exec(`UPDATE cabinet_sessions SET last_seen_at=?
		WHERE session_hash=? AND last_seen_at<?`, time.Now().Unix(), cabinetTokenHash(c.Value), time.Now().Add(-time.Hour).Unix())
	return u
}

// handleInternalCabinetLink вызывается только ботом по общему внутреннему секрету.
// Пользователь не может подставить чужой ID в публичной форме.
func (s *server) handleInternalCabinetLink(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || browserAuthDB == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "cabinet unavailable"})
		return
	}
	var in struct {
		UserID   int64  `json:"user_id"`
		Platform string `json:"platform"`
		Name     string `json:"name"`
		Secret   string `json:"secret"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad request"})
		return
	}
	expected := commentSyncSecret()
	if expected == "" || !hmac.Equal([]byte(in.Secret), []byte(expected)) ||
		in.UserID == 0 || (in.Platform != "tg" && in.Platform != "max") {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "forbidden"})
		return
	}
	token, err := randomCabinetToken()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "token"})
		return
	}
	now := time.Now()
	_, err = browserAuthDB.Exec(`INSERT INTO cabinet_login_tokens
		(token_hash,user_id,platform,name,expires_at,created_at) VALUES (?,?,?,?,?,?)`,
		cabinetTokenHash(token), in.UserID, in.Platform, cleanCabinetName(in.Name),
		now.Add(cabinetLoginTTL).Unix(), now.Unix())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "store"})
		return
	}
	// Удаляем только давно протухшие записи, чтобы таблицы не росли бесконечно.
	_, _ = browserAuthDB.Exec(`DELETE FROM cabinet_login_tokens WHERE expires_at<?`, now.Add(-24*time.Hour).Unix())
	_, _ = browserAuthDB.Exec(`DELETE FROM cabinet_sessions WHERE expires_at<?`, now.Add(-24*time.Hour).Unix())
	loginURL := cabinetPublicURL() + "/api/cabinet/login?token=" + url.QueryEscape(token)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "url": loginURL, "ttl_min": int(cabinetLoginTTL / time.Minute)})
}

// handleCabinetLogin атомарно гасит одноразовую ссылку, создаёт браузерную сессию
// и убирает токен из адресной строки редиректом.
func (s *server) handleCabinetLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet || browserAuthDB == nil {
		http.Error(w, "Вход временно недоступен", http.StatusServiceUnavailable)
		return
	}
	token := r.URL.Query().Get("token")
	if len(token) < 32 {
		http.Error(w, "Ссылка недействительна или уже использована", http.StatusUnauthorized)
		return
	}
	tx, err := browserAuthDB.Begin()
	if err != nil {
		http.Error(w, "Не удалось выполнить вход", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()
	var uid int64
	var platform, name string
	hash := cabinetTokenHash(token)
	err = tx.QueryRow(`SELECT user_id,platform,name FROM cabinet_login_tokens
		WHERE token_hash=? AND used_at=0 AND expires_at>?`, hash, time.Now().Unix()).Scan(&uid, &platform, &name)
	if err != nil {
		http.Error(w, "Ссылка недействительна или уже использована", http.StatusUnauthorized)
		return
	}
	res, err := tx.Exec(`UPDATE cabinet_login_tokens SET used_at=? WHERE token_hash=? AND used_at=0`,
		time.Now().Unix(), hash)
	if err != nil {
		http.Error(w, "Не удалось выполнить вход", http.StatusInternalServerError)
		return
	}
	if n, _ := res.RowsAffected(); n != 1 {
		http.Error(w, "Ссылка уже использована", http.StatusUnauthorized)
		return
	}
	session, err := randomCabinetToken()
	if err != nil {
		http.Error(w, "Не удалось выполнить вход", http.StatusInternalServerError)
		return
	}
	now := time.Now()
	_, err = tx.Exec(`INSERT INTO cabinet_sessions
		(session_hash,user_id,platform,name,expires_at,created_at,last_seen_at)
		VALUES (?,?,?,?,?,?,?)`, cabinetTokenHash(session), uid, platform, name,
		now.Add(cabinetSessionTTL).Unix(), now.Unix(), now.Unix())
	if err != nil || tx.Commit() != nil {
		http.Error(w, "Не удалось выполнить вход", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: cabinetCookieName, Value: session, Path: cabinetCookiePath(),
		MaxAge: int(cabinetSessionTTL.Seconds()), Expires: now.Add(cabinetSessionTTL),
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, cabinetPublicURL()+"/", http.StatusSeeOther)
}

func (s *server) handleCabinetLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if c, err := r.Cookie(cabinetCookieName); err == nil && browserAuthDB != nil {
		_, _ = browserAuthDB.Exec(`UPDATE cabinet_sessions SET revoked_at=? WHERE session_hash=?`,
			time.Now().Unix(), cabinetTokenHash(c.Value))
	}
	http.SetCookie(w, &http.Cookie{
		Name: cabinetCookieName, Value: "", Path: cabinetCookiePath(),
		MaxAge: -1, Expires: time.Unix(1, 0), HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// withBrowserCSRF допускает cookie-авторизацию изменяющих запросов только с
// текущего origin. Внутренние bot→server запросы cookie не имеют и не затрагиваются.
func withBrowserCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			if _, err := r.Cookie(cabinetCookieName); err == nil {
				origin := r.Header.Get("Origin")
				parsed, err := url.Parse(origin)
				if err != nil || parsed.Host != r.Host || (parsed.Scheme != "https" && parsed.Scheme != "http") {
					writeJSON(w, http.StatusForbidden, map[string]any{"error": "invalid origin"})
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}
