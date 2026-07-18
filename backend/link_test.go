package main

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestMergeLinkedEntitlements(t *testing.T) {
	oldPath := addonDBPath
	addonDBPath = filepath.Join(t.TempDir(), "addon.db")
	t.Cleanup(func() { addonDBPath = oldPath })

	db, err := sql.Open("sqlite", addonDBPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE entitlements (
		user_id INTEGER PRIMARY KEY, credits INTEGER NOT NULL DEFAULT 0,
		welcome_granted INTEGER NOT NULL DEFAULT 0, updated_at INTEGER NOT NULL)`)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = db.Exec(`INSERT INTO entitlements VALUES (10,1000,0,0),(20,9,1,0)`)
	_ = db.Close()

	if err := mergeLinkedEntitlements(10, 20); err != nil {
		t.Fatal(err)
	}
	if err := mergeLinkedEntitlements(10, 20); err != nil {
		t.Fatalf("merge must be idempotent: %v", err)
	}
	db, _ = sql.Open("sqlite", addonDBPath)
	defer db.Close()
	var credits, welcome int
	if err := db.QueryRow(`SELECT credits,welcome_granted FROM entitlements WHERE user_id=20`).Scan(&credits, &welcome); err != nil {
		t.Fatal(err)
	}
	if credits != 1009 || welcome != 1 {
		t.Fatalf("merged entitlement = (%d,%d), want (1009,1)", credits, welcome)
	}
	var oldRows int
	_ = db.QueryRow(`SELECT COUNT(*) FROM entitlements WHERE user_id=10`).Scan(&oldRows)
	if oldRows != 0 {
		t.Fatalf("source entitlement row remains after merge")
	}
}
