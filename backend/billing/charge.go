package billing

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/nikita-vanyasin/tinkoff"
)

// RunCharger периодически списывает по подпискам, у которых подошёл срок (рекуррент).
func (s *Service) RunCharger(ctx context.Context) {
	t := time.NewTicker(1 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.chargeDue(ctx)
		}
	}
}

// chargeDue инициирует автосписание для просроченных активных подписок.
// Сама оплата подтвердится нотификацией (она продлит paid_until).
func (s *Service) chargeDue(ctx context.Context) {
	now := time.Now().Unix()
	// active — штатное продление; past_due — ретрай неудачного списания, но только пока
	// период ещё не истёк (paid_until>now, т.е. внутри окна renewLeadDays). После истечения
	// перестаём долбить карту — PRO просто заканчивается.
	rows, err := s.db.Query(`SELECT user_id, rebill_id FROM subscriptions
		WHERE rebill_id != '' AND next_charge > 0 AND next_charge <= ?
		  AND (status='active' OR (status='past_due' AND paid_until > ?))`, now, now)
	if err != nil {
		return
	}
	type due struct {
		userID int64
		rebill string
	}
	var list []due
	for rows.Next() {
		var d due
		if rows.Scan(&d.userID, &d.rebill) == nil {
			list = append(list, d)
		}
	}
	rows.Close()

	for _, d := range list {
		// Атомарно помечаем списание выполняющимся. Пока не придёт итоговая нотификация,
		// этот аккаунт больше не попадёт в выборку и повторного Charge не будет.
		res, err := s.db.Exec(`UPDATE subscriptions
			SET status='charging', next_charge=?, updated_at=?
			WHERE user_id=? AND next_charge>0 AND next_charge<=?
			  AND (status='active' OR (status='past_due' AND paid_until>?))`,
			time.Now().AddDate(0, 0, 1).Unix(), now, d.userID, now, now)
		if err != nil {
			log.Printf("billing: claim failed user=%d: %v", d.userID, err)
			continue
		}
		claimed, _ := res.RowsAffected()
		if claimed == 0 {
			continue
		}
		if err := s.charge(ctx, d.userID, d.rebill); err != nil {
			log.Printf("billing: charge failed user=%d: %v", d.userID, err)
			s.db.Exec(`UPDATE subscriptions SET status='past_due', updated_at=? WHERE user_id=? AND status='charging'`,
				time.Now().Unix(), d.userID)
		}
	}
}

// charge: новый Init (без Recurrent) → Charge(PaymentID, RebillID). Подтверждение — нотификацией.
func (s *Service) charge(ctx context.Context, userID int64, rebillID string) error {
	orderID := fmt.Sprintf("sub-%d-%d", userID, time.Now().UnixNano())
	// Сумма продления = база + докупленные слоты зеркала×цена (A1: рекуррент растёт/падает
	// вместе со слотами; изменение слот применяется со следующего списания автоматически).
	amount := s.EffectiveAmount(userID)
	req := &tinkoff.InitRequest{
		Amount:          amount,
		OrderID:         orderID,
		CustomerKey:     strconv.FormatInt(userID, 10),
		Description:     "Продление PRO-подписки",
		NotificationURL: s.cfg.NotifyURL,
		Recurrent:       "", // автосписание делает Charge, не Recurrent
	}
	if s.cfg.ReceiptEmail != "" {
		req.Receipt = s.receiptFor(amount)
	}
	initResp, err := s.cli.InitWithContext(ctx, req)
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	_, _ = s.db.Exec(`INSERT OR REPLACE INTO billing_attempts
		(payment_id,user_id,order_id,amount,status,at) VALUES (?,?,?,?,?,?)`,
		initResp.PaymentID, userID, orderID, amount, "INIT", now)
	// привязываем текущий order_id, чтобы нотификация нашла подписку
	s.db.Exec(`UPDATE subscriptions SET order_id=?, updated_at=? WHERE user_id=?`, orderID, now, userID)
	_, err = s.cli.ChargeWithContext(ctx, &tinkoff.ChargeRequest{
		PaymentID: initResp.PaymentID,
		RebillID:  rebillID,
	})
	status := "CHARGED"
	if err != nil {
		status = "ERROR"
	}
	_, _ = s.db.Exec(`UPDATE billing_attempts SET status=? WHERE payment_id=?`, status, initResp.PaymentID)
	return err
}
