package main

import (
	"testing"
	"time"
)

func TestPeekQueueReturnsOnlyDueHeadPerDestination(t *testing.T) {
	repo := testRepo(t)
	now := time.Now().Unix()
	items := []QueueItem{
		{Direction: "max2tg", SrcChatID: -11, DstChatID: -101, SrcMsgID: "a1", CreatedAt: now, NextRetry: now + 3600},
		{Direction: "max2tg", SrcChatID: -11, DstChatID: -101, SrcMsgID: "a2", CreatedAt: now, NextRetry: now - 1},
		{Direction: "max2tg", SrcChatID: -12, DstChatID: -102, SrcMsgID: "b1", CreatedAt: now, NextRetry: now - 1},
		{Direction: "tg2max", SrcChatID: -13, DstChatID: -103, SrcMsgID: "c1", CreatedAt: now, NextRetry: now - 1},
	}
	for i := range items {
		if err := repo.EnqueueSend(&items[i]); err != nil {
			t.Fatal(err)
		}
	}

	got, err := repo.PeekQueue(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].SrcMsgID != "b1" || got[1].SrcMsgID != "c1" {
		t.Fatalf("due heads=%+v, want b1,c1; a2 must wait for a1", got)
	}

	if _, err := repo.db.Exec(`UPDATE send_queue SET next_retry=? WHERE src_msg_id='a1'`, now-1); err != nil {
		t.Fatal(err)
	}
	got, err = repo.PeekQueue(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].SrcMsgID != "a1" {
		t.Fatalf("heads after due=%+v, want a1,b1,c1", got)
	}
	if err := repo.DeleteFromQueue(got[0].ID); err != nil {
		t.Fatal(err)
	}
	got, err = repo.PeekQueue(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].SrcMsgID != "a2" {
		t.Fatalf("heads after delete=%+v, want a2,b1,c1", got)
	}
}

func TestQueueClaimsOneItemPerDestination(t *testing.T) {
	b := &Bridge{
		queueInFlight:      make(map[int64]struct{}),
		queueDestInFlight:  make(map[string]struct{}),
		queueMediaInFlight: make(map[int64]struct{}),
	}
	first := QueueItem{ID: 1, Direction: "max2tg", DstChatID: -101}
	sameDestination := QueueItem{ID: 2, Direction: "max2tg", DstChatID: -101}
	otherDestination := QueueItem{ID: 3, Direction: "max2tg", DstChatID: -102}

	if !b.claimQueueItem(first) {
		t.Fatal("first item was not claimed")
	}
	if b.claimQueueItem(sameDestination) {
		t.Fatal("second item for same destination was claimed concurrently")
	}
	if !b.claimQueueItem(otherDestination) {
		t.Fatal("independent destination was blocked")
	}

	b.releaseQueueItem(first)
	if !b.claimQueueItem(sameDestination) {
		t.Fatal("destination was not released")
	}
	b.releaseQueueItem(sameDestination)
	b.releaseQueueItem(otherDestination)
}

func TestQueueReservesWorkersForTextWhenMediaAreSlow(t *testing.T) {
	b := &Bridge{
		queueInFlight:      make(map[int64]struct{}),
		queueDestInFlight:  make(map[string]struct{}),
		queueMediaInFlight: make(map[int64]struct{}),
	}
	for i := int64(1); i <= queueMaxMediaInFlight; i++ {
		if !b.claimQueueItem(QueueItem{ID: i, Direction: "tg2max", DstChatID: -100 - i, AttType: "video"}) {
			t.Fatalf("media item %d was not claimed", i)
		}
	}
	if b.claimQueueItem(QueueItem{ID: 100, Direction: "tg2max", DstChatID: -999, AttType: "video"}) {
		t.Fatal("media exceeded its worker limit")
	}
	if !b.claimQueueItem(QueueItem{ID: 101, Direction: "max2tg", DstChatID: -1000}) {
		t.Fatal("text item was blocked by slow media")
	}
}

func TestTelegramRetryAfterUsesServerDelay(t *testing.T) {
	got, ok := telegramRetryAfter("Too Many Requests: retry after 22: retry_after 22 (429)")
	if !ok || got != 23*time.Second {
		t.Fatalf("delay=%s ok=%v, want 23s", got, ok)
	}
	if _, ok := telegramRetryAfter("temporary timeout"); ok {
		t.Fatal("ordinary error unexpectedly parsed as rate limit")
	}
}
