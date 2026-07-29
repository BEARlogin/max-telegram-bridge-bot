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
	b := &Bridge{extraHelpPages: []extraHelpPage{{ID: "dm", Button: "Один на один", Text: "Описание"}}}

	_, kb := b.tgStartMenu()
	var callbacks []string
	var labels []string
	var knowledgeURL string
	for _, row := range kb.Rows {
		for _, button := range row {
			callbacks = append(callbacks, button.CallbackData)
			labels = append(labels, button.Text)
			if button.Text == "📚 База знаний" {
				knowledgeURL = button.URL
			}
		}
	}
	joined := strings.Join(callbacks, " ")
	if !strings.Contains(joined, "help:pro") || !strings.Contains(joined, "help:more") || !strings.Contains(joined, "help:full") {
		t.Fatalf("callbacks = %q", joined)
	}
	if !strings.Contains(strings.Join(labels, " "), "Оформить PRO") {
		t.Fatalf("PRO CTA missing, labels = %q", labels)
	}
	if !strings.Contains(strings.Join(labels, " "), "Все шаги одной инструкцией") {
		t.Fatalf("labels = %q", labels)
	}
	if knowledgeURL != knowledgeBaseURL {
		t.Fatalf("knowledge base URL = %q, want %q", knowledgeURL, knowledgeBaseURL)
	}
}

func TestTelegramStartParamSupportsAdvertisingDeepLinks(t *testing.T) {
	for _, tc := range []struct {
		text    string
		payload string
		ok      bool
	}{
		{text: "/start", ok: true},
		{text: "/start 42", payload: "42", ok: true},
		{text: "/start@MaxTelegramBridgeBot 77", payload: "77", ok: true},
		{text: "/help", ok: false},
		{text: "start 42", ok: false},
	} {
		payload, ok := telegramStartParam(tc.text)
		if payload != tc.payload || ok != tc.ok {
			t.Fatalf("telegramStartParam(%q)=(%q,%v), want (%q,%v)",
				tc.text, payload, ok, tc.payload, tc.ok)
		}
	}
}

func TestExtraHelpMenuUsesSeparatePages(t *testing.T) {
	b := &Bridge{extraHelpPages: []extraHelpPage{
		{ID: "dm", Button: "Один на один", Text: "Описание DM"},
		{ID: "personal", Button: "Входящие Telegram", Text: "Описание инбокса"},
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
