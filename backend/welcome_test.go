package main

import (
	"database/sql"
	"testing"
)

func TestWriteGroupWelcomeIsolatedAndDisable(t *testing.T) {
	db, err := sql.Open("sqlite", "file::memory:?cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE antispam_config (
		platform TEXT NOT NULL,
		chat_id INTEGER NOT NULL,
		welcome_text TEXT NOT NULL DEFAULT '',
		welcome_by INTEGER NOT NULL DEFAULT 0,
		updated_at INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY(platform, chat_id)
	)`); err != nil {
		t.Fatal(err)
	}

	if err := writeGroupWelcomeExec(db, -1001, 42, "Привет, {name}!"); err != nil {
		t.Fatal(err)
	}
	if err := writeGroupWelcomeExec(db, -1002, 43, "Добро пожаловать"); err != nil {
		t.Fatal(err)
	}

	var text string
	var owner int64
	if err := db.QueryRow(`SELECT welcome_text, welcome_by FROM antispam_config
		WHERE platform='tg' AND chat_id=-1001`).Scan(&text, &owner); err != nil {
		t.Fatal(err)
	}
	if text != "Привет, {name}!" || owner != 42 {
		t.Fatalf("unexpected welcome: text=%q owner=%d", text, owner)
	}

	if err := writeGroupWelcomeExec(db, -1001, 42, ""); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT welcome_text, welcome_by FROM antispam_config
		WHERE platform='tg' AND chat_id=-1001`).Scan(&text, &owner); err != nil {
		t.Fatal(err)
	}
	if text != "" || owner != 0 {
		t.Fatalf("welcome not disabled: text=%q owner=%d", text, owner)
	}

	if err := db.QueryRow(`SELECT welcome_text, welcome_by FROM antispam_config
		WHERE platform='tg' AND chat_id=-1002`).Scan(&text, &owner); err != nil {
		t.Fatal(err)
	}
	if text != "Добро пожаловать" || owner != 43 {
		t.Fatalf("other group changed: text=%q owner=%d", text, owner)
	}
}
