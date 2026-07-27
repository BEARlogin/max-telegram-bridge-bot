package main

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// maxGroupCandidates — потолок числа проверок админства за один заход в кабинет.
// pairs могут содержать тысячи связок; перебирать все с getChatMember нельзя.
// Берём только недавно активные группы (по bot_chats.updated_at).
const maxGroupCandidates = 60

// Управление bridge-группами из кабинета. В отличие от кросспостов, у пар (pairs)
// нет владельца в БД, поэтому принадлежность подтверждаем админством юзера в
// TG-группе (кабинет открыт в TG, есть локальный Bot API). Группы, где юзер не
// админ, не показываем — иначе утекли бы чужие связки.

type groupInfo struct {
	TgChatID     int64        `json:"tg_chat_id"`
	MaxChatID    int64        `json:"max_chat_id"`
	TgTitle      string       `json:"tg_title"`
	MaxTitle     string       `json:"max_title"`
	Standalone   bool         `json:"standalone"` // группа без моста — только антиспам
	Prefix       bool         `json:"prefix"`
	Paused       bool         `json:"paused"`
	Direction    string       `json:"direction"` // both | tg>max | max>tg (PRO), пусто у standalone
	Antispam     bool         `json:"antispam"`
	AntispamMode string       `json:"antispam_mode"`
	AllowLinks   bool         `json:"allow_links"`
	StrikeLimit  int          `json:"strike_limit"`
	BanAfter     int          `json:"ban_after"`
	Action       string       `json:"action"`
	MuteMinutes  int          `json:"mute_minutes"`
	Warn         bool         `json:"warn"`
	Notify       string       `json:"notify"`
	Captcha      bool         `json:"captcha"`
	Antiraid     bool         `json:"antiraid"`
	ProfileGuard bool         `json:"profile_guard"`
	BlockWords   string       `json:"block_words"`
	BlockCats    string       `json:"block_cats"`
	DelService   bool         `json:"del_service"`
	Tone         string       `json:"tone"`
	WelcomeText  string       `json:"welcome_text"`
	Rules        []asRuleInfo `json:"rules"`
	BotAdmin     bool         `json:"bot_admin"` // бот админ в группе (для модерации)
}

// tgIsChatAdmin — является ли юзер админом/создателем TG-чата (через локальный Bot API).
func tgIsChatAdmin(chatID, userID int64) bool {
	if tgAPIURL == "" || tgBotToken == "" {
		return false
	}
	u := tgAPIURL + "/bot" + tgBotToken + "/getChatMember?chat_id=" + strconv.FormatInt(chatID, 10) +
		"&user_id=" + strconv.FormatInt(userID, 10)
	resp, err := http.Get(u)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var out struct {
		OK     bool `json:"ok"`
		Result struct {
			Status string `json:"status"`
		} `json:"result"`
	}
	if json.NewDecoder(resp.Body).Decode(&out) != nil || !out.OK {
		return false
	}
	return out.Result.Status == "administrator" || out.Result.Status == "creator"
}

// tgBotSelfID — id самого бота (getMe), кэшируется. 0 при ошибке.
var tgBotSelfIDCache int64

func tgBotSelfID() int64 {
	if tgBotSelfIDCache != 0 {
		return tgBotSelfIDCache
	}
	if tgAPIURL == "" || tgBotToken == "" {
		return 0
	}
	resp, err := http.Get(tgAPIURL + "/bot" + tgBotToken + "/getMe")
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	var out struct {
		OK     bool `json:"ok"`
		Result struct {
			ID int64 `json:"id"`
		} `json:"result"`
	}
	if json.NewDecoder(resp.Body).Decode(&out) == nil && out.OK {
		tgBotSelfIDCache = out.Result.ID
	}
	return tgBotSelfIDCache
}

// tgBotIsAdmin — добавлен ли сам бот админом/создателем в TG-чат (для модерации нужны права).
func tgBotIsAdmin(chatID int64) bool {
	id := tgBotSelfID()
	return id != 0 && tgIsChatAdmin(chatID, id)
}

