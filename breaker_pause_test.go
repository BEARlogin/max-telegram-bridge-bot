package main

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// testRepo поднимает мигрированную in-memory(file) sqlite и возвращает sqliteRepo.
func testRepo(t *testing.T) *sqliteRepo {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "t.db")+"?_journal_mode=WAL")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := runMigrations(db, "sqlite3"); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return &sqliteRepo{db: db}
}

func TestIsChatUnavailable(t *testing.T) {
	unavail := []string{
		"chat.denied", "chat.not.found", "not enough rights",
		"Forbidden: bot was kicked from the group chat",
		"Bad Request: not enough rights to send", "user is deactivated",
	}
	for _, s := range unavail {
		if !isChatUnavailable(s) {
			t.Errorf("isChatUnavailable(%q) = false, want true", s)
		}
	}
	// Глобальный бан аккаунта MAX (account.blocked / 403) и транзиенты — НЕ повод
	// паузить связку (иначе встанет всё).
	ok := []string{"account.blocked", "403", "timeout", "context deadline exceeded", ""}
	for _, s := range ok {
		if isChatUnavailable(s) {
			t.Errorf("isChatUnavailable(%q) = true, want false", s)
		}
	}
}

func TestCbPauseForChat_PairTransition(t *testing.T) {
	repo := testRepo(t)
	const tgChat, maxChat, owner = int64(111), int64(-999), int64(555)
	if _, err := repo.db.Exec(
		"INSERT INTO pairs (tg_chat_id, max_chat_id, prefix, created_at, tg_owner_id, max_owner_id) VALUES (?,?,?,?,?,?)",
		tgChat, maxChat, 0, 0, owner, 0); err != nil {
		t.Fatalf("insert pair: %v", err)
	}
	b := &Bridge{repo: repo}

	// owner резолвится и по MAX-, и по TG-стороне.
	if got := repo.TgOwnerForChat(maxChat); got != owner {
		t.Fatalf("TgOwnerForChat(max) = %d, want %d", got, owner)
	}
	if got := repo.TgOwnerForChat(tgChat); got != owner {
		t.Fatalf("TgOwnerForChat(tg) = %d, want %d", got, owner)
	}

	// Пауза по MAX-стороне (направление TG→MAX): первый раз — переход, второй — нет.
	if !b.cbPauseForChat(maxChat) {
		t.Fatal("first cbPauseForChat = false, want transition true")
	}
	if !repo.PairPaused(tgChat, maxChat) {
		t.Fatal("pair not paused after cbPauseForChat")
	}
	if b.cbPauseForChat(maxChat) {
		t.Fatal("second cbPauseForChat = true, want false (already paused, no re-notify)")
	}

	// После /unpause — пауза по TG-стороне (направление MAX→TG) снова даёт переход.
	if err := repo.SetPairPaused(tgChat, maxChat, false); err != nil {
		t.Fatalf("unpause: %v", err)
	}
	if !b.cbPauseForChat(tgChat) {
		t.Fatal("cbPauseForChat(tg) = false, want transition true")
	}
	if !repo.PairPaused(tgChat, maxChat) {
		t.Fatal("pair not paused via tg side")
	}
}

func TestCbPauseForChat_NoPairing(t *testing.T) {
	repo := testRepo(t)
	b := &Bridge{repo: repo}
	if b.cbPauseForChat(424242) {
		t.Fatal("cbPauseForChat for unknown chat = true, want false")
	}
}

func TestClaimCrosspost(t *testing.T) {
	repo := testRepo(t)
	// Первый claim того же сообщения — проходит (кросспостим); повтор — дубль (пропустить).
	if !repo.ClaimCrosspost("tg", -100500, "42") {
		t.Fatal("первый claim должен пройти")
	}
	if repo.ClaimCrosspost("tg", -100500, "42") {
		t.Fatal("повторный claim того же (platform,chat,msg) должен быть дублем")
	}
	// Другой msg_id / чат / платформа — независимы, проходят.
	if !repo.ClaimCrosspost("tg", -100500, "43") {
		t.Fatal("другой msg_id должен пройти")
	}
	if !repo.ClaimCrosspost("tg", -999, "42") {
		t.Fatal("другой чат должен пройти")
	}
	if !repo.ClaimCrosspost("max", -100500, "42") {
		t.Fatal("другая платформа должна пройти")
	}
}

func TestIsTgServiceSender(t *testing.T) {
	for _, id := range []int64{777000, 1087968824, 136817688} {
		if !isTgServiceSender(id) {
			t.Errorf("id %d должен быть служебным (не модерируем)", id)
		}
	}
	for _, id := range []int64{0, 123, 336903139, -1} {
		if isTgServiceSender(id) {
			t.Errorf("id %d НЕ служебный", id)
		}
	}
}

func TestMaxTextHasLink(t *testing.T) {
	b := &Bridge{cfg: Config{
		MaxBotURL:        "https://max.ru/id710708943262_bot",
		MaxBotURLReserve: "https://max.ru/id710708943262_4_bot",
	}}
	cases := []struct {
		text string
		want bool
	}{
		{"@id710708943262_4_bot", false},          // упоминание самого (запасного) бота — НЕ ссылка
		{"@id710708943262_bot", false},            // основной бот — НЕ ссылка
		{"@id710708943262_bot привет", false},     // бот + текст
		{"@scamchannel подпишись", true},          // чужой канал — ссылка
		{"@id710708943262_bot и @scam", true},     // есть чужое упоминание
		{"http://evil.example", true},             // http
		{"загляни на t.me/foo", true},             // t.me
		{"обычный текст без ссылок", false},        // нет ничего
		{"", false},
	}
	for _, c := range cases {
		if got := b.maxTextHasLink(c.text); got != c.want {
			t.Errorf("maxTextHasLink(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}
