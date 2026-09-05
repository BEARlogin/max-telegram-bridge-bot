package main

import (
	"context"
	"testing"

	maxschemes "github.com/max-messenger/max-bot-api-client-go/schemes"
)

type crosspostProTestAddon struct {
	Addon
	active bool
	owners [2]int64
}

func (a *crosspostProTestAddon) CrosspostProActive(_ context.Context, maxOwner, tgOwner int64) bool {
	a.owners = [2]int64{maxOwner, tgOwner}
	return a.active
}

func TestCrosspostFiltersRequireActivePROForExactPair(t *testing.T) {
	repo := testRepo(t)
	if err := repo.PairCrosspost(-1001, -2001, 11, 21); err != nil {
		t.Fatal(err)
	}
	if err := repo.PairCrosspost(-1002, -2001, 12, 22); err != nil {
		t.Fatal(err)
	}
	checker := &crosspostProTestAddon{}
	b := &Bridge{repo: repo, addon: checker}
	if b.crosspostProPair(context.Background(), -1001, -2001) {
		t.Fatal("inactive PRO enabled filters")
	}
	if checker.owners != [2]int64{11, 21} {
		t.Fatalf("owners=%v want [11 21]", checker.owners)
	}
	checker.active = true
	if !b.crosspostProPair(context.Background(), -1002, -2001) {
		t.Fatal("active PRO did not enable filters")
	}
	if checker.owners != [2]int64{12, 22} {
		t.Fatalf("owners=%v want [12 22]", checker.owners)
	}
}

func TestTgCrosspostExcluded(t *testing.T) {
	filters := []string{"#реклама", "партнёрский материал"}
	for _, tc := range []struct {
		name string
		msg  *TGMessage
		want bool
	}{
		{"text", &TGMessage{Text: "Партнёрский пост #реклама"}, true},
		{"caption", &TGMessage{Caption: "Фото #реклама"}, true},
		{"second filter", &TGMessage{Text: "Это партнёрский материал"}, true},
		{"different case", &TGMessage{Text: "#Реклама"}, false},
		{"missing", &TGMessage{Text: "Обычный пост"}, false},
		{"empty filters", &TGMessage{Text: "#реклама"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := tgCrosspostExcluded(tc.msg, filters)
			if tc.name == "empty filters" {
				got = tgCrosspostExcluded(tc.msg, nil)
			}
			if got != tc.want {
				t.Fatalf("tgCrosspostExcluded()=%v want %v", got, tc.want)
			}
		})
	}
}

func TestCrosspostExcludeSurvivesReplacementsJSON(t *testing.T) {
	want := []string{"#реклама", "партнёрский материал"}
	raw := marshalCrosspostReplacements(CrosspostReplacements{TgToMaxExcludeContains: want, MaxToTgExcludeContains: []string{"promo"}})
	if raw == "" {
		t.Fatal("exclude-only settings were discarded")
	}
	got := parseCrosspostReplacements(raw).TgToMaxExcludeContains
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("exclude=%q want %q", got, want)
	}
	if got := parseCrosspostReplacements(raw).MaxToTgExcludeContains; len(got) != 1 || got[0] != "promo" {
		t.Fatalf("MAX→TG exclude=%q", got)
	}
}

