package billing

import (
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestStatsUsesActualRenewalsForMRR(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		`CREATE TABLE subscriptions (
			user_id INTEGER PRIMARY KEY,status TEXT,amount INTEGER,paid_until INTEGER,
			rebill_id TEXT,mirror_slots INTEGER,trial_used INTEGER
		)`,
		`CREATE TABLE payments (
			user_id INTEGER,amount INTEGER,kind TEXT,status TEXT,at INTEGER
		)`,
		`CREATE TABLE account_links (tg_id INTEGER,max_id INTEGER)`,
	} {
		if _, err = db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	future := time.Now().Add(24 * time.Hour).Unix()
	if _, err = db.Exec(`INSERT INTO subscriptions VALUES
		(1,'active',29900,?,'card-1',2,1),
		(2,'active',49900,?,'card-2',1,0),
		(3,'trial',0,?,'',0,1),
		(4,'active',0,?,'',0,0)`, future, future, future, future); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO payments VALUES
		(1,29900,'sub','CONFIRMED',1),
		(2,49900,'sub','AUTHORIZED',2)`); err != nil {
		t.Fatal(err)
	}

	got := (&Service{db: db, cfg: Config{AmountKopecks: 49900}}).Stats()
	if got["mrr_rub"] != int64(995) { // 299 + 2×49 + 499 + 1×99
		t.Fatalf("mrr_rub=%v", got["mrr_rub"])
	}
	if got["pro_paying"] != int64(2) || got["sub_renewing"] != int64(2) {
		t.Fatalf("paying=%v renewing=%v", got["pro_paying"], got["sub_renewing"])
	}
}
