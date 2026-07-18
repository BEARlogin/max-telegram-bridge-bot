package main

import (
	"testing"

	maxschemes "github.com/max-messenger/max-bot-api-client-go/schemes"
)

func TestMaxCallbackChatID(t *testing.T) {
	tests := []struct {
		name string
		upd  *maxschemes.MessageCallbackUpdate
		want int64
	}{
		{
			name: "dialog uses clicker",
			upd: &maxschemes.MessageCallbackUpdate{
				Callback: maxschemes.Callback{User: maxschemes.User{UserId: 42}},
				Message:  &maxschemes.Message{Recipient: maxschemes.Recipient{ChatType: maxschemes.DIALOG, UserId: 99}},
			},
			want: 42,
		},
		{
			name: "group uses chat",
			upd: &maxschemes.MessageCallbackUpdate{
				Callback: maxschemes.Callback{User: maxschemes.User{UserId: 42}},
				Message:  &maxschemes.Message{Recipient: maxschemes.Recipient{ChatType: maxschemes.CHAT, ChatId: -123}},
			},
			want: -123,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := maxCallbackChatID(tt.upd); got != tt.want {
				t.Fatalf("maxCallbackChatID() = %d, want %d", got, tt.want)
			}
		})
	}
}
