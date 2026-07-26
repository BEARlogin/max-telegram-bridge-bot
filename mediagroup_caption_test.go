package main

import (
	"context"
	"errors"
	"testing"

	maxschemes "github.com/max-messenger/max-bot-api-client-go/schemes"
)

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
