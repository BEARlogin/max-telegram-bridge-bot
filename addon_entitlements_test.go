//go:build addon

package main

import (
	"database/sql"
	"path/filepath"
	"slices"
	"testing"
)

func TestEntitlementAccountIDsIncludesLinkedWorkspaceBilling(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "comments.db")
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE account_links (tg_id INTEGER, max_id INTEGER)`,
		`CREATE TABLE workspace_billing_aliases (user_id INTEGER, billing_account_id INTEGER, workspace_id INTEGER)`,
		`INSERT INTO account_links(tg_id,max_id) VALUES (1494227608,228006147)`,
		`INSERT INTO workspace_billing_aliases(user_id,billing_account_id,workspace_id)
		 VALUES (1494227608,7000000000000004863,4863),
		        (228006147,7000000000000004864,4864)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	db.Close()

	resolveWorkspace := func(userID int64) (int64, int64) {
		switch userID {
		case 1494227608:
			return 7000000000000004863, 0
		case 228006147:
			return 7000000000000004864, 0
		default:
			return userID, 0
		}
	}
	ids := entitlementAccountIDs(dbPath, 228006147, resolveWorkspace)
	for _, want := range []int64{228006147, 1494227608, 7000000000000004863, 7000000000000004864} {
		if !slices.Contains(ids, want) {
			t.Fatalf("entitlement IDs %v do not contain %d", ids, want)
		}
	}
}
