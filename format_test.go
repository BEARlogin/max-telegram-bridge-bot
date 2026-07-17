package main

import (
	"testing"

	maxschemes "github.com/max-messenger/max-bot-api-client-go/schemes"
)

func TestTgName(t *testing.T) {
	tests := []struct {
		name     string
		msg      *TGMessage
		expected string
	}{
		{
			name: "first name only",
			msg: &TGMessage{
				From: &UserInfo{FirstName: "Ivan"},
			},
			expected: "Ivan",
		},
		{
			name: "first and last name",
			msg: &TGMessage{
				From: &UserInfo{FirstName: "Ivan", LastName: "Petrov"},
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
	msg := &TGMessage{
		Text: "hello world",
		From: &UserInfo{FirstName: "Anna"},
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
			got := formatTgCaption(msg, tt.prefix, false)
			if got != tt.expected {
				t.Errorf("formatTgCaption() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestFormatTgCaption_UsesCaption(t *testing.T) {
	msg := &TGMessage{
		Text:    "",
		Caption: "photo caption",
		From:    &UserInfo{FirstName: "Bob"},
	}

	got := formatTgCaption(msg, false, false)
	expected := "Bob: photo caption"
	if got != expected {
		t.Errorf("formatTgCaption() = %q, want %q", got, expected)
	}
}

func TestFormatTgMessage(t *testing.T) {
	tests := []struct {
		name     string
		msg      *TGMessage
		prefix   bool
		expected string
	}{
		{
			name: "text with prefix",
			msg: &TGMessage{
				Text: "edited text",
				From: &UserInfo{FirstName: "Ivan"},
			},
			prefix:   true,
			expected: "[TG] Ivan: edited text",
		},
		{
			name: "text without prefix",
			msg: &TGMessage{
				Text: "edited text",
				From: &UserInfo{FirstName: "Ivan"},
			},
			prefix:   false,
			expected: "Ivan: edited text",
		},
		{
			name: "empty text returns empty",
			msg: &TGMessage{
				Text: "",
				From: &UserInfo{FirstName: "Ivan"},
			},
			prefix:   true,
			expected: "",
		},
		{
			name: "caption fallback",
			msg: &TGMessage{
				Text:    "",
				Caption: "cap",
				From:    &UserInfo{FirstName: "Ivan"},
			},
			prefix:   false,
			expected: "Ivan: cap",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatTgMessage(tt.msg, tt.prefix, false)
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
			got := formatMaxCaption(upd, tt.prefix, false)
			if got != tt.expected {
				t.Errorf("formatMaxCaption() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestFormatTgCrosspostCaption(t *testing.T) {
	tests := []struct {
		name     string
		msg      *TGMessage
		expected string
	}{
		{
			name:     "text",
			msg:      &TGMessage{Text: "Новый пост"},
			expected: "Новый пост",
		},
		{
			name:     "caption fallback",
			msg:      &TGMessage{Text: "", Caption: "фото"},
			expected: "фото",
		},
		{
			name:     "empty",
			msg:      &TGMessage{Text: ""},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatTgCrosspostCaption(tt.msg)
			if got != tt.expected {
				t.Errorf("formatTgCrosspostCaption() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestFormatMaxCrosspostCaption(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		expected string
	}{
		{"with text", "Новость дня", "Новость дня"},
		{"empty text", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upd := &maxschemes.MessageCreatedUpdate{
				Message: maxschemes.Message{
					Body: maxschemes.MessageBody{Text: tt.text},
				},
			}
			got := formatMaxCrosspostCaption(upd)
			if got != tt.expected {
				t.Errorf("formatMaxCrosspostCaption() = %q, want %q", got, tt.expected)
			}
		})
	}
}
