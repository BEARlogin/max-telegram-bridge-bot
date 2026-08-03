package main

import (
	"context"
	"errors"
	"testing"
	"time"

	maxschemes "github.com/max-messenger/max-bot-api-client-go/schemes"
)

func TestCrosspostMediaGroupBuffersAreIsolatedPerDestination(t *testing.T) {
	b := &Bridge{mgBuffers: make(map[string]*mediaGroupBuffer)}
	msg := &TGMessage{Chat: ChatInfo{ID: -1001}}

	b.bufferMediaGroup(context.Background(), "album", mediaGroupItem{
		msg: msg, crosspost: true, maxChatID: -2001, caption: "первый",
	})
	b.bufferMediaGroup(context.Background(), "album", mediaGroupItem{
		msg: msg, crosspost: true, maxChatID: -2002, caption: "второй",
	})

	b.mgMu.Lock()
	defer b.mgMu.Unlock()
	if len(b.mgBuffers) != 2 {
		t.Fatalf("crosspost album buffers=%d want 2", len(b.mgBuffers))
	}
	for key, buf := range b.mgBuffers {
		if buf.timer != nil {
			buf.timer.Stop()
		}
		if len(buf.items) != 1 {
			t.Fatalf("buffer %q items=%d want 1", key, len(buf.items))
		}
	}
}

func TestStaleMediaGroupTimerCannotDetachNewerCaption(t *testing.T) {
	b := &Bridge{mgBuffers: make(map[string]*mediaGroupBuffer)}
	b.bufferMediaGroup(context.Background(), "album", mediaGroupItem{})

	b.mgMu.Lock()
	staleGeneration := b.mgBuffers["album"].generation
	b.mgMu.Unlock()

	b.bufferMediaGroup(context.Background(), "album", mediaGroupItem{caption: "подпись"})

	if _, detached, err := b.detachMediaGroup("album", staleGeneration); err != nil {
		t.Fatal(err)
	} else if detached {
		t.Fatal("stale timer detached a buffer containing a newer album item")
	}

	buf, detached, err := b.detachMediaGroup("album", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !detached {
		t.Fatal("current album buffer was not detached")
	}
	if len(buf.items) != 2 {
		t.Fatalf("album items=%d want 2", len(buf.items))
	}
	if buf.items[1].caption != "подпись" {
		t.Fatalf("late caption=%q want %q", buf.items[1].caption, "подпись")
	}
}

func TestBridgeMediaGroupCaptionUsesBoldSenderName(t *testing.T) {
	b := &Bridge{}
	msg := &TGMessage{
		Chat:       ChatInfo{ID: -1001, Type: "supergroup"},
		SenderChat: &ChatInfo{ID: -2002, Type: "channel", Title: "Александр"},
	}
	caption, formatted := b.formatTgMediaGroupCaption(context.Background(), []mediaGroupItem{
		{msg: msg},
		{msg: msg, caption: "В том числе можно фото пересылать."},
	}, -3003, false)
	if !formatted || caption != "<b>Александр</b>: В том числе можно фото пересылать." {
		t.Fatalf("caption=%q formatted=%v", caption, formatted)
	}
}

func TestCrosspostMediaGroupCaptionDoesNotAddSender(t *testing.T) {
	b := &Bridge{}
	msg := &TGMessage{Chat: ChatInfo{ID: -1001}, SenderChat: &ChatInfo{Title: "Александр"}}
	caption, formatted := b.formatTgMediaGroupCaption(context.Background(), []mediaGroupItem{
		{msg: msg, caption: "<b>Готовый пост</b>"},
	}, -3003, true)
	if !formatted || caption != "<b>Готовый пост</b>" {
		t.Fatalf("caption=%q formatted=%v", caption, formatted)
	}
}

func TestMaxAlbumCaptionMissing(t *testing.T) {
	message := func(text string) *maxschemes.Message {
		return &maxschemes.Message{Body: maxschemes.MessageBody{Text: text}}
	}

	tests := []struct {
		name      string
		expected  string
		sent      *maxschemes.Message
		persisted *maxschemes.Message
		fetchErr  error
		want      bool
	}{
		{"empty caption", "", message(""), message(""), nil, false},
		{"persisted", "текст", message("текст"), message("текст"), nil, false},
		{"dropped after send", "текст", message("текст"), message(""), nil, true},
		{"response already empty and fetch failed", "текст", message(""), nil, errors.New("temporary"), true},
		{"response has text and fetch failed", "текст", message("текст"), nil, errors.New("temporary"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := maxAlbumCaptionMissing(tt.expected, tt.sent, tt.persisted, tt.fetchErr); got != tt.want {
				t.Fatalf("maxAlbumCaptionMissing()=%v want %v", got, tt.want)
			}
		})
	}
}

func TestVerifyCaptionAfterDelaysRepairsCaptionThatDisappearsLater(t *testing.T) {
	observations := []string{"подпись", ""}
	fetches := 0
	repairs := 0

	repaired, err := verifyCaptionAfterDelays(
		context.Background(),
		[]time.Duration{0, 0},
		func(context.Context) (string, error) {
			text := observations[fetches]
			fetches++
			return text, nil
		},
		func(context.Context) error {
			repairs++
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !repaired || repairs != 1 {
		t.Fatalf("repaired=%v repairs=%d want true,1", repaired, repairs)
	}
}
