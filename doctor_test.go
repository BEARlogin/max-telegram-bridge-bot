package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

type workspaceDoctorAddon struct {
	Addon
	ownerIDs []int64
}

func (a workspaceDoctorAddon) DoctorBillingOwnerIDs(context.Context, string, int64) []int64 {
	return a.ownerIDs
}

func TestDoctorConnectionsOwnedMetadataOnly(t *testing.T) {
	repo := testRepo(t)
	const (
		owner      = int64(101)
		foreign    = int64(202)
		tgPair     = int64(-1001)
		maxPair    = int64(-2001)
		tgChannel  = int64(-1002)
		maxChannel = int64(-2002)
	)
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	dayStart := doctorDayStart(now)

	_, err := repo.db.Exec(`INSERT INTO pairs
		(tg_chat_id,max_chat_id,prefix,created_at,tg_owner_id,max_owner_id,paused)
		VALUES (?,?,?,?,?,?,?)`, tgPair, maxPair, 0, dayStart-100, owner, 501, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = repo.db.Exec(`INSERT INTO crossposts
		(tg_chat_id,max_chat_id,direction,created_at,owner_id,tg_owner_id,paused)
		VALUES (?,?,?,?,?,?,?)`, tgChannel, maxChannel, "tg>max", dayStart-200, 502, owner, 1)
	if err != nil {
		t.Fatal(err)
	}
	_, err = repo.db.Exec(`INSERT INTO pairs
		(tg_chat_id,max_chat_id,prefix,created_at,tg_owner_id,max_owner_id,paused)
		VALUES (?,?,?,?,?,?,?)`, -1099, -2099, 0, dayStart-300, foreign, 599, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = repo.db.Exec(`INSERT INTO bot_chats(platform,chat_id,title,chat_type,updated_at) VALUES
		('tg',?,'TG title'||char(10)||'spoof','group',?),
		('max',?,'MAX title','chat',?),
		('tg',?,'Channel TG','channel',?),
		('max',?,'Channel MAX','channel',?)`,
		tgPair, dayStart, maxPair, dayStart, tgChannel, dayStart, maxChannel, dayStart)
	if err != nil {
		t.Fatal(err)
	}

	// Two mappings of one MAX source count as one transfer in the daily report.
	_, err = repo.db.Exec(`INSERT INTO messages
		(tg_chat_id,tg_msg_id,max_chat_id,max_msg_id,tg_thread_id,origin,created_at) VALUES
		(?,?,?,?,0,'tg',?),
		(?,?,?,?,0,'max',?),
		(?,?,?,?,0,'max',?)`,
		tgPair, 1, maxPair, "tg-mid", dayStart+10,
		tgPair, 2, maxPair, "max-mid", dayStart+20,
		tgPair, 3, maxPair, "max-mid", dayStart+21)
	if err != nil {
		t.Fatal(err)
	}
	_, err = repo.db.Exec(`INSERT INTO send_queue
		(direction,src_chat_id,dst_chat_id,src_msg_id,text,attempts,created_at,next_retry)
		VALUES ('tg2max',?,?,?,'SUPER_SECRET_BODY',2,?,?)`,
		tgPair, maxPair, "4", dayStart+30, dayStart+60)
	if err != nil {
		t.Fatal(err)
	}

	connections, err := repo.DoctorConnections("tg", owner, dayStart)
	if err != nil {
		t.Fatal(err)
	}
	if len(connections) != 2 {
		t.Fatalf("connections=%d, want 2: %+v", len(connections), connections)
	}
	var pair, crosspost DoctorConnection
	for _, c := range connections {
		switch c.Kind {
		case "bridge":
			pair = c
		case "crosspost":
			crosspost = c
		}
	}
	if pair.TodayTgToMax != 1 || pair.TodayMaxToTg != 1 {
		t.Fatalf("pair today counts=%d/%d, want 1/1", pair.TodayTgToMax, pair.TodayMaxToTg)
	}
	if pair.PendingTgToMax != 1 || pair.PendingMaxToTg != 0 || pair.MaxAttempts != 2 {
		t.Fatalf("pair queue metadata unexpected: %+v", pair)
	}
	if !crosspost.Paused || crosspost.Direction != "tg>max" {
		t.Fatalf("crosspost metadata unexpected: %+v", crosspost)
	}

	report := formatDoctorReport(now, connections)
	if strings.Contains(report, "SUPER_SECRET_BODY") {
		t.Fatal("doctor report exposed queued message content")
	}
	if strings.Contains(report, "\nspoof") {
		t.Fatal("chat title injected a report line")
	}
	if !strings.Contains(report, "25.07.2026 15:00 МСК") {
		t.Fatalf("report does not use MSK: %q", report)
	}

	foreignView, err := repo.DoctorConnections("tg", foreign, dayStart)
	if err != nil {
		t.Fatal(err)
	}
	if len(foreignView) != 1 || foreignView[0].TgChatID != -1099 {
		t.Fatalf("foreign owner view leaked connections: %+v", foreignView)
	}
	maxView, err := repo.DoctorConnections("max", owner, dayStart)
	if err != nil {
		t.Fatal(err)
	}
	if len(maxView) != 0 {
		t.Fatalf("TG owner ID must not match MAX owner column: %+v", maxView)
	}
}

func TestLegacyPairOwnerMakesConnectionVisible(t *testing.T) {
	repo := testRepo(t)
	const (
		tgChat   = int64(-100500)
		maxChat  = int64(-200500)
		tgOwner  = int64(101)
		maxOwner = int64(202)
	)
	if _, err := repo.db.Exec(`INSERT INTO pairs
		(tg_chat_id,max_chat_id,prefix,created_at,tg_owner_id,max_owner_id,paused)
		VALUES (?,?,0,0,0,0,0)`, tgChat, maxChat); err != nil {
		t.Fatal(err)
	}
	if !repo.SetPairOwner("tg", tgChat, tgOwner) {
		t.Fatal("TG legacy owner was not set")
	}
	if !repo.SetPairOwner("max", maxChat, maxOwner) {
		t.Fatal("MAX legacy owner was not set")
	}

	tgConnections, err := repo.DoctorConnections("tg", tgOwner, 0)
	if err != nil || len(tgConnections) != 1 || tgConnections[0].TgChatID != tgChat {
		t.Fatalf("TG doctor connections=%+v err=%v", tgConnections, err)
	}
	maxConnections, err := repo.DoctorConnections("max", maxOwner, 0)
	if err != nil || len(maxConnections) != 1 || maxConnections[0].MaxChatID != maxChat {
		t.Fatalf("MAX doctor connections=%+v err=%v", maxConnections, err)
	}
}

func TestDoctorConnectionsRejectInvalidPrincipal(t *testing.T) {
	repo := testRepo(t)
	for _, tc := range []struct {
		platform string
		userID   int64
	}{
		{"", 1},
		{"vk", 1},
		{"tg", 0},
		{"max", -1},
	} {
		if _, err := repo.DoctorConnections(tc.platform, tc.userID, 0); err == nil {
			t.Fatalf("DoctorConnections(%q,%d) accepted invalid principal", tc.platform, tc.userID)
		}
	}
}

func TestDoctorReportIncludesWorkspaceOwnedConnections(t *testing.T) {
	repo := testRepo(t)
	const (
		personalID = int64(101)
		billingID  = int64(7000000000000000042)
		tgChatID   = int64(-10042)
		maxChatID  = int64(-20042)
	)
	if _, err := repo.db.Exec(`INSERT INTO crossposts
		(tg_chat_id,max_chat_id,direction,created_at,owner_id,tg_owner_id,paused)
		VALUES (?,?,?,?,?,?,0)`, tgChatID, maxChatID, "both", 123, billingID, billingID); err != nil {
		t.Fatal(err)
	}
	b := &Bridge{repo: repo, addon: workspaceDoctorAddon{ownerIDs: []int64{billingID, billingID}}}
	report, err := b.doctorReport(context.Background(), "tg", personalID, time.Unix(456, 0))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(report, "Каналы") || !strings.Contains(report, "-10042") {
		t.Fatalf("workspace connection missing: %q", report)
	}
	if strings.Count(report, "-10042") != 1 {
		t.Fatalf("workspace connection was duplicated: %q", report)
	}
}

func TestFormatDoctorReportEmpty(t *testing.T) {
	now := time.Date(2026, 7, 25, 23, 30, 0, 0, time.UTC)
	report := formatDoctorReport(now, nil)
	if !strings.Contains(report, "26.07.2026 02:30 МСК") {
		t.Fatalf("MSK date rollover missing: %q", report)
	}
	if !strings.Contains(report, "подтверждённым владельцем") {
		t.Fatalf("empty ownership hint missing: %q", report)
	}
}

func TestFormatDoctorReportLegacyConnectionHasUnknownDate(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	report := formatDoctorReport(now, []DoctorConnection{{
		Kind:      "bridge",
		TgChatID:  -1001,
		MaxChatID: -2001,
		CreatedAt: 0,
	}})
	if !strings.Contains(report, "Связано: да (дата создания не сохранена)") {
		t.Fatalf("legacy connection shown as absent: %q", report)
	}
	if strings.Contains(report, "Связано: не было") {
		t.Fatalf("legacy connection incorrectly shown as absent: %q", report)
	}
}

func TestFormatDoctorReportUsesRuntimeCrosspostStatus(t *testing.T) {
	report := formatDoctorReport(time.Unix(1, 0), []DoctorConnection{{
		Kind: "crosspost", TgChatID: -1001, MaxChatID: -2001,
		RuntimeStatus: "⚠️ Telegram-бот не администратор TG-канала",
	}})
	if !strings.Contains(report, "Статус: ⚠️ Telegram-бот не администратор TG-канала") {
		t.Fatalf("runtime status missing: %q", report)
	}
}

func TestDoctorRateLimitIsPerPlatformAndUser(t *testing.T) {
	b := &Bridge{}
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	if !b.doctorTakeRate("tg", 1, now) {
		t.Fatal("first TG report was rate-limited")
	}
	if b.doctorTakeRate("tg", 1, now.Add(time.Second)) {
		t.Fatal("repeated TG report bypassed rate limit")
	}
	if !b.doctorTakeRate("max", 1, now.Add(time.Second)) {
		t.Fatal("MAX user was coupled to TG rate limit")
	}
	if !b.doctorTakeRate("tg", 2, now.Add(time.Second)) {
		t.Fatal("different TG user was rate-limited")
	}
	if !b.doctorTakeRate("tg", 1, now.Add(doctorRateWindow)) {
		t.Fatal("TG report remained limited after the window")
	}
	if b.doctorTakeRate("vk", 1, now) || b.doctorTakeRate("tg", 0, now) {
		t.Fatal("invalid principal passed rate limiter")
	}
}
