package main

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
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

type slotUsageItem struct {
	Kind   string `json:"kind"`
	Label  string `json:"label"`
	Detail string `json:"detail,omitempty"`
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

// slotOwnerIDs повторяет канонизацию groupSlotsUsed из аддона. После привязки
// MAX↔TG владельцем единого счёта всегда считается TG-id; linked нужен только
// Telegram Business-инбоксу, который может хранить владельца MAX-группы.
func (s *server) slotOwnerIDs(u user) (ownerID, linkedID int64) {
	ownerID = u.ID
	switch u.Platform {
	case "max":
		if tgID := s.store.LinkedTg(u.ID); tgID != 0 {
			return tgID, u.ID
		}
	case "tg":
		if maxID := s.store.LinkedMax(u.ID); maxID != 0 {
			linkedID = maxID
		}
	}
	return ownerID, linkedID
}

func slotChatName(db *sql.DB, platform string, chatID int64) string {
	if db != nil {
		var title string
		if db.QueryRow(`SELECT title FROM bot_chats WHERE platform=? AND chat_id=?`, platform, chatID).Scan(&title) == nil && title != "" {
			return title
		}
	}
	if platform == "tg" {
		return "Telegram " + strconv.FormatInt(chatID, 10)
	}
	return "MAX " + strconv.FormatInt(chatID, 10)
}

// slotsInfo — тот же состав слотов, что groupSlotsUsed в аддоне:
// группы + каналы + зеркала + VK + личные контакты + Telegram Business-инбоксы.
// items нужны кабинету, чтобы пользователь видел назначение каждого занятого слота.
func (s *server) slotsInfo(u user) map[string]any {
	ownerID, linkedID := s.slotOwnerIDs(u)
	items := make([]slotUsageItem, 0)
	breakdown := map[string]int{
		"groups": 0, "channels": 0, "mirrors": 0,
		"vk": 0, "direct_messages": 0, "business_inboxes": 0,
	}

	var bridgeDB *sql.DB
	if bridgeDBPath != "" {
		if db, err := sql.Open("sqlite", "file:"+bridgeDBPath+"?mode=ro&_pragma=busy_timeout(3000)"); err == nil {
			bridgeDB = db
			defer bridgeDB.Close()
		}
	}

	if bridgeDB != nil {
		rows, err := bridgeDB.Query(`SELECT p.tg_chat_id,p.max_chat_id,
			COALESCE(t.title,''),COALESCE(m.title,'')
			FROM pairs p
			LEFT JOIN bot_chats t ON t.platform='tg' AND t.chat_id=p.tg_chat_id
			LEFT JOIN bot_chats m ON m.platform='max' AND m.chat_id=p.max_chat_id
			WHERE (p.tg_owner_id=? AND p.tg_owner_id!=0)
				OR (p.max_owner_id=? AND p.max_owner_id!=0)
			ORDER BY p.tg_chat_id,p.max_chat_id`, ownerID, ownerID)
		if err == nil {
			for rows.Next() {
				var tgChatID, maxChatID int64
				var tgTitle, maxTitle string
				if rows.Scan(&tgChatID, &maxChatID, &tgTitle, &maxTitle) != nil {
					continue
				}
				if tgTitle == "" {
					tgTitle = slotChatName(bridgeDB, "tg", tgChatID)
				}
				if maxTitle == "" {
					maxTitle = slotChatName(bridgeDB, "max", maxChatID)
				}
				items = append(items, slotUsageItem{
					Kind: "group", Label: "Группа Telegram ↔ MAX",
					Detail: tgTitle + " ↔ " + maxTitle,
				})
				breakdown["groups"]++
			}
			rows.Close()
		}

		rows, err = bridgeDB.Query(`SELECT c.tg_chat_id,c.max_chat_id,
			COALESCE(t.title,''),COALESCE(m.title,'')
			FROM crossposts c
			LEFT JOIN bot_chats t ON t.platform='tg' AND t.chat_id=c.tg_chat_id
			LEFT JOIN bot_chats m ON m.platform='max' AND m.chat_id=c.max_chat_id
			WHERE c.deleted_at=0
				AND ((c.owner_id=? AND c.owner_id!=0)
					OR (c.tg_owner_id=? AND c.tg_owner_id!=0))
			ORDER BY c.tg_chat_id,c.max_chat_id`, ownerID, ownerID)
		if err == nil {
			for rows.Next() {
				var tgChatID, maxChatID int64
				var tgTitle, maxTitle string
				if rows.Scan(&tgChatID, &maxChatID, &tgTitle, &maxTitle) != nil {
					continue
				}
				if tgTitle == "" {
					tgTitle = slotChatName(bridgeDB, "tg", tgChatID)
				}
				if maxTitle == "" {
					maxTitle = slotChatName(bridgeDB, "max", maxChatID)
				}
				items = append(items, slotUsageItem{
					Kind: "channel", Label: "Канал Telegram ↔ MAX",
					Detail: tgTitle + " ↔ " + maxTitle,
				})
				breakdown["channels"]++
			}
			rows.Close()
		}
	}

	if addonDBPath != "" {
		if db, err := sql.Open("sqlite", "file:"+addonDBPath+"?mode=ro&_pragma=busy_timeout(3000)"); err == nil {
			for _, mirror := range []struct {
				platform string
				table    string
			}{
				{platform: "max", table: "max_mirror"},
				{platform: "tg", table: "tg_mirror"},
			} {
				rows, queryErr := db.Query(`SELECT src_chat,dst_chat FROM `+mirror.table+`
					WHERE owner_id=? ORDER BY src_chat,dst_chat`, ownerID)
				if queryErr != nil {
					continue
				}
				for rows.Next() {
					var srcChatID, dstChatID int64
					if rows.Scan(&srcChatID, &dstChatID) != nil {
						continue
					}
					platformLabel := "MAX"
					if mirror.platform == "tg" {
						platformLabel = "Telegram"
					}
					items = append(items, slotUsageItem{
						Kind: "mirror", Label: "Зеркало " + platformLabel,
						Detail: slotChatName(bridgeDB, mirror.platform, srcChatID) + " → " +
							slotChatName(bridgeDB, mirror.platform, dstChatID),
					})
					breakdown["mirrors"]++
				}
				rows.Close()
			}

			rows, queryErr := db.Query(`SELECT b.source_platform,b.source_chat_id,
				e.kind,e.title,a.community_id
				FROM vk_bindings b
				JOIN vk_endpoints e ON e.id=b.endpoint_id
				JOIN vk_accounts a ON a.id=e.account_id
				WHERE b.owner_id=? ORDER BY b.id`, ownerID)
			if queryErr == nil {
				for rows.Next() {
					var sourcePlatform, endpointKind, endpointTitle string
					var sourceChatID, communityID int64
					if rows.Scan(&sourcePlatform, &sourceChatID, &endpointKind, &endpointTitle, &communityID) != nil {
						continue
					}
					if endpointTitle == "" {
						endpointTitle = "сообщество VK " + strconv.FormatInt(communityID, 10)
						if endpointKind == "chat" {
							endpointTitle = "беседа " + endpointTitle
						}
					}
					items = append(items, slotUsageItem{
						Kind: "vk", Label: "VK-связка",
						Detail: slotChatName(bridgeDB, sourcePlatform, sourceChatID) + " ↔ " + endpointTitle,
					})
					breakdown["vk"]++
				}
				rows.Close()
			}

			rows, queryErr = db.Query(`SELECT a_platform,a_user_id,b_platform,b_user_id
				FROM dm_contacts WHERE owner_id=? ORDER BY id`, ownerID)
			if queryErr == nil {
				for rows.Next() {
					var aPlatform, bPlatform string
					var aUserID, bUserID int64
					if rows.Scan(&aPlatform, &aUserID, &bPlatform, &bUserID) != nil {
						continue
					}
					items = append(items, slotUsageItem{
						Kind: "direct_message", Label: "Один на один",
						Detail: strings.ToUpper(aPlatform) + " " + strconv.FormatInt(aUserID, 10) +
							" ↔ " + strings.ToUpper(bPlatform) + " " + strconv.FormatInt(bUserID, 10),
					})
					breakdown["direct_messages"]++
				}
				rows.Close()
			}

			rows, queryErr = db.Query(`SELECT tg_user_id,max_chat_id
				FROM tg_business_inboxes
				WHERE tg_user_id IN (?,?) OR max_owner_id IN (?,?)
				ORDER BY tg_user_id`, ownerID, linkedID, ownerID, linkedID)
			if queryErr == nil {
				for rows.Next() {
					var tgUserID, maxChatID int64
					if rows.Scan(&tgUserID, &maxChatID) != nil {
						continue
					}
					items = append(items, slotUsageItem{
						Kind: "business_inbox", Label: "Входящие Telegram",
						Detail: "Telegram " + strconv.FormatInt(tgUserID, 10) + " → " +
							slotChatName(bridgeDB, "max", maxChatID),
					})
					breakdown["business_inboxes"]++
				}
				rows.Close()
			}
			db.Close()
		}
	}

	extra := 0
	if s.billing != nil {
		extra = s.billing.MirrorSlots(s.billing.BillingID(u.ID))
	}
	return map[string]any{
		"used":      len(items),
		"base":      slotsBase,
		"extra":     extra,
		"limit":     slotsBase + extra,
		"breakdown": breakdown,
		"items":     items,
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
		"amount_kopecks":         amount,                                                                      // разовый прорейт-платёж сейчас
		"paid_until":             paidUntil,                                                                   // конец оплаченного периода (unix)
		"slot_price_kopecks":     s.billing.SlotPrice(bid),                                                    // цена слота за полный период
		"next_recurrent_kopecks": s.billing.EffectiveAmount(bid) + uint64(in.Groups)*s.billing.SlotPrice(bid), // рекуррент после покупки
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
