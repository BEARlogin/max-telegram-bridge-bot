package billing

import "testing"

func TestProrateSlotsKopecks(t *testing.T) {
	const period = int64(30 * 86400) // 30 дней
	cases := []struct {
		name              string
		n                 int
		remaining, period int64
		want              uint64
	}{
		{"full period, 1 slot", 1, period, period, 30000},          // весь период = полная цена
		{"half period, 1 slot", 1, period / 2, period, 15000},      // половина
		{"half period, 10 slots", 10, period / 2, period, 150000},  // 10×300×0.5 = 1500₽
		{"full period, 10 slots", 10, period, period, 300000},      // 3000₽
		{"tiny remaining floors to 100", 1, 1, period, 100},        // пол 1₽
		{"zero remaining", 1, 0, period, 0},                        // истёк — 0
		{"n<=0", 0, period, period, 0},                             // нет слотов
		{"remaining>period clamps", 5, period * 2, period, 150000}, // клампим до полного
	}
	for _, c := range cases {
		if got := prorateSlotsKopecks(c.n, c.remaining, c.period); got != c.want {
			t.Errorf("%s: prorate(%d,%d,%d)=%d want %d", c.name, c.n, c.remaining, c.period, got, c.want)
		}
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