// botChatTitle достаёт название чата из bot_chats (bridge.db) для красивого показа.
func botChatTitle(db *sql.DB, platform string, chatID int64) string {
	var t string
	db.QueryRow(`SELECT title FROM bot_chats WHERE platform=? AND chat_id=?`, platform, chatID).Scan(&t)
	return t
}

// userGroups возвращает bridge-группы юзера.
//
//  1. Точный путь: pairs.tg_owner_id = userID — кто выполнил /bridge на TG-стороне.
//     Без обращений к TG API, мгновенно. Так связываются все НОВЫЕ группы.
//  2. Фолбэк для старых связок (tg_owner_id=0, заведены до сбора владельца):
//     берём недавно активные (bot_chats) группы без владельца, не более
//     maxGroupCandidates, и проверяем админство юзера через getChatMember.
func userGroups(userID, effTg int64) []groupInfo {
	out := []groupInfo{}
	if bridgeDBPath == "" {
		return out
	}
	db, err := sql.Open("sqlite", "file:"+bridgeDBPath+"?mode=ro&_pragma=busy_timeout(3000)")
	if err != nil {
		return out
	}
	defer db.Close()

	seen := map[int64]bool{}

	// Все спаренные TG-группы (owner-agnostic): tg_chat_id → max_chat_id.
	// Нужна, чтобы не пометить standalone группу, у которой мост есть, но
	// owner в паре не записан (старые связки) и admin-check в шаге (2) не прошёл.
	bridged := map[int64]int64{}
	if br, err := db.Query(`SELECT tg_chat_id, max_chat_id FROM pairs WHERE tg_chat_id < 0`); err == nil {
		for br.Next() {
			var t, m int64
			if br.Scan(&t, &m) == nil {
				bridged[t] = m
			}
		}
		br.Close()
	}

	// (1) Точный путь по владельцу.
	owned, err := db.Query(`SELECT p.tg_chat_id, p.max_chat_id, COALESCE(p.prefix,1), COALESCE(b.title,''), COALESCE(p.paused,0)
		FROM pairs p LEFT JOIN bot_chats b ON b.platform='tg' AND b.chat_id=p.tg_chat_id
		WHERE p.tg_owner_id=? AND p.tg_chat_id < 0`, userID)
	if err == nil {
		type r struct {
			g      groupInfo
			prefix int
			paused int
		}
		var list []r
		for owned.Next() {
			var x r
			if owned.Scan(&x.g.TgChatID, &x.g.MaxChatID, &x.prefix, &x.g.TgTitle, &x.paused) == nil {
				list = append(list, x)
			}
		}
		owned.Close()
		for _, x := range list {
			x.g.Prefix = x.prefix == 1
			x.g.Paused = x.paused == 1
			x.g.MaxTitle = botChatTitle(db, "max", x.g.MaxChatID)
			out = append(out, x.g)
			seen[x.g.TgChatID] = true
		}
	}

	// (2) Фолбэк: старые связки без владельца — ограниченный перебор с проверкой админа.
	rows, err := db.Query(`SELECT p.tg_chat_id, p.max_chat_id, COALESCE(p.prefix,1), COALESCE(b.title,''), COALESCE(p.paused,0)
		FROM pairs p JOIN bot_chats b ON b.platform='tg' AND b.chat_id=p.tg_chat_id
		WHERE p.tg_chat_id < 0 AND COALESCE(p.tg_owner_id,0)=0 ORDER BY b.updated_at DESC LIMIT ?`, maxGroupCandidates)
	if err != nil {
		return out
	}
	type cand struct {
		g   groupInfo
		ord int
	}
	var cands []cand
	for rows.Next() {
		var g groupInfo
		var prefix, paused int
		if rows.Scan(&g.TgChatID, &g.MaxChatID, &prefix, &g.TgTitle, &paused) == nil {
			g.Prefix = prefix == 1
			g.Paused = paused == 1
			cands = append(cands, cand{g: g, ord: len(cands)})
		}
	}
	rows.Close()

	// Проверяем админство параллельно (пул воркеров), сохраняя порядок.
	type res struct {
		g     groupInfo
		ord   int
		admin bool
	}
	results := make([]res, len(cands))
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for i, c := range cands {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, c cand) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = res{g: c.g, ord: c.ord, admin: tgIsChatAdmin(c.g.TgChatID, userID)}
		}(i, c)
	}
	wg.Wait()

	var fb []res
	for _, r := range results {
		if r.admin && !seen[r.g.TgChatID] {
			fb = append(fb, r)
		}
	}
	sort.Slice(fb, func(a, b int) bool { return fb[a].ord < fb[b].ord })
	for _, r := range fb {
		g := r.g
		g.MaxTitle = botChatTitle(db, "max", g.MaxChatID)
		out = append(out, g)
		seen[g.TgChatID] = true
	}

	// (3) Standalone TG-группы: бот в группе, но связки нет — антиспам без моста.
	//     Берём недавно активные группы из bot_chats (не каналы), проверяем админство юзера.
	std, err := db.Query(`SELECT chat_id, COALESCE(title,'') FROM bot_chats
		WHERE platform='tg' AND chat_id < 0 AND chat_type IN ('group','supergroup')
		ORDER BY updated_at DESC LIMIT ?`, maxGroupCandidates)
	if err == nil {
		type sc struct {
			id    int64
			title string
		}
		var list []sc
		for std.Next() {
			var x sc
			if std.Scan(&x.id, &x.title) == nil && !seen[x.id] {
				list = append(list, x)
			}
		}
		std.Close()
		admins := make([]bool, len(list))
		sem := make(chan struct{}, 8)
		var wg sync.WaitGroup
		for i, x := range list {
			wg.Add(1)
			sem <- struct{}{}
			go func(i int, x sc) {
				defer wg.Done()
				defer func() { <-sem }()
				admins[i] = tgIsChatAdmin(x.id, userID)
			}(i, x)
		}
		wg.Wait()
		for i, x := range list {
			if admins[i] && !seen[x.id] {
				gi := groupInfo{TgChatID: x.id, TgTitle: x.title, Standalone: true}
				if mx, ok := bridged[x.id]; ok { // мост есть — не standalone
					gi.Standalone, gi.MaxChatID, gi.MaxTitle = false, mx, botChatTitle(db, "max", mx)
				}
				out = append(out, gi)
				seen[x.id] = true
			}
		}
	}

	// (4) Группы, где юзер настраивал антиспам — показываем ВСЕГДА (не зависят от лимита
	//     bot_chats: тихая группа выпадает из топ-60 по активности, но управление должно остаться).
	if addonDBPath != "" {
		if adb, err := sql.Open("sqlite", "file:"+addonDBPath+"?mode=ro&_pragma=busy_timeout(3000)"); err == nil {
			var ids []int64
			if rows, err := adb.Query(`SELECT chat_id FROM antispam_config
				WHERE platform='tg' AND chat_id < 0
					AND (enabled_by=? OR welcome_by=? OR (?!=0 AND (enabled_by=? OR welcome_by=?)))`,
				userID, userID, effTg, effTg, effTg); err == nil {
				for rows.Next() {
					var id int64
					if rows.Scan(&id) == nil && !seen[id] {
						ids = append(ids, id)
					}
				}
				rows.Close()
			}
			adb.Close()
			for _, id := range ids {
				gi := groupInfo{TgChatID: id, TgTitle: botChatTitle(db, "tg", id), Standalone: true}
				if mx, ok := bridged[id]; ok { // антиспам-группа, но мост есть — не standalone
					gi.Standalone, gi.MaxChatID, gi.MaxTitle = false, mx, botChatTitle(db, "max", mx)
				}
				out = append(out, gi)
				seen[id] = true
			}
		}
	}

	// Направление bridge (both|tg>max|max>tg) — хранится в addon.db (pair_direction).
	// Отсутствие строки = legacy both. Standalone-группам направление не показываем.
	dirs := map[int64]string{}
	if addonDBPath != "" {
		if adb, err := sql.Open("sqlite", "file:"+addonDBPath+"?mode=ro&_pragma=busy_timeout(3000)"); err == nil {
			if rows, err := adb.Query(`SELECT tg_chat_id, direction FROM pair_direction`); err == nil {
				for rows.Next() {
					var t int64
					var d string
					if rows.Scan(&t, &d) == nil {
						dirs[t] = d
					}
				}
				rows.Close()
			}
			adb.Close()
		}
	}

	// статус антиспама по TG-стороне каждой группы + права бота (если антиспам включён)
	for i := range out {
		if !out[i].Standalone {
			if d := dirs[out[i].TgChatID]; d != "" {
				out[i].Direction = d
			} else {
				out[i].Direction = "both"
			}
		}
		out[i].Antispam, out[i].AntispamMode = antispamStatus("tg", out[i].TgChatID)
		pol := antispamPolicy("tg", out[i].TgChatID)
		out[i].AllowLinks, out[i].StrikeLimit, out[i].BanAfter, out[i].Action, out[i].MuteMinutes, out[i].Warn, out[i].Notify, out[i].Captcha, out[i].Antiraid, out[i].ProfileGuard, out[i].BlockWords, out[i].BlockCats, out[i].DelService = pol.AllowLinks, pol.StrikeLimit, pol.BanAfter, pol.Action, pol.MuteMinutes, pol.Warn, pol.Notify, pol.Captcha, pol.Antiraid, pol.ProfileGuard, pol.BlockWords, pol.BlockCats, pol.DelService
		out[i].Tone = pol.Tone
		out[i].WelcomeText = readGroupWelcome(out[i].TgChatID)
		out[i].Rules = readAntispamRules("tg", out[i].TgChatID)
		if out[i].Antispam {
			out[i].BotAdmin = tgBotIsAdmin(out[i].TgChatID)
		}
	}
	return out
}

