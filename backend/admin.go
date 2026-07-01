package main

import (
	"database/sql"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// adminIDs — id владельцев/админов кабинета (ADMIN_USER_IDS, через запятую).
// Фолбэк — известные id владельца (TG + привязанный MAX), чтобы админка работала и без env.
func adminIDs() []int64 {
	raw := os.Getenv("ADMIN_USER_IDS")
	if raw == "" {
		return []int64{336903139, 11778263}
	}
	var ids []int64
	for _, s := range strings.Split(raw, ",") {
		if v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64); err == nil {
			ids = append(ids, v)
		}
	}
	return ids
}

// isAdminUser — админ ли вошедший (по своему id или привязанному TG-id).
func (s *server) isAdminUser(u user) bool {
	ids := adminIDs()
	cands := []int64{u.ID}
	if tg := s.effectiveTgID(u); tg != 0 {
		cands = append(cands, tg)
	}
	for _, c := range cands {
		for _, a := range ids {
			if a == c {
				return true
			}
		}
	}
	return false
}

// handleAdminStats — бизнес-показатели для админ-панели (только владелец).
func (s *server) handleAdminStats(w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	if !u.Valid {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	if !s.isAdminUser(u) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "forbidden"})
		return
	}

	out := map[string]any{}
	// Подписки + выручка — из биллинга (comments.db).
	if s.billing != nil {
		out["billing"] = s.billing.Stats()
	}

	cnt := func(path, q string, args ...any) int64 {
		if path == "" {
			return 0
		}
		db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(3000)")
		if err != nil {
			return 0
		}
		defer db.Close()
		var n int64
		db.QueryRow(q, args...).Scan(&n)
		return n
	}

	// Пользователи и контент (bridge.db).
	now := time.Now().Unix()
	day := int64(86400)
	out["users"] = map[string]any{
		"total":     cnt(bridgeDBPath, `SELECT COUNT(*) FROM users`),
		"tg":        cnt(bridgeDBPath, `SELECT COUNT(*) FROM users WHERE platform='tg'`),
		"max":       cnt(bridgeDBPath, `SELECT COUNT(*) FROM users WHERE platform='max'`),
		"new_today": cnt(bridgeDBPath, `SELECT COUNT(*) FROM users WHERE first_seen >= ?`, now-day),
		"new_7d":    cnt(bridgeDBPath, `SELECT COUNT(*) FROM users WHERE first_seen >= ?`, now-7*day),
		"active_7d": cnt(bridgeDBPath, `SELECT COUNT(*) FROM users WHERE last_seen >= ?`, now-7*day),
	}
	out["content"] = map[string]any{
		"crossposts":       cnt(bridgeDBPath, `SELECT COUNT(*) FROM crossposts WHERE deleted_at=0`),
		"comments_enabled": cnt(bridgeDBPath, `SELECT COUNT(*) FROM crossposts WHERE deleted_at=0 AND comments_enabled=1`),
		"bridge_groups":    cnt(bridgeDBPath, `SELECT COUNT(*) FROM pairs`),
		"bot_chats_tg":     cnt(bridgeDBPath, `SELECT COUNT(*) FROM bot_chats WHERE platform='tg'`),
		"bot_chats_max":    cnt(bridgeDBPath, `SELECT COUNT(*) FROM bot_chats WHERE platform='max'`),
	}
	// Антиспам + импорт (addon.db).
	out["antispam"] = map[string]any{
		"enabled_tg":  cnt(addonDBPath, `SELECT COUNT(*) FROM antispam_config WHERE enabled=1 AND platform='tg'`),
		"enabled_max": cnt(addonDBPath, `SELECT COUNT(*) FROM antispam_config WHERE enabled=1 AND platform='max'`),
		"bans":        cnt(addonDBPath, `SELECT COUNT(*) FROM as_blocks WHERE action='ban'`),
		"mutes":       cnt(addonDBPath, `SELECT COUNT(*) FROM as_blocks WHERE action='mute'`),
	}
	out["import"] = map[string]any{
		"jobs_done":      cnt(addonDBPath, `SELECT COUNT(*) FROM jobs WHERE status='done'`),
		"posts_imported": cnt(addonDBPath, `SELECT COUNT(*) FROM imported_posts`),
		"posts_balance":  cnt(addonDBPath, `SELECT COALESCE(SUM(credits),0) FROM entitlements`),
	}
	writeJSON(w, http.StatusOK, out)
}
