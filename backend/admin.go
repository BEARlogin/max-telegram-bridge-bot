package main

import (
	"database/sql"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

type vkAdminAccount struct {
	OwnerID       int64  `json:"owner_id"`
	OwnerUsername string `json:"owner_username,omitempty"`
	OwnerName     string `json:"owner_name,omitempty"`
	CommunityID   int64  `json:"community_id"`
	Enabled       bool   `json:"enabled"`
	ConnectedAt   int64  `json:"connected_at"`
	Endpoints     int64  `json:"endpoints"`
	Bindings      int64  `json:"bindings"`
}

type vkAdminBinding struct {
	ID             int64  `json:"id"`
	OwnerID        int64  `json:"owner_id"`
	OwnerUsername  string `json:"owner_username,omitempty"`
	CommunityID    int64  `json:"community_id"`
	EndpointKind   string `json:"endpoint_kind"`
	EndpointTitle  string `json:"endpoint_title"`
	SourcePlatform string `json:"source_platform"`
	SourceChatID   int64  `json:"source_chat_id"`
	SourceTitle    string `json:"source_title,omitempty"`
	Direction      string `json:"direction"`
	Paused         bool   `json:"paused"`
	CreatedAt      int64  `json:"created_at"`
	Deliveries     int64  `json:"deliveries"`
	LastDeliveryAt int64  `json:"last_delivery_at"`
	QueuePending   int64  `json:"queue_pending"`
	QueueAttempts  int64  `json:"queue_attempts"`
	LastQueueAt    int64  `json:"last_queue_at"`
	LastError      string `json:"last_error,omitempty"`
}

type vkAdminStats struct {
	Available              bool             `json:"available"`
	AccountsTotal          int64            `json:"accounts_total"`
	AccountsEnabled        int64            `json:"accounts_enabled"`
	OwnersTotal            int64            `json:"owners_total"`
	CommunitiesTotal       int64            `json:"communities_total"`
	AccountsWithoutBinding int64            `json:"accounts_without_binding"`
	EndpointsTotal         int64            `json:"endpoints_total"`
	BindingsTotal          int64            `json:"bindings_total"`
	ActiveBindings         int64            `json:"active_bindings"`
	PausedBindings         int64            `json:"paused_bindings"`
	DeliveriesTotal        int64            `json:"deliveries_total"`
	DeliveriesToVK         int64            `json:"deliveries_to_vk"`
	DeliveriesFromVK       int64            `json:"deliveries_from_vk"`
	LastDeliveryAt         int64            `json:"last_delivery_at"`
	QueuePending           int64            `json:"queue_pending"`
	QueueRetrying          int64            `json:"queue_retrying"`
	LastQueueAt            int64            `json:"last_queue_at"`
	Accounts               []vkAdminAccount `json:"accounts"`
	Bindings               []vkAdminBinding `json:"bindings"`
}

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
	out["vk"] = loadVKAdminStats(addonDBPath, bridgeDBPath)
	writeJSON(w, http.StatusOK, out)
}

