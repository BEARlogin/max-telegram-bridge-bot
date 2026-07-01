package billing

import (
	"bytes"
	"strconv"
	"strings"
	"time"

	"github.com/nikita-vanyasin/tinkoff"
)

// ClassifyNotification разбирает нотификацию и определяет, относится ли она к покупке
// постов (OrderId "posts-<uid>-…"). ok=false — нотификация не от этого терминала
// (подпись/ключ не совпали) → вызывающий пусть пробует другой клиент.
func (s *Service) ClassifyNotification(body []byte) (isPosts bool, uid int64, paymentID string, confirmed, ok bool) {
	n, err := s.Parse(body)
	if err != nil {
		return false, 0, "", false, false
	}
	ok = true
	paymentID = strconv.FormatUint(n.PaymentID, 10)
	confirmed = n.Status == tinkoff.StatusConfirmed
	if strings.HasPrefix(n.OrderID, "posts-") {
		isPosts = true
		if parts := strings.Split(n.OrderID, "-"); len(parts) >= 2 {
			uid, _ = strconv.ParseInt(parts[1], 10, 64)
		}
	}
	return
}

// renewLeadDays — за сколько дней до конца оплаченного периода списываем продление.
// Даёт окно на ретрай при отказе карты до фактического истечения PRO.
const renewLeadDays = 3

// HandleNotification разбирает нотификацию T-Bank, проверяет подпись и при успешной
// оплате активирует/продлевает подписку. Возвращает строку-ответ для T-Bank.
func (s *Service) HandleNotification(body []byte) (string, error) {
	n, err := s.cli.ParseNotification(bytes.NewBuffer(body))
	if err != nil {
		return "", err
	}
	pid := strconv.FormatUint(n.PaymentID, 10)
	switch n.Status {
	case tinkoff.StatusConfirmed, tinkoff.StatusAuthorized:
		// Идемпотентность по истории платежей: один платёж шлёт AUTHORIZED и CONFIRMED
		// (+ретраи) с ОДНИМ payment_id. recordPayment вернёт false, если pid уже учтён —
		// тогда повторно не продлеваем. Раньше сверка шла по payment_id в subscriptions,
		// но Init писал его заранее → notify считал оплату дублем и НЕ активировал.
		// RebillID (string) — deprecated и всегда пустой; реальный id в RebillIDUInt64.
		rebill := ""
		if n.RebillIDUInt64 != 0 {
			rebill = strconv.FormatUint(n.RebillIDUInt64, 10)
		}
		if !s.recordPayment(pid, n.OrderID, string(n.Status), rebill, "sub", n.Amount) {
			return s.cli.GetNotificationSuccessResponse(), nil
		}
		periodSec := int64(s.cfg.PeriodDays) * 86400
		leadSec := int64(renewLeadDays) * 86400
		// Продлеваем от MAX(paid_until, now) — не теряем остаток дней; следующее списание
		// (next_charge) — за renewLeadDays до конца (окно на ретрай при отказе карты).
		// rebill_id и карту (PAN) сохраняем — для автопродления и возобновления без оплаты.
		if rebill != "" {
			s.db.Exec(`UPDATE subscriptions SET status='active', rebill_id=?, payment_id=?, card_pan=?,
				paid_until = MAX(COALESCE(paid_until,0), strftime('%s','now')) + ?,
				next_charge = MAX(COALESCE(paid_until,0), strftime('%s','now')) + ?,
				updated_at = strftime('%s','now') WHERE order_id=?`,
				rebill, pid, n.PAN, periodSec, periodSec-leadSec, n.OrderID)
		} else {
			s.db.Exec(`UPDATE subscriptions SET status='active', payment_id=?, card_pan=?,
				paid_until = MAX(COALESCE(paid_until,0), strftime('%s','now')) + ?,
				next_charge = MAX(COALESCE(paid_until,0), strftime('%s','now')) + ?,
				updated_at = strftime('%s','now') WHERE order_id=?`,
				pid, n.PAN, periodSec, periodSec-leadSec, n.OrderID)
		}
	case tinkoff.StatusRejected:
		s.db.Exec(`UPDATE subscriptions SET status='past_due', updated_at=? WHERE order_id=?`,
			time.Now().Unix(), n.OrderID)
	}
	return s.cli.GetNotificationSuccessResponse(), nil
}

// IsActive — есть ли у юзера действующий PRO по подписке. Отменённая подписка
// (canceled) остаётся действующей до конца оплаченного периода — списаний больше нет.
func (s *Service) IsActive(userID int64) bool {
	status, until := s.SubStatus(userID)
	return (status == "active" || status == "canceled" || status == "trial") && until > time.Now().Unix()
}

// SubStatus — статус подписки (active|canceled|past_due|pending|"") и paid_until.
func (s *Service) SubStatus(userID int64) (string, int64) {
	var status string
	var until int64
	if s.db.QueryRow(`SELECT status, paid_until FROM subscriptions WHERE user_id=?`, userID).Scan(&status, &until) != nil {
		return "", 0
	}
	return status, until
}

// Cancel отменяет подписку: больше не списываем (next_charge=0), но PRO действует
// до конца оплаченного периода (paid_until).
func (s *Service) Cancel(userID int64) error {
	_, err := s.db.Exec(`UPDATE subscriptions SET status='canceled', next_charge=0, updated_at=? WHERE user_id=?`,
		time.Now().Unix(), userID)
	return err
}