// ownsGroup — права на управление группой: записанный владелец (tg_owner_id) или,
// для старых связок, подтверждённое админство в TG-группе.
func ownsGroup(userID, tgChatID int64) bool {
	if tgChatID >= 0 {
		return false
	}
	if bridgeDBPath != "" {
		if db, err := sql.Open("sqlite", "file:"+bridgeDBPath+"?mode=ro&_pragma=busy_timeout(3000)"); err == nil {
			var owner int64
			db.QueryRow(`SELECT COALESCE(tg_owner_id,0) FROM pairs WHERE tg_chat_id=?`, tgChatID).Scan(&owner)
			db.Close()
			if owner != 0 {
				return owner == userID
			}
		}
	}
	return tgIsChatAdmin(tgChatID, userID)
}

// handleSetGroupPrefix — вкл/выкл префикс [TG]/[MAX] для bridge-группы.
func (s *server) handleSetGroupPrefix(w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	if !u.Valid {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	var in struct {
		TgChatID int64 `json:"tg_chat_id"`
		Enabled  bool  `json:"enabled"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.TgChatID == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "tg_chat_id required"})
		return
	}
	if !ownsGroup(u.ID, in.TgChatID) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "вы не админ этой группы"})
		return
	}
	db, err := openRW(bridgeDBPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "db"})
		return
	}
	defer db.Close()
	v := 0
	if in.Enabled {
		v = 1
	}
	if _, err := db.Exec(`UPDATE pairs SET prefix=? WHERE tg_chat_id=?`, v, in.TgChatID); err != nil {
		log.Printf("group prefix err uid=%d tg=%d: %v", u.ID, in.TgChatID, err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "save failed"})
		return
	}
	log.Printf("group prefix uid=%d tg=%d on=%v", u.ID, in.TgChatID, in.Enabled)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleSetGroupPaused — пауза/возобновление связки группы (админ). На паузе сообщения не
// зеркалятся в обе стороны, связка цела. Колонка paused — общая с ядром бриджа.
func (s *server) handleSetGroupPaused(w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	if !u.Valid {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	var in struct {
		TgChatID int64 `json:"tg_chat_id"`
		Paused   bool  `json:"paused"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.TgChatID == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "tg_chat_id required"})
		return
	}
	if !ownsGroup(u.ID, in.TgChatID) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "вы не админ этой группы"})
		return
	}
	db, err := openRW(bridgeDBPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "db"})
		return
	}
	defer db.Close()
	v := 0
	if in.Paused {
		v = 1
	}
	if _, err := db.Exec(`UPDATE pairs SET paused=? WHERE tg_chat_id=?`, v, in.TgChatID); err != nil {
		log.Printf("group pause err uid=%d tg=%d: %v", u.ID, in.TgChatID, err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "save failed"})
		return
	}
	log.Printf("group pause uid=%d tg=%d paused=%v", u.ID, in.TgChatID, in.Paused)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// normalizeGroupDirection — допустимые значения направления bridge; "" при неверном.
