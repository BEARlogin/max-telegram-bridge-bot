package main

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
)

// mirrors.go — зеркала (MAX→MAX и TG→TG) и слоты тарифа в кабинете.
// Читаем addon.db (max_mirror/tg_mirror/max_mirror_src/tg_mirror_src) и bridge.db (bot_chats,
// pairs, crossposts — для счётчика слотов); названия чатов — из bot_chats.
// Слоты: база 5 + докупленные (billing.MirrorSlots); занято = мосты + зеркала + каналы.

const slotsBase = 5 // держать в синхроне с mirrorBaseLimit аддона бриджа

type mirrorLink struct {
	Platform string `json:"platform"` // "max" | "tg"
	SrcChat  int64  `json:"src_chat"`
	DstChat  int64  `json:"dst_chat"`
	SrcTitle string `json:"src_title"`
	DstTitle string `json:"dst_title"`
	Owned    bool   `json:"owned"` // связка принадлежит юзеру (можно удалить)
}

// userIDs — все id юзера: свой + привязанный (MAX↔TG), для владения связками на обеих платформах.
func (s *server) userIDs(u user) []int64 {
	ids := []int64{u.ID}
	if u.Platform == "max" {
		if tg := s.store.LinkedTg(u.ID); tg != 0 {
			ids = append(ids, tg)
		}
	} else if u.Platform == "tg" {
		if mx := s.store.LinkedMax(u.ID); mx != 0 {
			ids = append(ids, mx)
		}
	}
	return ids
}

// listMirrors — зеркала юзера: связки, где он владелец приёмника ИЛИ владелец донора.
func (s *server) listMirrors(u user) []mirrorLink {
	if addonDBPath == "" {
		return nil
	}
	db, err := sql.Open("sqlite", "file:"+addonDBPath+"?mode=ro&_pragma=busy_timeout(3000)")
	if err != nil {
		return nil
	}
	defer db.Close()

	ids := s.userIDs(u)
	in, args := sqlIn(ids)
	out := []mirrorLink{}
	for _, tbl := range []struct{ platform, mirror, src string }{
		{"max", "max_mirror", "max_mirror_src"},
		{"tg", "tg_mirror", "tg_mirror_src"},
	} {
		q := `SELECT m.src_chat, m.dst_chat,
			CASE WHEN m.owner_id IN (` + in + `) THEN 1 ELSE 0 END AS owned
			FROM ` + tbl.mirror + ` m
			LEFT JOIN ` + tbl.src + ` s ON s.chat_id = m.src_chat
			WHERE m.owner_id IN (` + in + `) OR s.owner_id IN (` + in + `)`
		rows, err := db.Query(q, append(append(append([]any{}, args...), args...), args...)...)
		if err != nil {
			continue
		}
		for rows.Next() {
			var l mirrorLink
			var owned int
			if rows.Scan(&l.SrcChat, &l.DstChat, &owned) != nil {
				continue
			}
			l.Platform = tbl.platform
			l.Owned = owned == 1
			out = append(out, l)
		}
		rows.Close()
	}

	// Названия чатов — из bot_chats бриджа.
	if bridgeDBPath != "" && len(out) > 0 {
		if bdb, err := sql.Open("sqlite", "file:"+bridgeDBPath+"?mode=ro&_pragma=busy_timeout(3000)"); err == nil {
			defer bdb.Close()
			title := func(platform string, chat int64) string {
				var t string
				_ = bdb.QueryRow(`SELECT title FROM bot_chats WHERE platform=? AND chat_id=?`, platform, chat).Scan(&t)
				return t
			}
			for i := range out {
				out[i].SrcTitle = title(out[i].Platform, out[i].SrcChat)
				out[i].DstTitle = title(out[i].Platform, out[i].DstChat)
			}
		}
	}
	return out
}

// slotsInfo — слоты тарифа юзера: занято (мосты + зеркала + каналы) / лимит (база + докупка).
func (s *server) slotsInfo(u user) map[string]any {
	ids := s.userIDs(u)
	used := 0

	if addonDBPath != "" {
		if db, err := sql.Open("sqlite", "file:"+addonDBPath+"?mode=ro&_pragma=busy_timeout(3000)"); err == nil {
			in, args := sqlIn(ids)
			var n int
			if db.QueryRow(`SELECT (SELECT COUNT(*) FROM max_mirror WHERE owner_id IN (`+in+`)) + (SELECT COUNT(*) FROM tg_mirror WHERE owner_id IN (`+in+`))`,
				append(append([]any{}, args...), args...)...).Scan(&n) == nil {
				used += n
			}
			db.Close()
		}
	}
	if bridgeDBPath != "" {
		if db, err := sql.Open("sqlite", "file:"+bridgeDBPath+"?mode=ro&_pragma=busy_timeout(3000)"); err == nil {
			in, args := sqlIn(ids)
			var n int
			if db.QueryRow(`SELECT (SELECT COUNT(*) FROM pairs WHERE tg_owner_id IN (`+in+`) OR max_owner_id IN (`+in+`)) +
				(SELECT COUNT(*) FROM crossposts WHERE deleted_at=0 AND (owner_id IN (`+in+`) OR tg_owner_id IN (`+in+`)))`,
				append(append(append(append([]any{}, args...), args...), args...), args...)...).Scan(&n) == nil {
				used += n
			}
			db.Close()
		}
	}

	extra := 0
	if s.billing != nil {
		extra = s.billing.MirrorSlots(s.billing.BillingID(u.ID))
	}
	return map[string]any{
		"used":  used,
		"base":  slotsBase,
		"extra": extra,
		"limit": slotsBase + extra,
	}
}

