package main

import (
	"testing"

	maxschemes "github.com/max-messenger/max-bot-api-client-go/schemes"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestTgName(t *testing.T) {
	tests := []struct {
		name     string
		msg      *tgbotapi.Message
		expected string
	}{
		{
			name: "first name only",
			msg: &tgbotapi.Message{
				From: &tgbotapi.User{FirstName: "Ivan"},
			},
			expected: "Ivan",
		},
		{
			name: "first and last name",
			msg: &tgbotapi.Message{
				From: &tgbotapi.User{FirstName: "Ivan", LastName: "Petrov"},
			},
			expected: "Ivan Petrov",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tgName(tt.msg)
			if got != tt.expected {
				t.Errorf("tgName() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestFormatTgCaption(t *testing.T) {
	msg := &tgbotapi.Message{
		Text: "hello world",
		From: &tgbotapi.User{FirstName: "Anna"},
	}

	tests := []struct {
		name     string
		prefix   bool
		expected string
	}{
		{"with prefix", true, "[TG] Anna: hello world"},
		{"without prefix", false, "Anna: hello world"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatTgCaption(msg, tt.prefix)
			if got != tt.expected {
				t.Errorf("formatTgCaption() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestFormatTgCaption_UsesCaption(t *testing.T) {
	msg := &tgbotapi.Message{
		Text:    "",
		Caption: "photo caption",
		From:    &tgbotapi.User{FirstName: "Bob"},
	}

	got := formatTgCaption(msg, false)
	expected := "Bob: photo caption"
	if got != expected {
		t.Errorf("formatTgCaption() = %q, want %q", got, expected)
	}
}

func TestFormatTgMessage(t *testing.T) {
	tests := []struct {
		name     string
		msg      *tgbotapi.Message
		prefix   bool
		expected string
	}{
		{
			name: "text with prefix",
			msg: &tgbotapi.Message{
				Text: "edited text",
				From: &tgbotapi.User{FirstName: "Ivan"},
			},
			prefix:   true,
			expected: "[TG] Ivan: edited text",
		},
		{
			name: "text without prefix",
			msg: &tgbotapi.Message{
				Text: "edited text",
				From: &tgbotapi.User{FirstName: "Ivan"},
			},
			prefix:   false,
			expected: "Ivan: edited text",
		},
		{
			name: "empty text returns empty",
			msg: &tgbotapi.Message{
				Text: "",
				From: &tgbotapi.User{FirstName: "Ivan"},
			},
			prefix:   true,
			expected: "",
		},
		{
			name: "caption fallback",
			msg: &tgbotapi.Message{
				Text:    "",
				Caption: "cap",
				From:    &tgbotapi.User{FirstName: "Ivan"},
			},
			prefix:   false,
			expected: "Ivan: cap",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatTgMessage(tt.msg, tt.prefix)
			if got != tt.expected {
				t.Errorf("formatTgMessage() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestMaxName(t *testing.T) {
	tests := []struct {
		name     string
		upd      *maxschemes.MessageCreatedUpdate
		expected string
	}{
		{
			name: "has name",
			upd: &maxschemes.MessageCreatedUpdate{
				Message: maxschemes.Message{
					Sender: maxschemes.User{Name: "Алексей"},
				},
			},
			expected: "Алексей",
		},
		{
			name: "fallback to username",
			upd: &maxschemes.MessageCreatedUpdate{
				Message: maxschemes.Message{
					Sender: maxschemes.User{Name: "", Username: "alex42"},
				},
			},
			expected: "alex42",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := maxName(tt.upd)
			if got != tt.expected {
				t.Errorf("maxName() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestFormatMaxCaption(t *testing.T) {
	upd := &maxschemes.MessageCreatedUpdate{
		Message: maxschemes.Message{
			Sender: maxschemes.User{Name: "Вася"},
			Body:   maxschemes.MessageBody{Text: "привет"},
		},
	}

	tests := []struct {
		name     string
		prefix   bool
		expected string
	}{
		{"with prefix", true, "[MAX] Вася: привет"},
		{"without prefix", false, "Вася: привет"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatMaxCaption(upd, tt.prefix)
			if got != tt.expected {
				t.Errorf("formatMaxCaption() = %q, want %q", got, tt.expected)
			}
		})
	}
}
