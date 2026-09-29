package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestTGBotSenderPreservesUpdateOrder фиксирует уровень библиотеки: без
// WithNotAsyncHandlers каждое обновление обрабатывается своей горутиной, и порядок
// теряется ещё до моста — очередь моста восстановить его уже не может.
func TestTGBotSenderPreservesUpdateOrder(t *testing.T) {
	const (
		token = "42:test-token"
		total = 100
	)

	var polled atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/getMe"):
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"ok":true,"result":{"id":42,"is_bot":true,"username":"order_bot"}}`)
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			if polled.Swap(true) {
				// Пачка уже отдана. Короткая пауза вместо долгого long-poll:
				// держать соединение до конца теста нельзя — httptest.Server.Close
				// ждёт обработчик, и тест зависал бы на выходе.
				time.Sleep(20 * time.Millisecond)
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"ok":true,"result":[]}`)
				return
			}
			var sb strings.Builder
			sb.WriteString(`{"ok":true,"result":[`)
			for i := 1; i <= total; i++ {
				if i > 1 {
					sb.WriteString(",")
				}
				fmt.Fprintf(&sb, `{"update_id":%d,"message":{"message_id":%d,"date":1,`+
					`"chat":{"id":-100500,"type":"supergroup"},"text":"msg-%d"}}`, i, i, i)
			}
			sb.WriteString(`]}`)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, sb.String())
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sender, err := NewTGBotSender(ctx, token, srv.URL)
	if err != nil {
		t.Fatalf("NewTGBotSender: %v", err)
	}
	updates := sender.StartPolling(ctx)

	for want := 1; want <= total; want++ {
		select {
		case upd := <-updates:
			if upd.Message == nil {
				t.Fatalf("update %d without message", want)
			}
			if upd.Message.MessageID != want {
				t.Fatalf("update order broken: got message %d, want %d", upd.Message.MessageID, want)
			}
		case <-time.After(15 * time.Second):
			t.Fatalf("update %d was not delivered", want)
		}
	}
}
