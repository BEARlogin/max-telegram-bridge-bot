package main

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type crosspostAccessTGSender struct {
	TGSender
	title        string
	chatErr      error
	memberStatus string
	memberErr    error
	username     string
}

func (s crosspostAccessTGSender) GetChat(context.Context, int64) (string, error) {
	return s.title, s.chatErr
}

func (s crosspostAccessTGSender) GetChatMember(context.Context, int64, int64) (string, error) {
	return s.memberStatus, s.memberErr
}

func (s crosspostAccessTGSender) BotUsername() string { return s.username }

func TestValidateTgCrosspostSource(t *testing.T) {
	for _, tc := range []struct {
		name      string
		sender    crosspostAccessTGSender
		requester int64
		wantErr   error
	}{
		{name: "available admin", sender: crosspostAccessTGSender{title: "Source", memberStatus: "administrator"}, requester: 42},
		{name: "bot has no access", sender: crosspostAccessTGSender{chatErr: errors.New("telegram 403")}, requester: 42, wantErr: errTgCrosspostBotNoAccess},
		{name: "requester is not admin", sender: crosspostAccessTGSender{title: "Source", memberStatus: "member"}, requester: 42, wantErr: errTgCrosspostUserNotAdmin},
		{name: "legacy access check", sender: crosspostAccessTGSender{title: "Source"}, requester: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &Bridge{tg: tc.sender}
			_, err := b.validateTgCrosspostSource(context.Background(), -1001, tc.requester)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err=%v want=%v", err, tc.wantErr)
			}
		})
	}
}

func TestTgCrosspostAccessTextNamesBot(t *testing.T) {
	b := &Bridge{tg: crosspostAccessTGSender{username: "MaxTelegramBridgeBot"}}
	got := b.tgCrosspostAccessText(-1001, errTgCrosspostBotNoAccess)
	if !strings.Contains(got, "@MaxTelegramBridgeBot") || !strings.Contains(got, "администратором") {
		t.Fatalf("access instruction=%q", got)
	}
}
