package billing

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestProrateSlotsKopecks(t *testing.T) {
	const period = int64(30 * 86400) // 30 дней
	cases := []struct {
		name              string
		n                 int
		remaining, period int64
		want              uint64
	}{
		{"full period, 1 slot", 1, period, period, 4900},          // весь период = полная цена
		{"half period, 1 slot", 1, period / 2, period, 2450},      // половина
		{"half period, 10 slots", 10, period / 2, period, 24500},  // 10×49×0.5 = 245₽
		{"full period, 10 slots", 10, period, period, 49000},      // 490₽
		{"tiny remaining floors to 100", 1, 1, period, 100},       // пол 1₽
		{"zero remaining", 1, 0, period, 0},                       // истёк — 0
		{"n<=0", 0, period, period, 0},                            // нет слотов
		{"remaining>period clamps", 5, period * 2, period, 24500}, // клампим до полного
	}
	for _, c := range cases {
		if got := prorateSlotsKopecks(c.n, c.remaining, c.period); got != c.want {
			t.Errorf("%s: prorate(%d,%d,%d)=%d want %d", c.name, c.n, c.remaining, c.period, got, c.want)
		}
	}
}

func TestPriceCohortsPreserveLegacySubscription(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s, err := New(db, Config{AmountKopecks: 49900})
	if err != nil {
		t.Fatal(err)
	}
	if got := s.BaseAmount(10); got != 49900 {
		t.Fatalf("new user base=%d want 49900", got)
	}
	if got := s.SlotPrice(10); got != 9900 {
		t.Fatalf("new user slot=%d want 9900", got)
	}
	_, err = db.Exec(`INSERT INTO subscriptions(user_id,status,amount,created_at,updated_at)
		VALUES(20,'canceled',29900,1,1)`)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.BaseAmount(20); got != 49900 {
		t.Fatalf("unpaid subscription row base=%d want 49900", got)
	}
	if got := s.SlotPrice(20); got != 9900 {
		t.Fatalf("unpaid subscription row slot=%d want 9900", got)
	}
	_, err = db.Exec(`INSERT INTO payments(payment_id,user_id,order_id,amount,status,kind,at)
		VALUES('legacy-payment',20,'sub-20-old',29900,'AUTHORIZED','sub',1)`)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.BaseAmount(20); got != 29900 {
		t.Fatalf("legacy base=%d want 29900", got)
	}
	if got := s.SlotPrice(20); got != 4900 {
		t.Fatalf("legacy slot=%d want 4900", got)
	}
}

func TestParseMirrorOrder(t *testing.T) {
	cases := []struct {
		order   string
		wantUID int64
		wantN   int
		wantOK  bool
	}{
		{"mslot-336903139-1700000000000000000-10", 336903139, 10, true},
		{"mslot-5-1-1", 5, 1, true},
		{"sub-336903139-123", 0, 0, false}, // не mirror
		{"mslot-abc-1-2", 0, 0, false},     // кривой uid
		{"mslot-5-1-0", 0, 0, false},       // n<=0
		{"mslot-5-1", 0, 0, false},         // мало частей
		{"", 0, 0, false},
	}
	for _, c := range cases {
		uid, n, ok := parseMirrorOrder(c.order)
		if uid != c.wantUID || n != c.wantN || ok != c.wantOK {
			t.Errorf("parse(%q)=(%d,%d,%v) want (%d,%d,%v)", c.order, uid, n, ok, c.wantUID, c.wantN, c.wantOK)
		}
	}
}
