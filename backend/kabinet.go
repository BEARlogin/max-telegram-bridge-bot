package main

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Редактирование из кабинета мини-аппа: докупка постов, удаление связки, комментарии.
// Всё под авторизацией initData (X-Init-Data). Пишем напрямую в bridge.db/addon.db
// (тот же сервер, WAL) — у бриджа нет кеша кросспостов, правки подхватываются сразу.

func openRW(path string) (*sql.DB, error) {
	return sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
}

// userIsPro — PRO по группе TG или активной подписке.
func (s *server) userIsPro(uid int64) bool {
	// Учитываем привязку MAX↔TG: PRO засчитывается по любой стороне связки.
	ids := []int64{uid}
	if tg := s.store.LinkedTg(uid); tg != 0 {
		ids = append(ids, tg)
	}
	if mx := s.store.LinkedMax(uid); mx != 0 {
		ids = append(ids, mx)
	}
	for _, id := range ids {
		if isProTG(id) {
			return true
		}
		if s.billing != nil && s.billing.IsActive(id) {
			return true
		}
	}
	return false
}

// ownsCrosspost — принадлежит ли связка (по max_chat_id) пользователю.
func ownsCrosspost(uid, maxChatID int64) bool {
	if bridgeDBPath == "" {
		return false
	}
	db, err := sql.Open("sqlite", "file:"+bridgeDBPath+"?mode=ro&_pragma=busy_timeout(3000)")
	if err != nil {
		return false
	}
	defer db.Close()
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM crossposts WHERE max_chat_id=? AND deleted_at=0 AND (owner_id=? OR tg_owner_id=?)`,
		maxChatID, uid, uid).Scan(&n)
	return n > 0
}

// crosspostWithinFreeLimit — связка в пределах бесплатного лимита (1 канал) у владельца
// uid: её «возрастной ранг» среди связок владельца < лимита. Для бесплатных комментов.
func crosspostWithinFreeLimit(uid, maxChatID int64) bool {
	if bridgeDBPath == "" {
		return false
	}
	db, err := sql.Open("sqlite", "file:"+bridgeDBPath+"?mode=ro&_pragma=busy_timeout(3000)")
	if err != nil {
		return false
	}
	defer db.Close()
	var rank int
	db.QueryRow(`SELECT COUNT(*) FROM crossposts WHERE deleted_at=0 AND (owner_id=? OR tg_owner_id=?)
		AND created_at < (SELECT created_at FROM crossposts WHERE max_chat_id=? AND deleted_at=0 LIMIT 1)`,
		uid, uid, maxChatID).Scan(&rank)
	return rank < 1 // freeCrosspostLimit
}

// handleBuyPosts — докупка пакета постов из кабинета (авторизация по initData,
// без подписи — личность уже подтверждена). Возвращает URL оплаты T-Bank.
func (s *server) handleBuyPosts(w http.ResponseWriter, r *http.Request) {
	if s.billing == nil || !s.billing.Enabled() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "billing disabled"})
		return
	}
	u := authUser(r)
	if !u.Valid {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	url, pid, err := s.billing.PayPosts(u.ID, postsAmount(), postsPerPurchase(), suffix)
	if err != nil {
		log.Printf("kabinet buy posts failed uid=%d: %v", u.ID, err)
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	log.Printf("kabinet buy posts uid=%d payment_id=%s", u.ID, pid)
	writeJSON(w, http.StatusOK, map[string]any{"url": url})
}

// handleDeleteCrosspost — удалить связку кросспоста (только владелец). Помечает
// deleted_at в bridge.db; бридж перестаёт зеркалить сразу (нет кеша).
func (s *server) handleDeleteCrosspost(w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	if !u.Valid {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	var in struct {
		MaxChatID int64 `json:"max_chat_id"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.MaxChatID == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "max_chat_id required"})
		return
	}
	if bridgeDBPath == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "no bridge db"})
		return
	}
	db, err := openRW(bridgeDBPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "db"})
		return
	}
	defer db.Close()
	res, err := db.Exec(`UPDATE crossposts SET deleted_at=?, deleted_by=?
		WHERE max_chat_id=? AND deleted_at=0 AND (owner_id=? OR tg_owner_id=?)`,
		time.Now().Unix(), u.ID, in.MaxChatID, u.ID, u.ID)
	if err != nil {
		log.Printf("kabinet delete crosspost err uid=%d max=%d: %v", u.ID, in.MaxChatID, err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "delete failed"})
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "не ваша связка или уже удалена"})
		return
	}
	log.Printf("kabinet crosspost deleted uid=%d max=%d", u.ID, in.MaxChatID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleSetReplacements — сохранить замены (правила text-replace) для связки.
// Только владелец. Пишет JSON в bridge.db (формат совпадает с bridge.Replacement).
func (s *server) handleSetReplacements(w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	if !u.Valid {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	var in struct {
		MaxChatID    int64   `json:"max_chat_id"`
		Replacements replSet `json:"replacements"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.MaxChatID == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "max_chat_id required"})
		return
	}
	if !ownsCrosspost(u.ID, in.MaxChatID) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "не ваша связка"})
		return
	}
	// Санитайз: ограничиваем число правил и длину (защита от мусора).
	// Регулярки валидируем — невалидный паттерн на проде молча ломал бы замену.
	var badRe string
	clean := func(in []replRule) []replRule {
		out := make([]replRule, 0, len(in))
		for _, rr := range in {
			rr.From = strings.TrimSpace(rr.From)
			rr.To = strings.TrimSpace(rr.To)
			if rr.From == "" || len([]rune(rr.From)) > 200 || len([]rune(rr.To)) > 200 {
				continue
			}
			if rr.Regex {
				if _, err := regexp.Compile(rr.From); err != nil {
					if badRe == "" {
						badRe = rr.From
					}
					continue
				}
			}
			out = append(out, rr)
			if len(out) >= 50 {
				break
			}
		}
		return out
	}
	in.Replacements.TgToMax = clean(in.Replacements.TgToMax)
	in.Replacements.MaxToTg = clean(in.Replacements.MaxToTg)
	if badRe != "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "неверная регулярка: " + badRe})
		return
	}

	raw, _ := json.Marshal(in.Replacements)
	if bridgeDBPath == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "no bridge db"})
		return
	}
	db, err := openRW(bridgeDBPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "db"})
		return
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE crossposts SET replacements=? WHERE max_chat_id=? AND deleted_at=0`, string(raw), in.MaxChatID); err != nil {
		log.Printf("kabinet set replacements err uid=%d max=%d: %v", u.ID, in.MaxChatID, err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "save failed"})
		return
	}
	log.Printf("kabinet replacements uid=%d max=%d tg>max=%d max>tg=%d", u.ID, in.MaxChatID, len(in.Replacements.TgToMax), len(in.Replacements.MaxToTg))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "replacements": in.Replacements})
}

