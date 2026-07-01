// commenter — комментарии для постов MAX-канала (которых в MAX нет нативно).
// Открывается как мини-апп по кнопке «💬 Комментарии» под кросспост-постом.
// MVP-бэкенд: stdlib + in-memory store. SQLite и кросс-синк с TG — следующими шагами.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"commenter/billing"
)

func parseUint(s string, def uint64) uint64 {
	if v, err := strconv.ParseUint(s, 10, 64); err == nil {
		return v
	}
	return def
}

func main() {
	addr := flag.String("addr", envOr("ADDR", ":8090"), "listen address")
	staticDir := flag.String("static", envOr("STATIC_DIR", "../frontend/dist"), "built frontend dir")
	dbPath := flag.String("db", envOr("DB_PATH", "comments.db"), "sqlite db path")
	flag.Parse()

	// Персистентное хранилище комментов; при сбое — in-memory (не теряем работоспособность).
	var st store
	if s, err := newSQLiteStore(*dbPath); err != nil {
		log.Printf("sqlite open failed (%v), using in-memory store", err)
		st = newMemStore()
	} else {
		log.Printf("sqlite store: %s", *dbPath)
		st = s
	}
	srv := &server{store: st}

	// Биллинг T-Bank (рекуррент). Включается, если заданы TBANK_TERMINAL_KEY/PASSWORD.
	if dbForBilling, ok := st.(*sqliteStore); ok {
		bcfg := billing.Config{
			TerminalKey:   os.Getenv("TBANK_TERMINAL_KEY"),
			Password:      os.Getenv("TBANK_PASSWORD"),
			NotifyURL:     os.Getenv("TBANK_NOTIFY_URL"),
			SuccessURL:    os.Getenv("TBANK_SUCCESS_URL"),
			FailURL:       os.Getenv("TBANK_FAIL_URL"),
			AmountKopecks: parseUint(os.Getenv("TBANK_AMOUNT"), 29900),
			ReceiptEmail:  os.Getenv("TBANK_RECEIPT_EMAIL"),
		}
		if bcfg.TerminalKey != "" {
			if b, err := billing.New(dbForBilling.db, bcfg); err != nil {
				log.Printf("billing init failed: %v", err)
			} else {
				srv.billing = b
				go b.RunCharger(context.Background())
				log.Printf("billing (T-Bank) enabled, amount=%d", bcfg.AmountKopecks)
			}
		}

		// Отдельный cert-клиент на DEMO-терминале для сертификации (чеки/возвраты).
		// Боевой терминал не трогаем. Включается, если задан TBANK_CERT_TERMINAL_KEY.
		if ck := os.Getenv("TBANK_CERT_TERMINAL_KEY"); ck != "" {
			ccfg := billing.Config{
				TerminalKey:   ck,
				Password:      os.Getenv("TBANK_CERT_PASSWORD"),
				NotifyURL:     os.Getenv("TBANK_NOTIFY_URL"),
				SuccessURL:    os.Getenv("TBANK_SUCCESS_URL"),
				FailURL:       os.Getenv("TBANK_FAIL_URL"),
				AmountKopecks: parseUint(os.Getenv("TBANK_AMOUNT"), 29900),
				ReceiptEmail:  os.Getenv("TBANK_RECEIPT_EMAIL"),
				Taxation:      os.Getenv("TBANK_TAXATION"),
			}
			if c, err := billing.New(dbForBilling.db, ccfg); err != nil {
				log.Printf("cert billing init failed: %v", err)
			} else {
				srv.certBilling = c
				log.Printf("cert billing (DEMO) enabled, terminal=%s receipt=%v", ck, ccfg.ReceiptEmail != "")
			}
		}
	}

	mux := http.NewServeMux()
	// API
	mux.HandleFunc("/api/comments", srv.handleComments) // GET ?post_id= | POST {post_id,text}
	mux.HandleFunc("/api/whoami", srv.handleWhoami)     // кто вошёл (проверка авторизации)
	mux.HandleFunc("/api/settings", srv.handleSettings) // данные кабинета (кросспосты/баланс/PRO)
	mux.HandleFunc("/api/billing/subscribe", srv.handleSubscribe)
	mux.HandleFunc("/api/billing/cancel", srv.handleCancelSub)
	mux.HandleFunc("/api/billing/resume", srv.handleResume)
	mux.HandleFunc("/api/admin/stats", srv.handleAdminStats) // бизнес-показатели (только админ)
	mux.HandleFunc("/api/billing/trial", srv.handleTrial)
	mux.HandleFunc("/api/billing/tbank-notify", srv.handleTbankNotify)
	mux.HandleFunc("/api/internal/comment", srv.handleInternalComment) // приём комментов из TG-обсуждения (бридж)
	mux.HandleFunc("/api/link/start", srv.handleLinkStart)             // выдать одноразовый код привязки MAX→TG
	mux.HandleFunc("/api/internal/link", srv.handleLinkComplete)       // бридж: погасить код /link <код>
	mux.HandleFunc("/api/posts/pay", srv.handlePostsPay)               // покупка постов импорта (T-Bank, из бота)
	mux.HandleFunc("/api/posts/buy", srv.handleBuyPosts)               // докупка постов из кабинета (initData)
	mux.HandleFunc("/api/crosspost/delete", srv.handleDeleteCrosspost)         // удалить связку (владелец)
	mux.HandleFunc("/api/crosspost/comments", srv.handleSetComments)           // вкл/выкл комментарии (PRO)
	mux.HandleFunc("/api/crosspost/replacements", srv.handleSetReplacements)   // сохранить замены (владелец)
	mux.HandleFunc("/api/crosspost/sync-edits", srv.handleSyncEdits)           // синк правок (владелец)
	mux.HandleFunc("/api/crosspost/antispam", srv.handleSetAntispam)           // антиспам связки (PRO)
	mux.HandleFunc("/api/crosspost/pause", srv.handleSetPaused)               // пауза связки кросспоста (владелец)
	mux.HandleFunc("/api/group/prefix", srv.handleSetGroupPrefix)             // префикс [TG]/[MAX] (админ группы)
	mux.HandleFunc("/api/group/pause", srv.handleSetGroupPaused)             // пауза связки группы (админ)
	mux.HandleFunc("/api/group/unbridge", srv.handleUnbridgeGroup)            // разорвать связку группы (админ)
	mux.HandleFunc("/api/group/antispam", srv.handleSetGroupAntispam)         // антиспам группы (PRO, админ)
	mux.HandleFunc("/api/antispam/check", srv.handleBotAdminCheck)            // перепроверить права бота в группе
	mux.HandleFunc("/api/blocks", srv.handleBlocks)                           // журнал заблокированных антиспамом
	mux.HandleFunc("/api/block/unban", srv.handleUnban)                       // разбанить (TG-мут/MAX-возврат)
	mux.HandleFunc("/api/debug", srv.handleDebug)       // временный сбор launch-контекста мини-аппа
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	// Статика мини-аппа (собранный Vue). Кэш: хэшированные ассеты — навсегда (immutable),
	// index.html — no-cache, иначе вебвью MAX/TG держит старый index с битым JS-хэшем
	// (после редеплоя dist старые хэши удаляются → 404 → вечная загрузка).
	mux.Handle("/", cacheHeaders(http.FileServer(http.Dir(*staticDir))))

	log.Printf("commenter listening on %s (static: %s)", *addr, *staticDir)
	if err := http.ListenAndServe(*addr, withCORS(mux)); err != nil {
		log.Fatal(err)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// cacheHeaders — кэш-политика статики: хэшированные ассеты вечны, остальное (index.html,
// SPA-оболочка) — no-cache, чтобы вебвью всегда подтягивал актуальный index с живым JS-хэшем.
func cacheHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache, must-revalidate")
		}
		next.ServeHTTP(w, r)
	})
}

// withCORS — мини-апп открывается во вебвью MAX/TG, нужен доступ к API.
func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Init-Data")
		// API-ответы (PRO-статус, настройки) никогда не кэшируем — вебвью MAX/TG
		// агрессивно кэширует GET, иначе показывает протухший «нет PRO».
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store, must-revalidate")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
