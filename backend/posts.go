package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"
)

// Покупка постов импорта через T-Bank. Платёж создаёт commenter (у него есть
// T-Bank клиент), а посты начисляются в addon.db (его владеет бридж-аддон).
// Кнопка покупки в аддоне ведёт сюда с подписанным uid.

func postsAmount() uint64    { return parseUint(os.Getenv("POSTS_AMOUNT"), 25000) }   // 250 ₽
func postsPerPurchase() int  { return int(parseUint(os.Getenv("POSTS_PER_PURCHASE"), 1000)) }
func postsSignSecret() string { return os.Getenv("POSTS_SIGN_SECRET") }

// postsSig — HMAC-SHA256(secret, uid), первые 16 hex. Должна совпадать с подписью в glue бриджа.
func postsSig(uid int64) string {
	mac := hmac.New(sha256.New, []byte(postsSignSecret()))
	mac.Write([]byte(strconv.FormatInt(uid, 10)))
	return hex.EncodeToString(mac.Sum(nil))[:16]
}

// handlePostsPay — GET ?u=<uid>&s=<sig>: создаёт платёж за пакет постов и редиректит
// на страницу оплаты T-Bank. Открывается по кнопке «Купить посты» в боте.
func (s *server) handlePostsPay(w http.ResponseWriter, r *http.Request) {
	if s.billing == nil || !s.billing.Enabled() {
		http.Error(w, "billing disabled", http.StatusServiceUnavailable)
		return
	}
	uid, _ := strconv.ParseInt(r.URL.Query().Get("u"), 10, 64)
	if uid == 0 {
		http.Error(w, "bad uid", http.StatusBadRequest)
		return
	}
	if postsSignSecret() != "" && r.URL.Query().Get("s") != postsSig(uid) {
		http.Error(w, "bad signature", http.StatusForbidden)
		return
	}
	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	url, pid, err := s.billing.PayPosts(uid, postsAmount(), postsPerPurchase(), suffix)
	if err != nil {
		log.Printf("posts pay failed uid=%d: %v", uid, err)
		http.Error(w, "payment init failed", http.StatusBadGateway)
		return
	}
	log.Printf("posts pay ok uid=%d payment_id=%s url=%s", uid, pid, url)
	http.Redirect(w, r, url, http.StatusFound)
}

// grantPosts начисляет посты пользователю в addon.db (идемпотентно по payment_id).
func (s *server) grantPosts(uid int64, paymentID string) {
	posts := postsPerPurchase()
	// Дедуп в нашей БД: одну и ту же оплату T-Bank нотифицирует несколько раз.
	if st, ok := s.store.(*sqliteStore); ok {
		st.db.Exec(`CREATE TABLE IF NOT EXISTS posts_grants (
			payment_id TEXT PRIMARY KEY, user_id INTEGER, posts INTEGER, created_at INTEGER)`)
		res, err := st.db.Exec(`INSERT OR IGNORE INTO posts_grants (payment_id, user_id, posts, created_at)
			VALUES (?, ?, ?, ?)`, paymentID, uid, posts, time.Now().Unix())
		if err != nil {
			log.Printf("posts grant dedup err pid=%s: %v", paymentID, err)
			return
		}
		if n, _ := res.RowsAffected(); n == 0 {
			log.Printf("posts grant skip (dup) pid=%s", paymentID)
			return
		}
	}
	if addonDBPath == "" {
		log.Printf("posts grant: ADDON_DB not set, cannot credit uid=%d", uid)
		return
	}
	db, err := sql.Open("sqlite", "file:"+addonDBPath+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		log.Printf("posts grant open addon.db: %v", err)
		return
	}
	defer db.Close()
	_, err = db.Exec(`INSERT INTO entitlements (user_id, credits, updated_at)
		VALUES (?, ?, strftime('%s','now'))
		ON CONFLICT(user_id) DO UPDATE SET credits = credits + ?, updated_at = strftime('%s','now')`,
		uid, posts, posts)
	if err != nil {
		log.Printf("posts grant credit err uid=%d: %v", uid, err)
		return
	}
	// История платежей (для админ-выручки): posts-платёж, kind='posts'. Идемпотентно
	// по payment_id (notify повторяется). Сумма — текущая цена пакета постов.
	if st, ok := s.store.(*sqliteStore); ok {
		st.db.Exec(`INSERT OR IGNORE INTO payments (payment_id, user_id, order_id, amount, status, kind, at)
			VALUES (?, ?, '', ?, 'CONFIRMED', 'posts', strftime('%s','now'))`,
			paymentID, uid, postsAmount())
	}
	log.Printf("posts granted uid=%d posts=%d pid=%s", uid, posts, paymentID)
}