// handleSyncEdits — вкл/выкл синхронизацию правок для связки (владелец).
func (s *server) handleSyncEdits(w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	if !u.Valid {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	var in struct {
		MaxChatID int64 `json:"max_chat_id"`
		Enabled   bool  `json:"enabled"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.MaxChatID == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "max_chat_id required"})
		return
	}
	if !ownsCrosspost(u.ID, in.MaxChatID) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "не ваша связка"})
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
	if _, err := db.Exec(`UPDATE crossposts SET sync_edits=? WHERE max_chat_id=? AND deleted_at=0`, v, in.MaxChatID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "save failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": in.Enabled})
}

// handleSetPaused — пауза/возобновление связки кросспоста (владелец). На паузе посты не
// пересылаются, связка не удаляется. Колонка paused — общая с ядром бриджа.
func (s *server) handleSetPaused(w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	if !u.Valid {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	var in struct {
		MaxChatID int64 `json:"max_chat_id"`
		Paused    bool  `json:"paused"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.MaxChatID == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "max_chat_id required"})
		return
	}
	if !ownsCrosspost(u.ID, in.MaxChatID) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "не ваша связка"})
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
	if _, err := db.Exec(`UPDATE crossposts SET paused=? WHERE max_chat_id=? AND deleted_at=0`, v, in.MaxChatID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "save failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "paused": in.Paused})
}

// handleSetComments — вкл/выкл комментарии под постами канала (PRO, только владелец).
func (s *server) handleSetComments(w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	if !u.Valid {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	var in struct {
		MaxChatID int64 `json:"max_chat_id"`
		Enabled   bool  `json:"enabled"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.MaxChatID == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "max_chat_id required"})
		return
	}
	if !ownsCrosspost(u.ID, in.MaxChatID) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "не ваша связка"})
		return
	}
	// Комменты доступны на PRO ИЛИ бесплатно на канале в пределах лимита (1 канал).
	if !s.userIsPro(u.ID) && !crosspostWithinFreeLimit(u.ID, in.MaxChatID) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "Бесплатно комментарии доступны на 1 канале. Для остальных — PRO."})
		return
	}
	if addonDBPath == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "no addon db"})
		return
	}
	db, err := openRW(addonDBPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "db"})
		return
	}
	defer db.Close()
	v := 0
	if in.Enabled {
		v = 1
	}
	_, err = db.Exec(`INSERT INTO channel_comments (max_chat_id, enabled, updated_at)
		VALUES (?, ?, strftime('%s','now'))
		ON CONFLICT(max_chat_id) DO UPDATE SET enabled=excluded.enabled, updated_at=excluded.updated_at`,
		in.MaxChatID, v)
	if err != nil {
		log.Printf("kabinet set comments err uid=%d max=%d: %v", u.ID, in.MaxChatID, err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "save failed"})
		return
	}
	log.Printf("kabinet comments uid=%d max=%d enabled=%v", u.ID, in.MaxChatID, in.Enabled)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": in.Enabled})
}
