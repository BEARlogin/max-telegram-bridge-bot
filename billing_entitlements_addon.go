//go:build addon

package main

import "database/sql"

// entitlementAccountIDs resolves every identity that may own the same billing
// entitlement: the current platform ID, its active workspace billing account,
// linked Telegram/MAX identities, and their workspace billing accounts.
//
// Workspace subscriptions are stored under billing_account_id, while existing
// bridges keep the original Telegram/MAX owner IDs. Keep this resolver shared
// by every PRO/slot check so migration does not turn paying users into free ones.
func entitlementAccountIDs(commenterDB string, userID int64, billingAccountIDs func(int64) (int64, int64)) []int64 {
	if userID == 0 {
		return nil
	}
	seen := map[int64]bool{}
	queue := make([]int64, 0, 6)
	add := func(id int64) {
		if id != 0 && !seen[id] {
			seen[id] = true
			queue = append(queue, id)
		}
	}
	add(userID)

	var db *sql.DB
	if commenterDB != "" {
		if opened, err := sql.Open("sqlite3", commenterDB+"?mode=ro&_busy_timeout=3000"); err == nil {
			db = opened
			defer db.Close()
		}
	}

	out := make([]int64, 0, 6)
	for len(queue) > 0 && len(out) < 64 {
		id := queue[0]
		queue = queue[1:]
		out = append(out, id)

		if billingAccountIDs != nil {
			canonical, linked := billingAccountIDs(id)
			add(canonical)
			add(linked)
		}
		if db == nil {
			continue
		}

		var tgID, maxID int64
		if db.QueryRow(`SELECT tg_id,max_id FROM account_links
			WHERE tg_id=? OR max_id=? LIMIT 1`, id, id).Scan(&tgID, &maxID) == nil {
			add(tgID)
			add(maxID)
		}

		rows, err := db.Query(`SELECT user_id,billing_account_id
			FROM workspace_billing_aliases
			WHERE user_id=? OR billing_account_id=?`, id, id)
		if err == nil {
			for rows.Next() {
				var memberID, billingID int64
				if rows.Scan(&memberID, &billingID) == nil {
					add(memberID)
					add(billingID)
				}
			}
			rows.Close()
		}
	}
	return out
}