// handleDeleteMirror — удалить зеркальную связку (владелец связки или владелец донора).
func (s *server) handleDeleteMirror(w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	if !u.Valid {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	var in struct {
		Platform string `json:"platform"`
		SrcChat  int64  `json:"src_chat"`
		DstChat  int64  `json:"dst_chat"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || (in.Platform != "max" && in.Platform != "tg") || in.SrcChat == 0 || in.DstChat == 0 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if addonDBPath == "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "недоступно"})
		return
	}
	db, err := openRW(addonDBPath)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "db"})
		return
	}
	defer db.Close()

	tbl, srcTbl := "max_mirror", "max_mirror_src"
	if in.Platform == "tg" {
		tbl, srcTbl = "tg_mirror", "tg_mirror_src"
	}
	ids := s.userIDs(u)
	inClause, args := sqlIn(ids)
	// Право: владелец связки ИЛИ владелец донора.
	var one int
	q := `SELECT 1 FROM ` + tbl + ` m LEFT JOIN ` + srcTbl + ` s ON s.chat_id = m.src_chat
		WHERE m.src_chat=? AND m.dst_chat=? AND (m.owner_id IN (` + inClause + `) OR s.owner_id IN (` + inClause + `))`
	qa := append([]any{in.SrcChat, in.DstChat}, append(append([]any{}, args...), args...)...)
	if db.QueryRow(q, qa...).Scan(&one) != nil {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "не ваша связка"})
		return
	}
	if _, err := db.Exec(`DELETE FROM `+tbl+` WHERE src_chat=? AND dst_chat=?`, in.SrcChat, in.DstChat); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "delete failed"})
		return
	}
	log.Printf("mirror deleted via kabinet user=%d platform=%s %d→%d", u.ID, in.Platform, in.SrcChat, in.DstChat)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handlePreviewSlots — расчёт покупки слотов (промежуточный экран): прорейт-сумма за
// остаток периода, конец периода, цена слота и будущий рекуррент. Платёж НЕ создаётся.
func (s *server) handlePreviewSlots(w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	if !u.Valid {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	var in struct {
		Groups int `json:"groups"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.Groups <= 0 || in.Groups > 50 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if s.billing == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "биллинг недоступен"})
		return
	}
	bid := s.billing.BillingID(u.ID)
	amount, paidUntil, err := s.billing.PreviewMirrorSlots(bid, in.Groups)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "нужна активная подписка PRO"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                     true,
		"amount_kopecks":         amount,                 // разовый прорейт-платёж сейчас
		"paid_until":             paidUntil,              // конец оплаченного периода (unix)
		"slot_price_kopecks":     s.billing.SlotPrice(),  // цена слота за полный период
		"next_recurrent_kopecks": s.billing.EffectiveAmount(bid) + uint64(in.Groups)*s.billing.SlotPrice(), // рекуррент после покупки
	})
}

// handleBuySlots — докупка слотов из кабинета: возвращает платёжную ссылку T-Bank.
func (s *server) handleBuySlots(w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	if !u.Valid {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	var in struct {
		Groups int `json:"groups"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.Groups <= 0 || in.Groups > 50 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if s.billing == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "биллинг недоступен"})
		return
	}
	url, amount, err := s.billing.BuyMirrorSlots(r.Context(), s.billing.BillingID(u.ID), in.Groups)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "pay_url": url, "amount_kopecks": amount})
}

// handleReduceSlots — уменьшение доп-слотов из кабинета. Без возврата за текущий период:
// рекуррент снизится со следующего продления (EffectiveAmount пересчитается).
func (s *server) handleReduceSlots(w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	if !u.Valid {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	var in struct {
		Groups int `json:"groups"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.Groups <= 0 || in.Groups > 50 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if s.billing == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "биллинг недоступен"})
		return
	}
	bid := s.billing.BillingID(u.ID)
	next, err := s.billing.ReduceMirrorSlots(bid, in.Groups)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "не удалось уменьшить"})
		return
	}
	log.Printf("slots reduced via kabinet user=%d by=%d now=%d", u.ID, in.Groups, next)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "slots": next})
}

// sqlIn — плейсхолдеры и аргументы для IN (?,?,…).
func sqlIn(ids []int64) (string, []any) {
	ph := ""
	args := make([]any, 0, len(ids))
	for i, id := range ids {
		if i > 0 {
			ph += ","
		}
		ph += "?"
		args = append(args, id)
	}
	return ph, args
}