func normalizeGroupDirection(d string) string {
	switch d {
	case "tg>max", "max>tg", "both":
		return d
	default:
		return ""
	}
}

// handleSetGroupDirection — направление пересылки моста группы (both|tg>max|max>tg).
// PRO-функция (как /bridge direction в боте). Хранится в addon.db (pair_direction),
// поэтому core-схема pairs остаётся публичной/бесплатной.
func (s *server) handleSetGroupDirection(w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	if !u.Valid {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	var in struct {
		TgChatID  int64  `json:"tg_chat_id"`
		Direction string `json:"direction"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.TgChatID == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "tg_chat_id required"})
		return
	}
	dir := normalizeGroupDirection(strings.TrimSpace(in.Direction))
	if dir == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "direction must be both|tg>max|max>tg"})
		return
	}
	if !ownsGroup(u.ID, in.TgChatID) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "вы не админ этой группы"})
		return
	}
	if !s.userIsPro(u.ID) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "Направление пересылки — PRO-функция"})
		return
	}
	// max_chat_id связки резолвим на сервере (не доверяем клиенту): pair_direction PK — (tg,max).
	var maxChatID int64
	if bridgeDBPath != "" {
		if rdb, err := sql.Open("sqlite", "file:"+bridgeDBPath+"?mode=ro&_pragma=busy_timeout(3000)"); err == nil {
			rdb.QueryRow(`SELECT max_chat_id FROM pairs WHERE tg_chat_id=?`, in.TgChatID).Scan(&maxChatID)
			rdb.Close()
		}
	}
	if maxChatID == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "у группы нет моста"})
		return
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
	if _, err := db.Exec(`INSERT INTO pair_direction (tg_chat_id, max_chat_id, direction, updated_by, updated_at)
			VALUES (?, ?, ?, ?, strftime('%s','now'))
		ON CONFLICT(tg_chat_id, max_chat_id) DO UPDATE SET
			direction = excluded.direction,
			updated_by = excluded.updated_by,
			updated_at = excluded.updated_at`,
		in.TgChatID, maxChatID, dir, u.ID); err != nil {
		log.Printf("group direction err uid=%d tg=%d: %v", u.ID, in.TgChatID, err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "save failed"})
		return
	}
	log.Printf("group direction uid=%d tg=%d max=%d dir=%s", u.ID, in.TgChatID, maxChatID, dir)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleUnbridgeGroup — разорвать связку группы (удалить пару).
func (s *server) handleUnbridgeGroup(w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	if !u.Valid {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	var in struct {
		TgChatID int64 `json:"tg_chat_id"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.TgChatID == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "tg_chat_id required"})
		return
	}
	if !ownsGroup(u.ID, in.TgChatID) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "вы не админ этой группы"})
		return
	}
	db, err := openRW(bridgeDBPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "db"})
		return
	}
	defer db.Close()
	if _, err := db.Exec(`DELETE FROM pairs WHERE tg_chat_id=?`, in.TgChatID); err != nil {
		log.Printf("group unbridge err uid=%d tg=%d: %v", u.ID, in.TgChatID, err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "unbridge failed"})
		return
	}
	log.Printf("group unbridge uid=%d tg=%d", u.ID, in.TgChatID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
