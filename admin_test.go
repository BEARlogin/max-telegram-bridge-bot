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

func TestIsTgAnonymousAdmin(t *testing.T) {
	chat := ChatInfo{ID: -100123, Type: "supergroup"}
	tests := []struct {
		name string
		msg  *TGMessage
		want bool
	}{
		{"nil", nil, false},
		{"no sender chat", &TGMessage{Chat: chat}, false},
		{"different sender chat", &TGMessage{Chat: chat, SenderChat: &ChatInfo{ID: -999}}, false},
		{"same sender chat (anonymous)", &TGMessage{Chat: chat, SenderChat: &ChatInfo{ID: chat.ID}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTgAnonymousAdmin(tt.msg); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
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

func TestIsMaxUserAdminOrOwnerFallsBackToChatOwner(t *testing.T) {
	admins := []maxschemes.ChatMember{{UserId: 200, IsAdmin: true}}
	if !isMaxUserAdminOrOwner(admins, 100, 100) {
		t.Fatal("private chat owner omitted from admins must still be accepted")
	}
	if !isMaxUserAdminOrOwner(admins, 100, 200) {
		t.Fatal("listed admin must be accepted")
	}
	if isMaxUserAdminOrOwner(admins, 100, 300) {
		t.Fatal("ordinary member must not be accepted")
	}
	if isMaxUserAdminOrOwner(nil, 0, 0) {
		t.Fatal("zero user must not be accepted")
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

func TestParseModCommand(t *testing.T) {
	for _, tc := range []struct {
		text string
		cmd  string
		arg  string
		ok   bool
	}{
		{text: "/ban", cmd: "ban", ok: true},
		{text: "/mute 30", cmd: "mute", arg: "30", ok: true},
		{text: "/mute@MaxTelegramBridgeBot @user 60", cmd: "mute", arg: "@user 60", ok: true},
		{text: "/unban 123", cmd: "unban", arg: "123", ok: true},
		{text: "/unmute @user", cmd: "unmute", arg: "@user", ok: true},
		{text: "/antispam on", ok: false},
		{text: "mute 30", ok: false},
	} {
		cmd, arg, ok := parseModCommand(tc.text)
		if cmd != tc.cmd || arg != tc.arg || ok != tc.ok {
			t.Errorf("parseModCommand(%q)=(%q,%q,%v), want (%q,%q,%v)",
				tc.text, cmd, arg, ok, tc.cmd, tc.arg, tc.ok)
		}
	}
}
