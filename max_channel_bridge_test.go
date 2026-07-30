package main

import (
	"context"
	"path/filepath"
	"testing"
)

func TestCompleteMaxBridgeKeyAllowsSeveralTgGroupsInOneMaxChannel(t *testing.T) {
	repo, err := NewSQLiteRepo(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatal(err)
	}
	if sqlite, ok := repo.(*sqliteRepo); ok {
		t.Cleanup(func() { _ = sqlite.db.Close() })
	}

	b := &Bridge{repo: repo}
	const maxChannelID, maxOwnerID = int64(-7001), int64(901)
	tgSources := []struct {
		chatID  int64
		ownerID int64
	}{
		{chatID: -1001001, ownerID: 101},
		{chatID: -1001002, ownerID: 102},
	}

	for _, src := range tgSources {
		paired, key, registerErr := repo.Register("", "tg", src.chatID, src.ownerID)
		if registerErr != nil || paired || key == "" {
			t.Fatalf("create key for %d: paired=%v key=%q err=%v", src.chatID, paired, key, registerErr)
		}
		if reply, ok := b.completeMaxBridgeKey(context.Background(), key, maxChannelID, maxOwnerID); !ok {
			t.Fatalf("complete %d: %s", src.chatID, reply)
		}
	}

	got := repo.GetTgChats(maxChannelID)
	if len(got) != 2 {
		t.Fatalf("sources=%v, want both TG groups", got)
	}
	seen := map[int64]bool{}
	for _, id := range got {
		seen[id] = true
	}
	for _, src := range tgSources {
		if !seen[src.chatID] {
			t.Fatalf("source %d missing from %v", src.chatID, got)
		}
	}
}
