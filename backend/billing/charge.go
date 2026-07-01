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
		// Сдвигаем next_charge сразу (защита от двойного списания, если нотификация задержится).
		s.db.Exec(`UPDATE subscriptions SET next_charge=?, updated_at=? WHERE user_id=?`,
			time.Now().AddDate(0, 0, 1).Unix(), now, d.userID) // +1 день; нотификация продлит на период
		if err := s.charge(ctx, d.userID, d.rebill); err != nil {
			log.Printf("billing: charge failed user=%d: %v", d.userID, err)
		}
	}
}

// charge: новый Init (без Recurrent) → Charge(PaymentID, RebillID). Подтверждение — нотификацией.
func (s *Service) charge(ctx context.Context, userID int64, rebillID string) error {
	orderID := fmt.Sprintf("sub-%d-%d", userID, time.Now().UnixNano())
	req := &tinkoff.InitRequest{
		Amount:      s.cfg.AmountKopecks,
		OrderID:     orderID,
		CustomerKey: strconv.FormatInt(userID, 10),
		Description: "Продление PRO-подписки",
		Recurrent:   "", // автосписание делает Charge, не Recurrent
	}
	if s.cfg.ReceiptEmail != "" {
		req.Receipt = s.receiptFor(s.cfg.AmountKopecks)
	}
	initResp, err := s.cli.InitWithContext(ctx, req)
	if err != nil {
		return err
	}
	// привязываем текущий order_id, чтобы нотификация нашла подписку
	s.db.Exec(`UPDATE subscriptions SET order_id=?, updated_at=? WHERE user_id=?`, orderID, time.Now().Unix(), userID)
	_, err = s.cli.ChargeWithContext(ctx, &tinkoff.ChargeRequest{
		PaymentID: initResp.PaymentID,
		RebillID:  rebillID,
	})
	return err
}
