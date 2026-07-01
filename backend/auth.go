package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
)

// botTokens — токены ботов, к которым привязан мини-апп. Ими подписывается initData
// (схема Telegram WebApp). MAX_BOT_TOKEN — MAX-бот, TG_BOT_TOKEN — Telegram-бот
// (один мини-апп открывается из обоих). Проверяем подпись каждым по очереди.
var botTokens, botPlatforms = func() ([]string, []string) {
	var t, p []string
	// Каждая env может содержать НЕСКОЛЬКО токенов через запятую — на время смены бота
	// (старый + новый): мини-апп, открытый от любого из них, проходит проверку подписи.
	for _, k := range []struct{ env, plat string }{{"MAX_BOT_TOKEN", "max"}, {"TG_BOT_TOKEN", "tg"}} {
		for _, v := range strings.Split(os.Getenv(k.env), ",") {
			if v = strings.TrimSpace(v); v != "" {
				t = append(t, v)
				p = append(p, k.plat)
			}
		}
	}
	return t, p
}()

// user — авторизованный автор комментария.
type user struct {
	ID       int64
	Name     string
	Valid    bool
	Platform string // "max" | "tg" — какой бот-токен подтвердил подпись
}

// authUser проверяет подпись initData мини-аппа MAX (как Telegram WebApp).
// Данные приходят в заголовке X-Init-Data (строка WebAppData из hash).
func authUser(r *http.Request) user {
	initData := r.Header.Get("X-Init-Data")
	if initData == "" || len(botTokens) == 0 {
		return user{Name: "Гость"}
	}
	vals, err := url.ParseQuery(initData)
	if err != nil {
		return user{Name: "Гость"}
	}
	hash := vals.Get("hash")
	if hash == "" {
		return user{Name: "Гость"}
	}

	// data-check-string: все поля кроме hash, отсортированы по ключу, "k=v" через \n.
	keys := make([]string, 0, len(vals))
	for k := range vals {
		if k != "hash" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(vals.Get(k))
	}
	dcs := b.String()

	// secret = HMAC_SHA256(key="WebAppData", msg=botToken); hash = HMAC_SHA256(secret, dcs).
	// Пробуем каждый токен (MAX/TG) — мини-апп открывается из обоих ботов.
	platform := ""
	for i, tok := range botTokens {
		sk := hmac.New(sha256.New, []byte("WebAppData"))
		sk.Write([]byte(tok))
		mac := hmac.New(sha256.New, sk.Sum(nil))
		mac.Write([]byte(dcs))
		if hmac.Equal([]byte(hex.EncodeToString(mac.Sum(nil))), []byte(hash)) {
			platform = botPlatforms[i] // botTokens порядок: [MAX, TG]
			break
		}
	}
	if platform == "" {
		return user{Name: "Гость"}
	}

	var u struct {
		ID        int64  `json:"id"`
		FirstName string `json:"first_name"`
		LastName  string `json:"last_name"`
	}
	_ = json.Unmarshal([]byte(vals.Get("user")), &u)
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if name == "" {
		name = "Гость"
	}
	return user{ID: u.ID, Name: name, Valid: true, Platform: platform}
}

// startParam достаёт стартовый параметр (наш post_id) из initData.
func startParam(r *http.Request) string {
	vals, err := url.ParseQuery(r.Header.Get("X-Init-Data"))
	if err != nil {
		return ""
	}
	return vals.Get("start_param")
}
