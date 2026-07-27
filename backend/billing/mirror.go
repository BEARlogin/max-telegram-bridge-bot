package billing

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/nikita-vanyasin/tinkoff"
)

const (
	legacyBaseAmountKopecks uint64 = 29900
	legacySlotPriceKopecks  uint64 = 4900
	newSlotPriceKopecks     uint64 = 9900
)

// mirrorOrderPrefix — префикс OrderID для покупки слотов зеркала (прорейт-платёж).
const mirrorOrderPrefix = "mslot-"

// billingID возвращает id, под которым лежит АКТИВНАЯ подписка (сам userID или связанный TG↔MAX
// из account_links). Важно выбирать id с активной подпиской, а не любую строку: у MAX-id может
// висеть pending-строка, тогда как реальная подписка — под связанным TG-id. Если активной нет —
// возвращаем сам userID.
func (s *Service) BillingID(userID int64) int64 {
	ids := []int64{userID}
	var linked int64
	if s.db.QueryRow(`SELECT tg_id FROM account_links WHERE max_id=? AND tg_id!=0`, userID).Scan(&linked) == nil && linked != 0 {
		ids = append(ids, linked)
	}
	linked = 0
	if s.db.QueryRow(`SELECT max_id FROM account_links WHERE tg_id=? AND max_id!=0`, userID).Scan(&linked) == nil && linked != 0 {
		ids = append(ids, linked)
	}
	now := time.Now().Unix()
	for _, id := range ids {
		var status string
		var until int64
		if s.db.QueryRow(`SELECT status, paid_until FROM subscriptions WHERE user_id=?`, id).Scan(&status, &until) == nil {
			if (status == "active" || status == "canceled" || status == "trial") && until > now {
				return id
			}
		}
	}
	return userID
}

// MirrorSlots — сколько доп-слотов зеркала докуплено пользователем.
func (s *Service) MirrorSlots(userID int64) int {
	var n int
	_ = s.db.QueryRow(`SELECT mirror_slots FROM subscriptions WHERE user_id=?`, userID).Scan(&n)
	if n < 0 {
		return 0
	}
	return n
}

// EffectiveAmount — сумма рекуррентного списания: база + слоты×цена. По ней продлеваем (A1:
// докупка увеличивает рекуррент, уменьшение — снижает со следующего периода).
func (s *Service) EffectiveAmount(userID int64) uint64 {
	return s.BaseAmount(userID) + uint64(s.MirrorSlots(userID))*s.SlotPrice(userID)
}

// prorateSlotsKopecks — прорейт-стоимость n слотов за ОСТАТОК периода: n×цена × remaining/period.
// Округляем вверх (в пользу сервиса), пол — 100 коп (мин. сумма T-Bank). Чистая функция — тест.
func prorateSlotsAtPrice(n int, remaining, periodSec int64, slotPrice uint64) uint64 {
	if n <= 0 || remaining <= 0 || periodSec <= 0 {
		return 0
	}
	if remaining > periodSec {
		remaining = periodSec
	}
	full := float64(n) * float64(slotPrice)
	amount := uint64(math.Ceil(full * float64(remaining) / float64(periodSec)))
	if amount < 100 {
		amount = 100
	}
	return amount
}

func prorateSlotsKopecks(n int, remaining, periodSec int64) uint64 {
	return prorateSlotsAtPrice(n, remaining, periodSec, legacySlotPriceKopecks)
}

// SlotPrice сохраняет 49 ₽ старой платной когорте и применяет 99 ₽ к новой.
func (s *Service) SlotPrice(userID int64) uint64 {
	if s.BaseAmount(userID) <= legacyBaseAmountKopecks {
		return legacySlotPriceKopecks
	}
	return newSlotPriceKopecks
}

// PreviewMirrorSlots — расчёт покупки n слотов БЕЗ создания платежа: прорейт-сумма за
// остаток периода + конец оплаченного периода. Для промежуточного экрана в кабинете.
func (s *Service) PreviewMirrorSlots(userID int64, n int) (amount uint64, paidUntil int64, err error) {
	if n <= 0 {
		return 0, 0, fmt.Errorf("bad slot count")
	}
	userID = s.BillingID(userID)
	var status string
	if e := s.db.QueryRow(`SELECT status, paid_until FROM subscriptions WHERE user_id=?`, userID).
		Scan(&status, &paidUntil); e != nil {
		return 0, 0, fmt.Errorf("no subscription")
	}
	now := time.Now().Unix()
	if !(status == "active" || status == "canceled" || status == "trial") || paidUntil <= now {
		return 0, 0, fmt.Errorf("subscription not active")
	}
	periodSec := int64(s.cfg.PeriodDays) * 86400
	return prorateSlotsAtPrice(n, paidUntil-now, periodSec, s.SlotPrice(userID)), paidUntil, nil
}

