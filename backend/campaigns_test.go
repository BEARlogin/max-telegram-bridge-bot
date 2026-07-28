package main

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAdCampaignStatsConnectsStartsActivationAndRevenue(t *testing.T) {
	t.Setenv("TG_BOT_URL", "https://t.me/TestBridgeBot")
	dir := t.TempDir()
	addonPath := filepath.Join(dir, "addon.db")
	bridgePath := filepath.Join(dir, "bridge.db")

	addonDB, err := sql.Open("sqlite", addonPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = ensureAdCampaignSchema(addonDB); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`INSERT INTO ad_campaigns(id,name,source,note,active,created_at,updated_at)
			VALUES(1,'Telegram Ads','telegram_ads','Креатив A',1,900,900)`,
		`INSERT INTO ad_campaign_starts(campaign_id,user_id,tg_message_id,started_at,is_new)
			VALUES(1,101,10,1000,1),(1,101,11,1001,1)`,
		`INSERT INTO ad_attributions(user_id,campaign_id,attributed_at,is_new)
			VALUES(101,1,1000,1)`,
	} {
		if _, err = addonDB.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if err = addonDB.Close(); err != nil {
		t.Fatal(err)
	}

	bridgeDB, err := sql.Open("sqlite", bridgePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`CREATE TABLE pairs(tg_owner_id INTEGER,created_at INTEGER)`,
		`CREATE TABLE crossposts(tg_owner_id INTEGER,created_at INTEGER,deleted_at INTEGER)`,
		`INSERT INTO pairs VALUES(101,1100)`,
	} {
		if _, err = bridgeDB.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if err = bridgeDB.Close(); err != nil {
		t.Fatal(err)
	}

	billingDB, err := sql.Open("sqlite", filepath.Join(dir, "billing.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer billingDB.Close()
	for _, query := range []string{
		`CREATE TABLE subscriptions(user_id INTEGER,trial_used INTEGER,updated_at INTEGER)`,
		`CREATE TABLE payments(user_id INTEGER,amount INTEGER,kind TEXT,status TEXT,at INTEGER)`,
		`INSERT INTO subscriptions VALUES(101,1,1200)`,
		`INSERT INTO payments VALUES(101,49900,'sub','CONFIRMED',1300)`,
	} {
		if _, err = billingDB.Exec(query); err != nil {
			t.Fatal(err)
		}
	}

	campaigns, err := loadAdCampaignStats(addonPath, bridgePath, billingDB)
	if err != nil {
		t.Fatal(err)
	}
	if len(campaigns) != 1 {
		t.Fatalf("campaigns=%+v", campaigns)
	}
	got := campaigns[0]
	if got.Starts != 2 || got.UniqueVisitors != 1 || got.AttributedUsers != 1 ||
		got.NewUsers != 1 || got.ActivatedUsers != 1 || got.TrialUsers != 1 ||
		got.PaidUsers != 1 || got.ProUsers != 1 || got.RevenueKopecks != 49900 ||
		got.ConversionToPaid != 100 {
		t.Fatalf("stats=%+v", got)
	}
	if !strings.Contains(got.Link, "t.me/TestBridgeBot") || !strings.Contains(got.Link, "start=1") {
		t.Fatalf("link=%q", got.Link)
	}
}

func TestAdCampaignSchemaAndLinkUseNumericCampaignID(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "addon.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = ensureAdCampaignSchema(db); err != nil {
		t.Fatal(err)
	}
	result, err := db.Exec(`INSERT INTO ad_campaigns(name,active,created_at,updated_at)
		VALUES('Посев',1,1,1)`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	if id <= 0 || !strings.HasSuffix(adCampaignLink(id), "?start=1") {
		t.Fatalf("id=%d link=%q", id, adCampaignLink(id))
	}
}
