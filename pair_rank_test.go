package main

import "testing"

func TestPairRankUsesConnectionOrderAcrossBothOwners(t *testing.T) {
	repo := testRepo(t)
	const tgOwner, maxOwner = int64(101), int64(202)
	rows := []struct {
		tgChat, maxChat, createdAt int64
	}{
		{-1003, -2003, 30},
		{-1001, -2001, 10},
		{-1002, -2002, 20},
	}
	for _, row := range rows {
		if _, err := repo.db.Exec(`INSERT INTO pairs
			(tg_chat_id,max_chat_id,prefix,created_at,tg_owner_id,max_owner_id)
			VALUES(?,?,0,?,?,?)`,
			row.tgChat, row.maxChat, row.createdAt, tgOwner, maxOwner); err != nil {
			t.Fatal(err)
		}
	}

	if got := repo.PairRank(maxOwner, tgOwner, -1001, -2001); got != 0 {
		t.Fatalf("oldest rank=%d, want 0", got)
	}
	if got := repo.PairRank(maxOwner, tgOwner, -1002, -2002); got != 1 {
		t.Fatalf("middle rank=%d, want 1", got)
	}
	if got := repo.PairRank(maxOwner, tgOwner, -1003, -2003); got != 2 {
		t.Fatalf("newest rank=%d, want 2", got)
	}
}
