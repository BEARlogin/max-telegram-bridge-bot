package main

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestLoadVKAdminStatsSeparatesAccountsBindingsAndQueue(t *testing.T) {
	dir := t.TempDir()
	addonPath := filepath.Join(dir, "addon.db")
	bridgePath := filepath.Join(dir, "bridge.db")
	adb, err := sql.Open("sqlite", addonPath)
	if err != nil {
		t.Fatal(err)
	}
	defer adb.Close()
	for _, q := range []string{
		`CREATE TABLE vk_accounts (id INTEGER PRIMARY KEY,owner_id INTEGER,community_id INTEGER,enabled INTEGER,created_at INTEGER)`,
		`CREATE TABLE vk_endpoints (id INTEGER PRIMARY KEY,account_id INTEGER,kind TEXT,title TEXT,enabled INTEGER)`,
		`CREATE TABLE vk_bindings (id INTEGER PRIMARY KEY,owner_id INTEGER,source_platform TEXT,source_chat_id INTEGER,endpoint_id INTEGER,direction TEXT,paused INTEGER,created_at INTEGER)`,
		`CREATE TABLE vk_message_map (binding_id INTEGER,direction TEXT,created_at INTEGER)`,
		`CREATE TABLE vk_delivery_queue (id INTEGER PRIMARY KEY,binding_id INTEGER,attempts INTEGER,updated_at INTEGER,last_error TEXT)`,
		`INSERT INTO vk_accounts VALUES (1,42,100,1,1000),(2,42,200,1,1100)`,
		`INSERT INTO vk_endpoints VALUES (1,1,'community_wall','VK 100',1)`,
		`INSERT INTO vk_bindings VALUES (1,42,'tg',-1001,1,'both',0,1200)`,
		`INSERT INTO vk_message_map VALUES (1,'source>vk',1300),(1,'vk>source',1400)`,
		`INSERT INTO vk_delivery_queue VALUES (1,1,4,1500,'authorization failed')`,
	} {
		if _, err = adb.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	bdb, err := sql.Open("sqlite", bridgePath)
	if err != nil {
		t.Fatal(err)
	}
	defer bdb.Close()
	for _, q := range []string{
		`CREATE TABLE users (user_id INTEGER,username TEXT,first_name TEXT)`,
		`CREATE TABLE bot_chats (platform TEXT,chat_id INTEGER,title TEXT)`,
		`INSERT INTO users VALUES (42,'owner','Owner')`,
		`INSERT INTO bot_chats VALUES ('tg',-1001,'Source channel')`,
	} {
		if _, err = bdb.Exec(q); err != nil {
			t.Fatal(err)
		}
	}

	got := loadVKAdminStats(addonPath, bridgePath)
	if !got.Available || got.AccountsTotal != 2 || got.OwnersTotal != 1 {
		t.Fatalf("accounts: %+v", got)
	}
	if got.BindingsTotal != 1 || got.ActiveBindings != 1 || got.AccountsWithoutBinding != 1 {
		t.Fatalf("bindings: %+v", got)
	}
	if got.DeliveriesToVK != 1 || got.DeliveriesFromVK != 1 || got.QueuePending != 1 {
		t.Fatalf("delivery: %+v", got)
	}
	if len(got.Bindings) != 1 || got.Bindings[0].OwnerUsername != "owner" ||
		got.Bindings[0].SourceTitle != "Source channel" || got.Bindings[0].QueueAttempts != 4 {
		t.Fatalf("binding detail: %+v", got.Bindings)
	}
}