// BuyMirrorSlots создаёт РАЗОВЫЙ платёж (ссылку) на докупку n слотов зеркала: прорейт за остаток
// текущего периода. НЕ списывает с карты и НЕ регистрирует рекуррент — пользователь платит по
// ссылке сам. Слоты начисляются по нотификации (kind=mirror). Возвращает URL оплаты и сумму (коп).
func (s *Service) BuyMirrorSlots(ctx context.Context, userID int64, n int) (payURL string, amount uint64, err error) {
	if n <= 0 {
		return "", 0, fmt.Errorf("bad slot count")
	}
	userID = s.BillingID(userID) // подписка может быть под связанным (TG↔MAX) id
	var status string
	var paidUntil int64
	if e := s.db.QueryRow(`SELECT status, paid_until FROM subscriptions WHERE user_id=?`, userID).
		Scan(&status, &paidUntil); e != nil {
		return "", 0, fmt.Errorf("no subscription")
	}
	now := time.Now().Unix()
	if !(status == "active" || status == "canceled" || status == "trial") || paidUntil <= now {
		return "", 0, fmt.Errorf("subscription not active")
	}
	periodSec := int64(s.cfg.PeriodDays) * 86400
	amount = prorateSlotsAtPrice(n, paidUntil-now, periodSec, s.SlotPrice(userID))

	orderID := fmt.Sprintf("%s%d-%d-%d", mirrorOrderPrefix, userID, time.Now().UnixNano(), n)
	initReq := &tinkoff.InitRequest{
		Amount:          amount,
		OrderID:         orderID,
		CustomerKey:     strconv.FormatInt(userID, 10),
		Description:     fmt.Sprintf("Докупка %d групп зеркала", n),
		NotificationURL: s.cfg.NotifyURL,
		SuccessURL:      s.cfg.SuccessURL,
		FailURL:         s.cfg.FailURL,
		Recurrent:       "", // разовый платёж по ссылке, без рекуррента и без Charge
	}
	if s.cfg.ReceiptEmail != "" {
		initReq.Receipt = s.receiptFor(amount)
	}
	initResp, err := s.cli.InitWithContext(ctx, initReq)
	if err != nil {
		return "", 0, err
	}
	return initResp.PaymentURL, amount, nil
}

// ReduceMirrorSlots уменьшает слоты на n (не ниже 0). Без возврата: рекуррент снизится со
// следующего списания (EffectiveAmount пересчитается). Возвращает новое число слотов.
func (s *Service) ReduceMirrorSlots(userID int64, n int) (int, error) {
	if n <= 0 {
		return s.MirrorSlots(userID), nil
	}
	userID = s.BillingID(userID)
	next := s.MirrorSlots(userID) - n
	if next < 0 {
		next = 0
	}
	_, err := s.db.Exec(`UPDATE subscriptions SET mirror_slots=?, updated_at=? WHERE user_id=?`,
		next, time.Now().Unix(), userID)
	return next, err
}

// grantMirrorSlots начисляет слоты (вызывается из нотификации по kind=mirror). Идемпотентность —
// на уровне recordPayment у вызывающего.
func (s *Service) grantMirrorSlots(userID int64, n int) {
	if userID == 0 || n <= 0 {
		return
	}
	s.db.Exec(`UPDATE subscriptions SET mirror_slots = mirror_slots + ?, updated_at=? WHERE user_id=?`,
		n, time.Now().Unix(), userID)
}

// parseMirrorOrder разбирает OrderID "mslot-<uid>-<ts>-<n>" → uid, n. Чистая функция — тест.
func parseMirrorOrder(orderID string) (userID int64, n int, ok bool) {
	if !strings.HasPrefix(orderID, mirrorOrderPrefix) {
		return 0, 0, false
	}
	parts := strings.Split(strings.TrimPrefix(orderID, mirrorOrderPrefix), "-")
	if len(parts) != 3 {
		return 0, 0, false
	}
	uid, err1 := strconv.ParseInt(parts[0], 10, 64)
	cnt, err2 := strconv.Atoi(parts[2])
	if err1 != nil || err2 != nil || cnt <= 0 {
		return 0, 0, false
	}
	return uid, cnt, true
}
