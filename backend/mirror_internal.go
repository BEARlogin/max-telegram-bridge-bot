package main

import (
	"encoding/json"
	"log"
	"net/http"
)

// handleInternalMirrorSlots — bridge дёргает при докупке/уменьшении групп зеркала.
// groups > 0 — докупить (прорейт-списание по сохранённой карте, слоты начислятся по
// нотификации T-Bank); groups < 0 — уменьшить (без возврата, рекуррент упадёт со след. периода).
// Auth — общий секрет COMMENT_SYNC_SECRET (как /api/internal/link).
func (s *server) handleInternalMirrorSlots(w http.ResponseWriter, r *http.Request) {
	secret := commentSyncSecret()
	var in struct {
		UserID int64  `json:"user_id"`
		Groups int    `json:"groups"`
		Secret string `json:"secret"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if secret == "" || in.Secret != secret || in.UserID == 0 || in.Groups == 0 {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "forbidden"})
		return
	}
	if s.billing == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "billing disabled"})
		return
	}
	if in.Groups > 0 {
		url, amount, err := s.billing.BuyMirrorSlots(r.Context(), in.UserID, in.Groups)
		if err != nil {
			log.Printf("mirror-slots buy FAILED user=%d groups=%d: %v", in.UserID, in.Groups, err)
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		log.Printf("mirror-slots buy-link user=%d groups=%d amount=%d", in.UserID, in.Groups, amount)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "pay_url": url, "amount_kopecks": amount})
		return
	}
	next, err := s.billing.ReduceMirrorSlots(in.UserID, -in.Groups)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "reduce failed"})
		return
	}
	log.Printf("mirror-slots reduce user=%d by=%d now=%d", in.UserID, -in.Groups, next)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "slots": next})
}
