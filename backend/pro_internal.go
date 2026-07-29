package main

import (
	"encoding/json"
	"log"
	"net/http"
)

// handleInternalProSubscribe создаёт ссылку первого платежа PRO для кнопки
// непосредственно в Telegram/MAX-боте. Авторизация — общий COMMENT_SYNC_SECRET.
func (s *server) handleInternalProSubscribe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	secret := commentSyncSecret()
	var in struct {
		UserID int64  `json:"user_id"`
		Secret string `json:"secret"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if secret == "" || in.Secret != secret || in.UserID == 0 {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "forbidden"})
		return
	}
	if s.billing == nil || !s.billing.Enabled() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "billing disabled"})
		return
	}

	userID := s.billing.BillingID(in.UserID)
	amount := s.billing.BaseAmount(userID)
	url, err := s.billing.Subscribe(r.Context(), userID, "")
	if err != nil {
		log.Printf("bot PRO subscribe failed user=%d: %v", userID, err)
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": "payment unavailable"})
		return
	}
	log.Printf("bot PRO subscribe link created user=%d amount=%d", userID, amount)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":             true,
		"pay_url":        url,
		"amount_kopecks": amount,
	})
}
