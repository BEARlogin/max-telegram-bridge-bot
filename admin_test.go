package main

import (
	"testing"

	maxschemes "github.com/max-messenger/max-bot-api-client-go/schemes"
)

func TestIsTgGroup(t *testing.T) {
	tests := []struct {
		chatType string
		want     bool
	}{
		{"group", true},
		{"supergroup", true},
		{"private", false},
		{"channel", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.chatType, func(t *testing.T) {
			if got := isTgGroup(tt.chatType); got != tt.want {
				t.Errorf("isTgGroup(%q) = %v, want %v", tt.chatType, got, tt.want)
			}
		})
	}
}

func TestIsTgAdmin(t *testing.T) {
	tests := []struct {
		status string
		want   bool
	}{
		{"creator", true},
		{"administrator", true},
		{"member", false},
		{"restricted", false},
		{"left", false},
		{"kicked", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			if got := isTgAdmin(tt.status); got != tt.want {
				t.Errorf("isTgAdmin(%q) = %v, want %v", tt.status, got, tt.want)
			}
		})
	}
}

func TestIsMaxGroup(t *testing.T) {
	tests := []struct {
		name     string
		chatType maxschemes.ChatType
		want     bool
	}{
		{"chat", maxschemes.CHAT, true},
		{"channel", maxschemes.CHANNEL, true},
		{"dialog", maxschemes.DIALOG, false},
		{"empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isMaxGroup(tt.chatType); got != tt.want {
				t.Errorf("isMaxGroup(%q) = %v, want %v", tt.chatType, got, tt.want)
			}
		})
	}
}

func TestIsMaxUserAdmin(t *testing.T) {
	admins := []maxschemes.ChatMember{
		{UserId: 100, Name: "Owner", IsOwner: true, IsAdmin: true},
		{UserId: 200, Name: "Admin", IsAdmin: true},
		{UserId: 300, Name: "Bot", IsBot: true, IsAdmin: true},
	}

	tests := []struct {
		name   string
		userID int64
		want   bool
	}{
		{"owner is admin", 100, true},
		{"admin is admin", 200, true},
		{"bot admin", 300, true},
		{"non-admin user", 999, false},
		{"zero id", 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isMaxUserAdmin(admins, tt.userID); got != tt.want {
				t.Errorf("isMaxUserAdmin(admins, %d) = %v, want %v", tt.userID, got, tt.want)
			}
		})
	}
}

func TestIsMaxUserAdmin_EmptyList(t *testing.T) {
	if isMaxUserAdmin(nil, 100) {
		t.Error("isMaxUserAdmin(nil, 100) = true, want false")
	}
	if isMaxUserAdmin([]maxschemes.ChatMember{}, 100) {
		t.Error("isMaxUserAdmin([], 100) = true, want false")
	}
}

func TestIsTgChannel(t *testing.T) {
	tests := []struct {
		chatType string
		want     bool
	}{
		{"channel", true},
		{"group", false},
		{"supergroup", false},
		{"private", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.chatType, func(t *testing.T) {
			if got := isTgChannel(tt.chatType); got != tt.want {
				t.Errorf("isTgChannel(%q) = %v, want %v", tt.chatType, got, tt.want)
			}
		})
	}
}