func TestMaxCrosspostExcludeRequiresExactPairPRO(t *testing.T) {
	repo := testRepo(t)
	const maxChatID int64 = -2001
	for _, tgChatID := range []int64{-1001, -1002} {
		if err := repo.PairCrosspost(tgChatID, maxChatID, 11, -tgChatID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := repo.db.Exec(`UPDATE crossposts SET replacements=? WHERE tg_chat_id=? AND max_chat_id=?`,
		marshalCrosspostReplacements(CrosspostReplacements{MaxToTgExcludeContains: []string{"#реклама"}}), -1001, maxChatID); err != nil {
		t.Fatal(err)
	}
	checker := &crosspostProTestAddon{active: true}
	b := &Bridge{repo: repo, addon: checker}
	upd := &maxschemes.MessageCreatedUpdate{Message: maxschemes.Message{Body: maxschemes.MessageBody{Text: "пост #реклама"}}}
	if !b.maxCrosspostExcluded(context.Background(), -1001, maxChatID, upd) {
		t.Fatal("exact pair filter was not applied")
	}
	if b.maxCrosspostExcluded(context.Background(), -1002, maxChatID, upd) {
		t.Fatal("sibling pair inherited filter")
	}
	checker.active = false
	if b.maxCrosspostExcluded(context.Background(), -1001, maxChatID, upd) {
		t.Fatal("inactive PRO applied filter")
	}
}

func TestExcludedMaxCrosspostDoesNotConsumeClaim(t *testing.T) {
	repo := testRepo(t)
	const tgChatID, maxChatID int64 = -1001, -2001
	if err := repo.PairCrosspost(tgChatID, maxChatID, 11, 21); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.db.Exec(`UPDATE crossposts SET replacements=? WHERE tg_chat_id=? AND max_chat_id=?`,
		marshalCrosspostReplacements(CrosspostReplacements{MaxToTgExcludeContains: []string{"#реклама"}}), tgChatID, maxChatID); err != nil {
		t.Fatal(err)
	}
	b := &Bridge{repo: repo, addon: &crosspostProTestAddon{active: true}}
	upd := &maxschemes.MessageCreatedUpdate{Message: maxschemes.Message{
		Recipient: maxschemes.Recipient{ChatId: maxChatID},
		Body:      maxschemes.MessageBody{Mid: "mid", Text: "пост #реклама"},
	}}
	const claimKey = "mid:-1001"
	if b.claimMaxCrosspost(context.Background(), tgChatID, maxChatID, claimKey, upd) {
		t.Fatal("excluded post was claimed")
	}
	if !repo.ClaimCrosspost("max", maxChatID, claimKey) {
		t.Fatal("excluded post consumed the deduplication claim")
	}
}

func TestTgCrosspostAlbumExcludedWhenAnyPartMatches(t *testing.T) {
	items := []mediaGroupItem{
		{msg: &TGMessage{Caption: "Обычная подпись"}},
		{msg: &TGMessage{Caption: "Скрытая часть #реклама"}},
		{msg: &TGMessage{}},
	}
	if !tgCrosspostAlbumExcluded(items, []string{"#реклама"}) {
		t.Fatal("album with an excluded part must be skipped as a whole")
	}
}

func TestCrosspostExcludeIsReadForExactLink(t *testing.T) {
	repo := testRepo(t)
	const maxChatID int64 = -2001
	if err := repo.PairCrosspost(-1001, maxChatID, 1, 2); err != nil {
		t.Fatal(err)
	}
	if err := repo.PairCrosspost(-1002, maxChatID, 1, 3); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.db.Exec(`UPDATE crossposts SET replacements=? WHERE tg_chat_id=? AND max_chat_id=?`,
		marshalCrosspostReplacements(CrosspostReplacements{TgToMaxExcludeContains: []string{"#one"}, MaxToTgExcludeContains: []string{"max-one"}}), -1001, maxChatID); err != nil {
		t.Fatal(err)
	}
	if got := repo.GetCrosspostReplacementsFor(-1001, maxChatID).TgToMaxExcludeContains; len(got) != 1 || got[0] != "#one" {
		t.Fatalf("first link exclude=%q", got)
	}
	if got := repo.GetCrosspostReplacementsFor(-1002, maxChatID).TgToMaxExcludeContains; len(got) != 0 {
		t.Fatalf("second link inherited exclude=%q", got)
	}
	if got := repo.GetCrosspostReplacementsFor(-1001, maxChatID).MaxToTgExcludeContains; len(got) != 1 || got[0] != "max-one" {
		t.Fatalf("first link MAX→TG exclude=%q", got)
	}
	if got := repo.GetCrosspostReplacementsFor(-1002, maxChatID).MaxToTgExcludeContains; len(got) != 0 {
		t.Fatalf("second link inherited MAX→TG exclude=%q", got)
	}
}
