package main

import (
	"context"
	"testing"
	"time"
)

func TestTGUpdateBackpressureDoesNotDropUpdate(t *testing.T) {
	s := &tgBotSender{updates: make(chan TGUpdate, 1)}
	s.updates <- TGUpdate{Message: &TGMessage{MessageID: 1}}

	done := make(chan struct{})
	go func() {
		s.enqueueUpdate(context.Background(), TGUpdate{Message: &TGMessage{MessageID: 2}})
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("enqueue returned while the channel was full: update was not backpressured")
	case <-time.After(25 * time.Millisecond):
	}

	if got := (<-s.updates).Message.MessageID; got != 1 {
		t.Fatalf("first update=%d want=1", got)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("enqueue did not resume after capacity became available")
	}
	if got := (<-s.updates).Message.MessageID; got != 2 {
		t.Fatalf("second update=%d want=2", got)
	}
}

func TestTGUpdateBackpressureStopsOnCanceledContext(t *testing.T) {
	s := &tgBotSender{updates: make(chan TGUpdate, 1)}
	s.updates <- TGUpdate{Message: &TGMessage{MessageID: 1}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	s.enqueueUpdate(ctx, TGUpdate{Message: &TGMessage{MessageID: 2}})
	if len(s.updates) != 1 {
		t.Fatalf("queued updates=%d want=1", len(s.updates))
	}
}