func loadVKAdminStats(addonPath, bridgePath string) vkAdminStats {
	out := vkAdminStats{
		Accounts: []vkAdminAccount{},
		Bindings: []vkAdminBinding{},
	}
	if addonPath == "" {
		return out
	}
	db, err := sql.Open("sqlite", "file:"+addonPath+"?mode=ro&_pragma=busy_timeout(3000)")
	if err != nil {
		return out
	}
	defer db.Close()
	if err = db.Ping(); err != nil {
		return out
	}
	out.Available = true

	scanCount := func(q string, dest ...any) {
		_ = db.QueryRow(q).Scan(dest...)
	}
	scanCount(`SELECT COUNT(*), COALESCE(SUM(enabled=1),0), COUNT(DISTINCT owner_id),
		COUNT(DISTINCT community_id) FROM vk_accounts`,
		&out.AccountsTotal, &out.AccountsEnabled, &out.OwnersTotal, &out.CommunitiesTotal)
	scanCount(`SELECT COUNT(*) FROM vk_accounts a
		WHERE NOT EXISTS (
			SELECT 1 FROM vk_endpoints e JOIN vk_bindings b ON b.endpoint_id=e.id
			WHERE e.account_id=a.id
		)`, &out.AccountsWithoutBinding)
	scanCount(`SELECT COUNT(*) FROM vk_endpoints`, &out.EndpointsTotal)
	scanCount(`SELECT COUNT(*), COALESCE(SUM(a.enabled=1 AND e.enabled=1 AND b.paused=0),0),
		COALESCE(SUM(b.paused=1),0)
		FROM vk_bindings b
		JOIN vk_endpoints e ON e.id=b.endpoint_id
		JOIN vk_accounts a ON a.id=e.account_id`,
		&out.BindingsTotal, &out.ActiveBindings, &out.PausedBindings)
	scanCount(`SELECT COUNT(*),
		COALESCE(SUM(direction='source>vk'),0),
		COALESCE(SUM(direction='vk>source'),0),
		COALESCE(MAX(created_at),0)
		FROM vk_message_map`,
		&out.DeliveriesTotal, &out.DeliveriesToVK, &out.DeliveriesFromVK, &out.LastDeliveryAt)
	scanCount(`SELECT COUNT(*), COALESCE(SUM(attempts>0),0), COALESCE(MAX(updated_at),0)
		FROM vk_delivery_queue`,
		&out.QueuePending, &out.QueueRetrying, &out.LastQueueAt)

	ownerNames := loadVKOwnerNames(bridgePath)
	rows, err := db.Query(`SELECT a.owner_id,a.community_id,a.enabled,a.created_at,
		COUNT(DISTINCT e.id),COUNT(DISTINCT b.id)
		FROM vk_accounts a
		LEFT JOIN vk_endpoints e ON e.account_id=a.id
		LEFT JOIN vk_bindings b ON b.endpoint_id=e.id
		GROUP BY a.id ORDER BY a.created_at DESC,a.id DESC`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var a vkAdminAccount
			if rows.Scan(&a.OwnerID, &a.CommunityID, &a.Enabled, &a.ConnectedAt, &a.Endpoints, &a.Bindings) == nil {
				a.OwnerUsername = ownerNames[a.OwnerID][0]
				a.OwnerName = ownerNames[a.OwnerID][1]
				out.Accounts = append(out.Accounts, a)
			}
		}
	}

	rows, err = db.Query(`SELECT b.id,b.owner_id,a.community_id,e.kind,e.title,
		b.source_platform,b.source_chat_id,b.direction,b.paused,b.created_at,
		(SELECT COUNT(*) FROM vk_message_map m WHERE m.binding_id=b.id),
		(SELECT COALESCE(MAX(created_at),0) FROM vk_message_map m WHERE m.binding_id=b.id),
		(SELECT COUNT(*) FROM vk_delivery_queue q WHERE q.binding_id=b.id),
		(SELECT COALESCE(MAX(attempts),0) FROM vk_delivery_queue q WHERE q.binding_id=b.id),
		(SELECT COALESCE(MAX(updated_at),0) FROM vk_delivery_queue q WHERE q.binding_id=b.id),
		COALESCE((SELECT last_error FROM vk_delivery_queue q
			WHERE q.binding_id=b.id ORDER BY updated_at DESC,id DESC LIMIT 1),'')
		FROM vk_bindings b
		JOIN vk_endpoints e ON e.id=b.endpoint_id
		JOIN vk_accounts a ON a.id=e.account_id
		ORDER BY b.created_at DESC,b.id DESC`)
	if err == nil {
		defer rows.Close()
		chatTitles := loadVKChatTitles(bridgePath)
		for rows.Next() {
			var b vkAdminBinding
			if rows.Scan(&b.ID, &b.OwnerID, &b.CommunityID, &b.EndpointKind, &b.EndpointTitle,
				&b.SourcePlatform, &b.SourceChatID, &b.Direction, &b.Paused, &b.CreatedAt,
				&b.Deliveries, &b.LastDeliveryAt, &b.QueuePending, &b.QueueAttempts,
				&b.LastQueueAt, &b.LastError) == nil {
				b.OwnerUsername = ownerNames[b.OwnerID][0]
				b.SourceTitle = chatTitles[b.SourcePlatform+":"+strconv.FormatInt(b.SourceChatID, 10)]
				if len(b.LastError) > 240 {
					b.LastError = b.LastError[:240]
				}
				out.Bindings = append(out.Bindings, b)
			}
		}
	}
	return out
}

func loadVKOwnerNames(path string) map[int64][2]string {
	out := map[int64][2]string{}
	if path == "" {
		return out
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(3000)")
	if err != nil {
		return out
	}
	defer db.Close()
	rows, err := db.Query(`SELECT user_id,username,first_name FROM users`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var username, name string
		if rows.Scan(&id, &username, &name) == nil {
			out[id] = [2]string{username, name}
		}
	}
	return out
}

func loadVKChatTitles(path string) map[string]string {
	out := map[string]string{}
	if path == "" {
		return out
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(3000)")
	if err != nil {
		return out
	}
	defer db.Close()
	rows, err := db.Query(`SELECT platform,chat_id,title FROM bot_chats`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var platform, title string
		var chatID int64
		if rows.Scan(&platform, &chatID, &title) == nil {
			out[platform+":"+strconv.FormatInt(chatID, 10)] = title
		}
	}
	return out
}
