package main

import (
	"io"
	"log"
	"net/http"
)

// handleSubscribe — оформить PRO: создаёт платёж T-Bank с рекуррентом, возвращает URL оплаты.
func (s *server) handleSubscribe(w http.ResponseWriter, r *http.Request) {
	if s.billing == nil || !s.billing.Enabled() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "billing disabled"})
		return
	}
	u := authUser(r)
	if !u.Valid {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	url, err := s.billing.Subscribe(r.Context(), u.ID, u.Name)
	if err != nil {
		log.Printf("billing subscribe failed user=%d: %v", u.ID, err)
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	log.Printf("billing subscribe ok user=%d url=%s", u.ID, url)
	writeJSON(w, http.StatusOK, map[string]any{"url": url})
}

// handleCancelSub — отмена подписки из кабинета (initData). Списаний больше не будет,
// PRO действует до конца оплаченного периода.
func (s *server) handleCancelSub(w http.ResponseWriter, r *http.Request) {
	if s.billing == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "billing disabled"})
		return
	}
	u := authUser(r)
	if !u.Valid {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	if err := s.billing.Cancel(s.billing.BillingID(u.ID)); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "cancel failed"})
		return
	}
	log.Printf("billing cancel user=%d", u.ID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleResume — возобновить подписку. Без формы оплаты, если есть сохранённая карта:
// в пределах периода — мгновенно; после истечения — списание по rebill. need_card ⇒
// карты нет, фронт отправит на полную оплату (subscribe).
func (s *server) handleResume(w http.ResponseWriter, r *http.Request) {
	if s.billing == nil || !s.billing.Enabled() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "billing disabled"})
		return
	}
	u := authUser(r)
	if !u.Valid {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	res, err := s.billing.Resume(r.Context(), s.billing.BillingID(u.ID))
	if err != nil {
		log.Printf("billing resume failed user=%d: %v", u.ID, err)
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	log.Printf("billing resume user=%d result=%s", u.ID, res)
	writeJSON(w, http.StatusOK, map[string]any{"result": res})
}

// handleTrial — выдать бесплатный триал PRO (один раз на юзера, без карты).
func (s *server) handleTrial(w http.ResponseWriter, r *http.Request) {
	if s.billing == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "billing disabled"})
		return
	}
	u := authUser(r)
	if !u.Valid {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	if err := s.billing.StartTrial(u.ID, 7); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	log.Printf("billing trial started user=%d", u.ID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleTbankNotify — нотификации T-Bank (об оплате/списании). Активирует/продлевает подписку.
func (s *server) handleTbankNotify(w http.ResponseWriter, r *http.Request) {
	if s.billing == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	log.Printf("tbank notify: %s", body)
	// Покупка постов импорта (OrderId "posts-…") — начисляем посты, а не подписку.
	if isPosts, uid, pid, confirmed, ok := s.billing.ClassifyNotification(body); ok && isPosts {
		if confirmed {
			s.grantPosts(uid, pid)
		}
		w.Write([]byte(s.billing.NotifyOK()))
		return
	}
	// Иначе — подписка. Нотификация может прийти с боевого или DEMO (cert) терминала —
	// у каждого свой ключ/подпись. Пробуем боевой, при несовпадении ключа — cert.
	resp, err := s.billing.HandleNotification(body)
	if err != nil && s.certBilling != nil {
		resp, err = s.certBilling.HandleNotification(body)
	}
	if err != nil {
		log.Printf("tbank notify handle failed: %v", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	w.Write([]byte(resp))
}

// Cert-эндпоинты (admin/pay, admin/refund) удалены после прохождения сертификации
// T-Bank — они создавали реальные платежи и были постоянным риском на проде.
