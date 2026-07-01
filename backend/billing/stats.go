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

	// Платящие сейчас (оплатили, в периоде) = active + canceled. PRO всего = +trial.
	paying := count(`SELECT COUNT(*) FROM subscriptions WHERE status IN ('active','canceled') AND paid_until>?`, now)
	m["pro_paying"] = paying
	m["pro_total"] = count(`SELECT COUNT(*) FROM subscriptions WHERE status IN ('active','canceled','trial') AND paid_until>?`, now)

	// Выручка (payments.amount в копейках).
	var revTotal, revMonth, revSub, revPosts, payCount int64
	s.db.QueryRow(`SELECT COALESCE(SUM(amount),0), COUNT(*) FROM payments`).Scan(&revTotal, &payCount)
	s.db.QueryRow(`SELECT COALESCE(SUM(amount),0) FROM payments WHERE at >= strftime('%s', strftime('%Y-%m-01 00:00:00','now'))`).Scan(&revMonth)
	s.db.QueryRow(`SELECT COALESCE(SUM(amount),0) FROM payments WHERE kind='sub'`).Scan(&revSub)
	s.db.QueryRow(`SELECT COALESCE(SUM(amount),0) FROM payments WHERE kind='posts'`).Scan(&revPosts)
	m["revenue_total_rub"] = revTotal / 100
	m["revenue_month_rub"] = revMonth / 100
	m["revenue_sub_rub"] = revSub / 100
	m["revenue_posts_rub"] = revPosts / 100
	m["payments_count"] = payCount
	m["mrr_rub"] = paying * int64(s.cfg.AmountKopecks) / 100
	return m
}
