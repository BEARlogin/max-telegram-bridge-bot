package main

import "testing"

func TestMessageOriginDistinguishesSourceFromMirror(t *testing.T) {
	repo := testRepo(t)

	repo.SaveMsgOrigin(-1001, 11, -2001, "tg-origin", 0, "tg")
	repo.SaveMsgOrigin(-1001, 12, -2001, "max-origin", 0, "max")

	if origin, ok := repo.LookupTgMsgOrigin("tg-origin"); !ok || origin != "tg" {
		t.Fatalf("TG origin = %q, %v; want tg, true", origin, ok)
	}
	if origin, ok := repo.LookupTgMsgOrigin("max-origin"); !ok || origin != "max" {
		t.Fatalf("MAX origin = %q, %v; want max, true", origin, ok)
	}
}

func TestLegacyMessageOriginIsEmpty(t *testing.T) {
	repo := testRepo(t)
	repo.SaveMsg(-1001, 11, -2001, "legacy", 0)

	if origin, ok := repo.LookupTgMsgOrigin("legacy"); !ok || origin != "" {
		t.Fatalf("legacy origin = %q, %v; want empty, true", origin, ok)
	}
}

func TestMaxDeleteSyncOnlyForMaxOrigin(t *testing.T) {
	for _, tc := range []struct {
		origin string
		want   bool
	}{
		{origin: "max", want: true},
		{origin: "tg", want: false},
		{origin: "", want: false},
	} {
		if got := shouldSyncMaxDelete(tc.origin); got != tc.want {
			t.Errorf("shouldSyncMaxDelete(%q) = %v, want %v", tc.origin, got, tc.want)
		}
	}
}

func TestTGChannelPostFromMaxIsNotCrosspostedBack(t *testing.T) {
	repo := testRepo(t)
	bridge := &Bridge{repo: repo}

	repo.SaveMsgOrigin(-1001, 21, -2001, "from-max", 0, "max")
	repo.SaveMsgOrigin(-1001, 22, -2001, "from-tg", 0, "tg")

	if !bridge.tgChannelPostCameFromMax(-1001, 21) {
		t.Fatal("MAX-origin channel post must be treated as an echo")
	}
	if bridge.tgChannelPostCameFromMax(-1001, 22) {
		t.Fatal("TG-origin channel post must remain deliverable")
	}
	if bridge.tgChannelPostCameFromMax(-1001, 23) {
		t.Fatal("unmapped channel post must remain deliverable")
	}
}
