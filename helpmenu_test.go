package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStartMenuLinksPagedAndFullHelp(t *testing.T) {
	helpPath := filepath.Join(t.TempDir(), "help.html")
	if err := os.WriteFile(helpPath, []byte("Полная инструкция"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HELP_FILE", helpPath)
	b := &Bridge{extraHelpPages: []extraHelpPage{{ID: "dm", Button: "Личный мост", Text: "Описание"}}}

	_, kb := b.tgStartMenu()
	var callbacks []string
	for _, row := range kb.Rows {
		for _, button := range row {
			callbacks = append(callbacks, button.CallbackData)
		}
	}
	joined := strings.Join(callbacks, " ")
	if !strings.Contains(joined, "help:more") || !strings.Contains(joined, "help:full") {
		t.Fatalf("callbacks = %q", joined)
	}
}

func TestExtraHelpMenuUsesSeparatePages(t *testing.T) {
	b := &Bridge{extraHelpPages: []extraHelpPage{
		{ID: "dm", Button: "Личный мост", Text: "Описание DM"},
		{ID: "personal", Button: "Личные сообщения", Text: "Описание инбокса"},
	}}
	text, kb := b.extraHelpMenu()
	if !strings.Contains(text, "Дополнительные возможности") {
		t.Fatalf("menu text = %q", text)
	}
	if len(kb.Rows) != 3 || kb.Rows[0][0].CallbackData != "help:more:dm" || kb.Rows[1][0].CallbackData != "help:more:personal" {
		t.Fatalf("menu rows = %+v", kb.Rows)
	}
	if page, ok := b.extraHelpPage("personal"); !ok || page != "Описание инбокса" {
		t.Fatalf("page = %q, ok=%v", page, ok)
	}
}

func TestExtraHelpMenuFallsBackToLegacyText(t *testing.T) {
	b := &Bridge{extraHelp: "Старый раздел"}
	text, kb := b.extraHelpMenu()
	if text != "Старый раздел" || len(kb.Rows) != 1 || kb.Rows[0][0].CallbackData != "help:home" {
		t.Fatalf("text=%q rows=%+v", text, kb.Rows)
	}
}
