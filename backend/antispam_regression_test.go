package main

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestWriteGroupAntispamConfigsRollsBackBothSides(t *testing.T) {
	oldPath := addonDBPath
	addonDBPath = filepath.Join(t.TempDir(), "addon.db")
	t.Cleanup(func() { addonDBPath = oldPath })
	db, err := sql.Open("sqlite", addonDBPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE antispam_config (
		platform TEXT NOT NULL CHECK(platform='tg'), chat_id INTEGER NOT NULL,
		enabled INTEGER, enabled_by INTEGER, mode TEXT, link_delay_h INTEGER, trust_msgs INTEGER,
		strike_limit INTEGER, ban_after INTEGER, action TEXT, mute_minutes INTEGER, warn INTEGER,
		notify TEXT, captcha INTEGER, antiraid INTEGER, profile_guard INTEGER, block_words TEXT,
		block_cats TEXT, del_service INTEGER, tone TEXT, updated_at INTEGER,
		PRIMARY KEY(platform,chat_id))`)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	err = writeGroupAntispamConfigs(-1001, -2001, 7, true, "enforce", 24, 3,
		asPolicy{StrikeLimit: 2, BanAfter: 3, Action: "mute", MuteMinutes: 60, Notify: "ban"})
	if err == nil {
		t.Fatal("expected MAX-side constraint failure")
	}
	db, _ = sql.Open("sqlite", addonDBPath)
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM antispam_config`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("partial settings persisted after rollback: %d", count)
	}
}

func TestTGUnbanCallsUnbanChatMember(t *testing.T) {
	oldURL, oldToken, oldClient := tgAPIURL, tgBotToken, httpShort
	defer func() { tgAPIURL, tgBotToken, httpShort = oldURL, oldToken, oldClient }()
	called := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.URL.Path != "/bottest-token/unbanChatMember" {
			t.Errorf("path=%q", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("chat_id") != "-1001" || q.Get("user_id") != "42" || q.Get("only_if_banned") != "true" {
			t.Errorf("query=%v", q)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()
	tgAPIURL, tgBotToken, httpShort = ts.URL, "test-token", ts.Client()

	if err := tgUnban(-1001, 42); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("unbanChatMember was not called")
	}
}
