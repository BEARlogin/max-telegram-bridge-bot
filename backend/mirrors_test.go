package main

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestSlotsInfoMatchesBridgeSlotCategories(t *testing.T) {
	oldAddonPath, oldBridgePath := addonDBPath, bridgeDBPath
	dir := t.TempDir()
	addonDBPath = filepath.Join(dir, "addon.db")
	bridgeDBPath = filepath.Join(dir, "bridge.db")
	t.Cleanup(func() {
		addonDBPath = oldAddonPath
		bridgeDBPath = oldBridgePath
	})

	bridgeDB, err := sql.Open("sqlite", bridgeDBPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = bridgeDB.Exec(`
		CREATE TABLE pairs (
			tg_chat_id INTEGER, max_chat_id INTEGER,
			tg_owner_id INTEGER, max_owner_id INTEGER
		);
		CREATE TABLE crossposts (
			tg_chat_id INTEGER, max_chat_id INTEGER,
			owner_id INTEGER, tg_owner_id INTEGER, deleted_at INTEGER
		);
		CREATE TABLE bot_chats (
			platform TEXT, chat_id INTEGER, title TEXT,
			PRIMARY KEY(platform,chat_id)
		);
		INSERT INTO pairs VALUES (-101,-201,10,0),(-109,-209,20,0);
		INSERT INTO crossposts VALUES (-102,-202,10,10,0);
		INSERT INTO bot_chats VALUES
			('tg',-101,'TG group'),('max',-201,'MAX group'),
			('tg',-102,'TG channel'),('max',-202,'MAX channel'),
			('max',-203,'MAX inbox');
	`)
	if err != nil {
		t.Fatal(err)
	}
	_ = bridgeDB.Close()

	addonDB, err := sql.Open("sqlite", addonDBPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = addonDB.Exec(`
		CREATE TABLE max_mirror (src_chat INTEGER,dst_chat INTEGER,owner_id INTEGER);
		CREATE TABLE tg_mirror (src_chat INTEGER,dst_chat INTEGER,owner_id INTEGER);
		CREATE TABLE vk_accounts (id INTEGER PRIMARY KEY,community_id INTEGER);
		CREATE TABLE vk_endpoints (
			id INTEGER PRIMARY KEY,account_id INTEGER,kind TEXT,title TEXT
		);
		CREATE TABLE vk_bindings (
			id INTEGER PRIMARY KEY,owner_id INTEGER,source_platform TEXT,
			source_chat_id INTEGER,endpoint_id INTEGER
		);
		CREATE TABLE dm_contacts (
			id INTEGER PRIMARY KEY,owner_id INTEGER,
			a_platform TEXT,a_user_id INTEGER,b_platform TEXT,b_user_id INTEGER
		);
		CREATE TABLE tg_business_inboxes (
			tg_user_id INTEGER,max_chat_id INTEGER,max_owner_id INTEGER
		);
		INSERT INTO max_mirror VALUES (-103,-203,10),(-109,-209,20);
		INSERT INTO vk_accounts VALUES (1,12345);
		INSERT INTO vk_endpoints VALUES (1,1,'community_wall','VK community');
		INSERT INTO vk_bindings VALUES (1,10,'tg',-102,1);
		INSERT INTO dm_contacts VALUES (1,10,'tg',10,'max',20);
		INSERT INTO tg_business_inboxes VALUES (10,-203,20);
	`)
	if err != nil {
		t.Fatal(err)
	}
	_ = addonDB.Close()

	info := (&server{store: newMemStore()}).slotsInfo(user{ID: 10, Platform: "tg"})
	if got := info["used"].(int); got != 6 {
		t.Fatalf("used slots = %d, want 6", got)
	}
	items := info["items"].([]slotUsageItem)
	if len(items) != 6 {
		t.Fatalf("slot items = %d, want 6", len(items))
	}
	for i, item := range items {
		if item.Kind == "" || item.Label == "" || item.Detail == "" {
			t.Fatalf("item %d is incomplete: %#v", i, item)
		}
	}
	breakdown := info["breakdown"].(map[string]int)
	for _, category := range []string{"groups", "channels", "mirrors", "vk", "direct_messages", "business_inboxes"} {
		if breakdown[category] != 1 {
			t.Errorf("%s slots = %d, want 1", category, breakdown[category])
		}
	}
}
