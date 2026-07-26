package main

import (
	"fmt"
	"testing"
)

func TestCrosspostLinksAreManyToMany(t *testing.T) {
	repo := testRepo(t)
	const tgA = int64(-100101)
	const tgB = int64(-100102)
	const maxA = int64(-200201)
	const maxB = int64(-200202)

	if err := repo.PairCrosspost(tgA, maxA, 11, 21); err != nil {
		t.Fatal(err)
	}
	if err := repo.PairCrosspost(tgB, maxA, 12, 22); err != nil {
		t.Fatal(err)
	}
	if err := repo.PairCrosspost(tgA, maxB, 13, 21); err != nil {
		t.Fatal(err)
	}

	maxLinks := repo.GetCrosspostTgChats(maxA)
	if len(maxLinks) != 2 {
		t.Fatalf("MAX accumulator links = %v, want two TG sources", maxLinks)
	}
	seenTG := map[int64]bool{}
	for _, l := range maxLinks {
		seenTG[l.TgChatID] = true
		if l.MaxChatID != maxA {
			t.Fatalf("link has wrong max: %+v", l)
		}
	}
	if !seenTG[tgA] || !seenTG[tgB] {
		t.Fatalf("MAX accumulator saw TG sources %v", seenTG)
	}

	tgLinks := repo.GetCrosspostMaxChats(tgA)
	if len(tgLinks) != 2 {
		t.Fatalf("TG accumulator links = %v, want two MAX sources", tgLinks)
	}
	seenMAX := map[int64]bool{}
	for _, l := range tgLinks {
		seenMAX[l.MaxChatID] = true
		if l.TgChatID != tgA {
			t.Fatalf("link has wrong tg: %+v", l)
		}
	}
	if !seenMAX[maxA] || !seenMAX[maxB] {
		t.Fatalf("TG accumulator saw MAX sources %v", seenMAX)
	}
}

func TestCrosspostClaimIsPerDestination(t *testing.T) {
	repo := testRepo(t)
	const tgChannel = int64(-100101)
	const maxA = int64(-200201)
	const maxB = int64(-200202)

	if !repo.ClaimCrosspost("tg", tgChannel, "42:"+itoa64(maxA)) {
		t.Fatal("first delivery to maxA must claim")
	}
	if repo.ClaimCrosspost("tg", tgChannel, "42:"+itoa64(maxA)) {
		t.Fatal("duplicate delivery to same maxA must not claim")
	}
	if !repo.ClaimCrosspost("tg", tgChannel, "42:"+itoa64(maxB)) {
		t.Fatal("same TG post must be deliverable to another MAX destination")
	}
}

func itoa64(v int64) string {
	return fmt.Sprintf("%d", v)
}
