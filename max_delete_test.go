package main

import (
	"context"
	"errors"
	"testing"
	"time"

	maxbot "github.com/max-messenger/max-bot-api-client-go"
	maxschemes "github.com/max-messenger/max-bot-api-client-go/schemes"
)

type maxDeleteSender struct {
	TGSender
	attempts []MessageRoute
	fail     map[int]error
}

func (s *maxDeleteSender) DeleteMessage(_ context.Context, chatID int64, msgID int) error {
	s.attempts = append(s.attempts, MessageRoute{TgChatID: chatID, TgMsgID: msgID})
	return s.fail[msgID]
}

func TestHandleMaxMessageRemovedDeletesEveryEligibleMapping(t *testing.T) {
	repo := testRepo(t)
	const maxChat int64 = 900
	for _, cp := range []struct {
		tg        int64
		direction string
	}{{303, "both"}, {304, "tg>max"}} {
		if _, err := repo.db.Exec(`INSERT INTO crossposts
			(tg_chat_id,max_chat_id,direction,created_at,sync_edits) VALUES (?,?,?,?,1)`, cp.tg, maxChat, cp.direction, 1); err != nil {
			t.Fatal(err)
		}
	}

	const mid = "album-mid"
	for _, route := range []struct {
		chat   int64
		msg    int
		origin string
	}{{101, 10, "max"}, {101, 11, "max"}, {202, 20, "max"}, {303, 30, "max"},
		{304, 40, "max"}, {305, 50, "tg"}} {
		repo.SaveMsgOrigin(route.chat, route.msg, maxChat, mid, 0, route.origin)
	}
	// Same mid in another MAX chat must not be selected.
	repo.SaveMsgOrigin(306, 60, 901, mid, 0, "max")

	sender := &maxDeleteSender{fail: map[int]error{10: errors.New("telegram unavailable")}}
	b := &Bridge{repo: repo, tg: sender}
	b.handleMaxMessageRemoved(context.Background(), &maxschemes.MessageRemovedUpdate{ChatID: maxChat, MessageId: mid})

	want := []MessageRoute{{TgChatID: 101, TgMsgID: 10}, {TgChatID: 101, TgMsgID: 11},
		{TgChatID: 202, TgMsgID: 20}, {TgChatID: 303, TgMsgID: 30}}
	if len(sender.attempts) != len(want) {
		t.Fatalf("delete attempts=%v, want %v", sender.attempts, want)
	}
	for i := range want {
		if sender.attempts[i] != want[i] {
			t.Fatalf("delete attempt %d=%v, want %v", i, sender.attempts[i], want[i])
		}
	}
}

func TestHandleMaxMessageRemovedHonorsDisabledCrosspostSync(t *testing.T) {
	repo := testRepo(t)
	const maxChat, tgChat = int64(700), int64(701)
	if _, err := repo.db.Exec(`INSERT INTO crossposts
		(tg_chat_id,max_chat_id,direction,created_at,sync_edits) VALUES (?,?,'both',1,0)`, tgChat, maxChat); err != nil {
		t.Fatal(err)
	}
	repo.SaveMsgOrigin(tgChat, 70, maxChat, "mid", 0, "max")
	sender := &maxDeleteSender{}
	(&Bridge{repo: repo, tg: sender}).handleMaxMessageRemoved(context.Background(),
		&maxschemes.MessageRemovedUpdate{ChatID: maxChat, MessageId: "mid"})
	if len(sender.attempts) != 0 {
		t.Fatalf("delete attempts=%v, want none", sender.attempts)
	}
}

func TestHandleMaxMessageRemovedDeduplicatesWithoutCreatedMIDCollision(t *testing.T) {
	repo := testRepo(t)
	const maxChat, tgChat = int64(800), int64(801)
	repo.SaveMsgOrigin(tgChat, 81, maxChat, "shared-mid", 0, "max")
	sender := &maxDeleteSender{}
	b := &Bridge{
		cfg:         Config{MaxTokenOld: "old"},
		repo:        repo,
		tg:          sender,
		maxApiOld:   &maxbot.Api{},
		maxSeenMid:  map[string]int64{"message_created:shared-mid": time.Now().Unix()},
		maxBotCache: make(map[int64]string),
	}
	upd := &maxschemes.MessageRemovedUpdate{ChatID: maxChat, MessageId: "shared-mid"}
	b.handleMaxMessageRemoved(context.Background(), upd)
	b.handleMaxMessageRemoved(context.Background(), upd)
	if len(sender.attempts) != 1 {
		t.Fatalf("delete attempts=%v, want one", sender.attempts)
	}
}
