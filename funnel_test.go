package main

import "testing"

// insertPair — прямая вставка связки для теста fan-out «несколько TG → одна MAX».
func insertPair(t *testing.T, r *sqliteRepo, tgChat, maxChat, tgOwner int64) {
	t.Helper()
	if _, err := r.db.Exec(
		"INSERT INTO pairs (tg_chat_id, max_chat_id, prefix, created_at, tg_owner_id, max_owner_id) VALUES (?,?,?,?,?,?)",
		tgChat, maxChat, 0, 0, tgOwner, 0); err != nil {
		t.Fatalf("insert pair: %v", err)
	}
}

// TestGetTgChats_Funnel — одна MAX-группа может быть стоком для нескольких TG-групп.
func TestGetTgChats_Funnel(t *testing.T) {
	repo := testRepo(t)
	const maxChat = int64(-999)

	// Нет связок — пусто, старый одиночный резолвер тоже говорит «не связано».
	if got := repo.GetTgChats(maxChat); len(got) != 0 {
		t.Fatalf("GetTgChats на пустом = %v, want []", got)
	}
	if _, ok := repo.GetTgChat(maxChat); ok {
		t.Fatal("GetTgChat на пустом должен вернуть ok=false")
	}

	// Три TG-группы сливаются в одну MAX-группу.
	insertPair(t, repo, 101, maxChat, 501)
	insertPair(t, repo, 102, maxChat, 502)
	insertPair(t, repo, 103, maxChat, 503)

	got := repo.GetTgChats(maxChat)
	if len(got) != 3 {
		t.Fatalf("GetTgChats = %v, want 3 элемента", got)
	}
	want := map[int64]bool{101: true, 102: true, 103: true}
	for _, id := range got {
		if !want[id] {
			t.Errorf("неожиданный tg_chat_id %d", id)
		}
		delete(want, id)
	}
	if len(want) != 0 {
		t.Fatalf("не все TG-группы вернулись, остались: %v", want)
	}

	// Обратная сторона (TG→MAX) должна оставаться однозначной для каждой TG-группы.
	if max, ok := repo.GetMaxChat(101); !ok || max != maxChat {
		t.Fatalf("GetMaxChat(101) = %d,%v, want %d,true", max, ok, maxChat)
	}
}

// TestMaxMsgDeliveredTo_PerChat — дедуп MAX→TG пер-чат: одно MAX-сообщение может
// доставляться в несколько TG-групп, но в одну и ту же — только раз.
func TestMaxMsgDeliveredTo_PerChat(t *testing.T) {
	repo := testRepo(t)
	const maxChat = int64(-999)
	const maxMid = "mid-42"

	if repo.MaxMsgDeliveredTo(maxMid, 101) {
		t.Fatal("до доставки MaxMsgDeliveredTo должен быть false")
	}
	// Доставили в группу 101.
	repo.SaveMsg(101, 1001, maxChat, maxMid, 0)
	if !repo.MaxMsgDeliveredTo(maxMid, 101) {
		t.Fatal("после SaveMsg(101) доставка в 101 должна детектиться (не шлём повторно)")
	}
	// В группу 102 то же MAX-сообщение ещё НЕ доставлено — фан-аут не должен глушиться.
	if repo.MaxMsgDeliveredTo(maxMid, 102) {
		t.Fatal("доставка в 102 не должна глушиться доставкой в 101 (пер-чат дедуп)")
	}
	repo.SaveMsg(102, 1002, maxChat, maxMid, 0)
	if !repo.MaxMsgDeliveredTo(maxMid, 102) {
		t.Fatal("после SaveMsg(102) доставка в 102 должна детектиться")
	}
	// Пустой mid — всегда false (нечего дедупить).
	if repo.MaxMsgDeliveredTo("", 101) {
		t.Fatal("пустой mid должен давать false")
	}
}

func TestListAndDeleteTgMessageMappingsPreserveWholeAlbum(t *testing.T) {
	repo := testRepo(t)
	const maxChat, tgChat = int64(-999), int64(-1001)
	for _, tgMsgID := range []int{41, 42, 43} {
		repo.SaveMsgOrigin(tgChat, tgMsgID, maxChat, "mid-album", 0, "max")
	}
	got := repo.ListTgMsgIDs("mid-album", tgChat)
	if len(got) != 3 || got[0] != 41 || got[1] != 42 || got[2] != 43 {
		t.Fatalf("album mappings=%v", got)
	}
	repo.DeleteTgMsgMapping(tgChat, 42)
	got = repo.ListTgMsgIDs("mid-album", tgChat)
	if len(got) != 2 || got[0] != 41 || got[1] != 43 {
		t.Fatalf("mappings after delete=%v", got)
	}
}

func TestMaxMessageAlreadyMappedSuppressesTgToMaxEcho(t *testing.T) {
	repo := testRepo(t)
	b := &Bridge{repo: repo}

	if b.maxMessageAlreadyMapped("") {
		t.Fatal("empty mid must not be treated as mapped")
	}
	if b.maxMessageAlreadyMapped("mid-imported") {
		t.Fatal("unknown mid must not be treated as mapped")
	}

	// Channel import relays through the user's scratch DM, so the mapped TG chat
	// may differ from the TG channel paired with the MAX destination.
	repo.SaveMsg(332817449, 254414, -76980483059929, "mid-imported", 0)
	if !b.maxMessageAlreadyMapped("mid-imported") {
		t.Fatal("TG->MAX result must suppress its MAX channel webhook echo")
	}
}
