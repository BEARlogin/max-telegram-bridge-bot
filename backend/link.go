package main

import (
	"crypto/rand"
	"encoding/json"
	"log"
	"net/http"
)

// Привязка MAX-аккаунта к TG-аккаунту по ОДНОРАЗОВОМУ коду (не по id — id перебираемы).
// Баланс постов (импорт) и часть PRO-данных хранятся по TG-id; кабинет, открытый из MAX,
// без связки их не видит. Поток: MAX-кабинет генерит код → юзер шлёт TG-боту /link <код> →
// бридж дёргает /api/internal/link → max_id привязывается к tg_id отправителя.

const linkCodeTTL = 600 // сек, 10 минут на ввод кода

// genLinkCode — короткий человекочитаемый код из безопасного алфавита (без 0/O/1/I).
func genLinkCode() string {
	const alpha = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "ABCDEF"
	}
	for i := range b {
		b[i] = alpha[int(b[i])%len(alpha)]
	}
	return string(b)
}

// effectiveTgID — для MAX-юзера возвращает привязанный TG-id (или 0). Для TG-юзера — его же id.
func (s *server) effectiveTgID(u user) int64 {
	if u.Platform == "tg" {
		return u.ID
	}
	if u.Platform == "max" {
		return s.store.LinkedTg(u.ID)
	}
	return 0
}

// handleLinkStart — MAX-кабинет: выдать одноразовый код для привязки TG.
func (s *server) handleLinkStart(w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	if !u.Valid {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	code := genLinkCode()
	s.store.LinkNewCode(u.ID, code)
	log.Printf("link code issued max=%d", u.ID)
	writeJSON(w, http.StatusOK, map[string]any{"code": code, "ttl_min": linkCodeTTL / 60})
}

// handleInternalLinkStart — бридж зовёт по команде /link в личке MAX-бота: выдать
// одноразовый код привязки для max_id (тот же флоу, что кнопка в кабинете).
func (s *server) handleInternalLinkStart(w http.ResponseWriter, r *http.Request) {
	secret := commentSyncSecret()
	var in struct {
		MaxID  int64  `json:"max_id"`
		Secret string `json:"secret"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if secret == "" || in.Secret != secret || in.MaxID == 0 {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "forbidden"})
		return
	}
	code := genLinkCode()
	s.store.LinkNewCode(in.MaxID, code)
	log.Printf("link code issued via bot max=%d", in.MaxID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "code": code, "ttl_min": linkCodeTTL / 60})
}

// handleAutoLink — бридж зовёт при создании bridge-связки с известными владельцами обеих
// сторон: автопривязка MAX↔TG без кода. Линкуем ТОЛЬКО не привязанные ранее аккаунты.
func (s *server) handleAutoLink(w http.ResponseWriter, r *http.Request) {
	secret := commentSyncSecret()
	var in struct {
		MaxID  int64  `json:"max_id"`
		TgID   int64  `json:"tg_id"`
		Secret string `json:"secret"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if secret == "" || in.Secret != secret || in.MaxID == 0 || in.TgID == 0 {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "forbidden"})
		return
	}
	ok := s.store.AutoLink(in.MaxID, in.TgID)
	if ok {
		log.Printf("auto-link max=%d tg=%d (bridge pairing)", in.MaxID, in.TgID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": ok})
}

// handleLinkComplete — бридж зовёт сюда, когда TG-юзер прислал /link <код>.
func (s *server) handleLinkComplete(w http.ResponseWriter, r *http.Request) {
	secret := commentSyncSecret()
	var in struct {
		Code   string `json:"code"`
		TgID   int64  `json:"tg_id"`
		Secret string `json:"secret"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if secret == "" || in.Secret != secret || in.TgID == 0 || in.Code == "" {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "forbidden"})
		return
	}
	maxID, ok := s.store.LinkRedeem(in.Code, in.TgID, linkCodeTTL)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "код неверный или истёк"})
		return
	}
	log.Printf("link complete max=%d tg=%d", maxID, in.TgID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
