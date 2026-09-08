package main

import "testing"

func TestParseTgIgnoredBotIDs(t *testing.T) {
	got, err := parseTgIgnoredBotIDs(" 8818355235,8544076231,,8818355235 ")
	if err != nil {
		t.Fatalf("parseTgIgnoredBotIDs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d IDs, want 2", len(got))
	}
	for _, id := range []int64{8818355235, 8544076231} {
		if _, ok := got[id]; !ok {
			t.Errorf("ID %d missing from parsed set", id)
		}
	}
}

func TestParseTgIgnoredBotIDsRejectsInvalidIDs(t *testing.T) {
	for _, value := range []string{"bot", "0", "-1", "8818355235,broken"} {
		t.Run(value, func(t *testing.T) {
			if _, err := parseTgIgnoredBotIDs(value); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestShouldIgnoreTgUpdateCoversMessageVariants(t *testing.T) {
	const ignoredID int64 = 8818355235
	b := &Bridge{cfg: Config{TgIgnoredBotIDs: map[int64]struct{}{ignoredID: {}}}}
	ignoredMessage := func() *TGMessage {
		return &TGMessage{MessageID: 7, Chat: ChatInfo{ID: -1001}, From: &UserInfo{ID: ignoredID, IsBot: true}}
	}

	tests := []struct {
		name   string
		update TGUpdate
	}{
		{"message", TGUpdate{Message: ignoredMessage()}},
		{"edited message", TGUpdate{EditedMessage: ignoredMessage()}},
		{"channel post", TGUpdate{ChannelPost: ignoredMessage()}},
		{"edited channel post", TGUpdate{EditedChannelPost: ignoredMessage()}},
		{"business message", TGUpdate{BusinessMessage: ignoredMessage()}},
		{"edited business message", TGUpdate{EditedBusinessMessage: ignoredMessage()}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !b.shouldIgnoreTgUpdate(tt.update) {
				t.Fatal("configured bot-authored message was not ignored")
			}
		})
	}
}

func TestShouldIgnoreTgUpdateDoesNotBlanketIgnoreBotsOrChannelPosts(t *testing.T) {
	const ignoredID int64 = 8818355235
	b := &Bridge{cfg: Config{TgIgnoredBotIDs: map[int64]struct{}{ignoredID: {}}}}

	tests := []struct {
		name   string
		update TGUpdate
	}{
		{"different bot", TGUpdate{Message: &TGMessage{From: &UserInfo{ID: 99, IsBot: true}}}},
		{"human with configured ID", TGUpdate{Message: &TGMessage{From: &UserInfo{ID: ignoredID}}}},
		{"ordinary channel post", TGUpdate{ChannelPost: &TGMessage{SenderChat: &ChatInfo{ID: -1001, Type: "channel"}}}},
		{"ordinary edited channel post", TGUpdate{EditedChannelPost: &TGMessage{From: &UserInfo{ID: 42}}}},
		{"forward from configured bot", TGUpdate{Message: &TGMessage{From: &UserInfo{ID: 42}, BotForward: "ignored_bot"}}},
		{"inline-bot result", TGUpdate{Message: &TGMessage{From: &UserInfo{ID: 42}, ViaInlineBot: "ignored_bot"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if b.shouldIgnoreTgUpdate(tt.update) {
				t.Fatal("unrelated Telegram message was ignored")
			}
		})
	}
}
