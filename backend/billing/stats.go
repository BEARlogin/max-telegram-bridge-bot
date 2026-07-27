package billing

import "time"

// Stats — сводка по подпискам и выручке для админ-панели.
func (s *Service) Stats() map[string]any {
	now := time.Now().Unix()
	m := map[string]any{}
	count := func(q string, args ...any) int64 {
		var n int64
		s.db.QueryRow(q, args...).Scan(&n)
		return n
	}

	// Подписки по состоянию (с действующим доступом — paid_until в будущем).
	m["sub_active"] = count(`SELECT COUNT(*) FROM subscriptions WHERE status='active' AND paid_until>?`, now)
	m["sub_canceled"] = count(`SELECT COUNT(*) FROM subscriptions WHERE status='canceled' AND paid_until>?`, now)
	m["sub_trial"] = count(`SELECT COUNT(*) FROM subscriptions WHERE status='trial' AND paid_until>?`, now)
	m["sub_past_due"] = count(`SELECT COUNT(*) FROM subscriptions WHERE status='past_due' AND paid_until>?`, now)
	m["sub_pending"] = count(`SELECT COUNT(*) FROM subscriptions WHERE status='pending'`)
	m["trials_total"] = count(`SELECT COUNT(*) FROM subscriptions WHERE trial_used=1`)

	// Не считаем бесплатный промодоступ оплатой: платящий должен иметь успешный платёж.
	paying := count(`SELECT COUNT(*) FROM subscriptions s
		WHERE s.status IN ('active','canceled') AND s.paid_until>?
		AND EXISTS (SELECT 1 FROM payments p WHERE p.user_id=s.user_id AND p.kind='sub'
			AND p.status IN ('AUTHORIZED','CONFIRMED'))`, now)
	m["pro_paying"] = paying
	m["pro_total"] = count(`SELECT COUNT(*) FROM subscriptions WHERE status IN ('active','canceled','trial') AND paid_until>?`, now)
	m["paid_users_total"] = count(`SELECT COUNT(DISTINCT user_id) FROM payments
		WHERE kind='sub' AND status IN ('AUTHORIZED','CONFIRMED')`)
	m["sub_renewing"] = count(`SELECT COUNT(*) FROM subscriptions
		WHERE status='active' AND paid_until>? AND rebill_id<>''`, now)
	m["account_links"] = count(`SELECT COUNT(*) FROM account_links`)

	// Выручка (payments.amount в копейках).
	var revTotal, revMonth, revSub, revPosts, revMirror, payCount int64
	s.db.QueryRow(`SELECT COALESCE(SUM(amount),0), COUNT(*) FROM payments`).Scan(&revTotal, &payCount)
	s.db.QueryRow(`SELECT COALESCE(SUM(amount),0) FROM payments WHERE at >= strftime('%s', strftime('%Y-%m-01 00:00:00','now'))`).Scan(&revMonth)
	s.db.QueryRow(`SELECT COALESCE(SUM(amount),0) FROM payments WHERE kind='sub'`).Scan(&revSub)
	s.db.QueryRow(`SELECT COALESCE(SUM(amount),0) FROM payments WHERE kind='posts'`).Scan(&revPosts)
	s.db.QueryRow(`SELECT COALESCE(SUM(amount),0) FROM payments WHERE kind='mirror'`).Scan(&revMirror)
	m["revenue_total_rub"] = float64(revTotal) / 100
	m["revenue_month_rub"] = float64(revMonth) / 100
	m["revenue_sub_rub"] = float64(revSub) / 100
	m["revenue_posts_rub"] = float64(revPosts) / 100
	m["revenue_mirror_rub"] = float64(revMirror) / 100
	m["payments_count"] = payCount
	// MRR = сумма реально запланированных автопродлений. Учитываем legacy-цену,
	// новую цену и допслоты; trial/canceled/promo без привязанной карты не включаем.
	var mrr int64
	s.db.QueryRow(`SELECT COALESCE(SUM(amount + mirror_slots *
		CASE WHEN amount<=29900 THEN 4900 ELSE 9900 END),0)
		FROM subscriptions WHERE status='active' AND paid_until>? AND rebill_id<>''`, now).Scan(&mrr)
	m["mrr_rub"] = mrr / 100
	return m
}
