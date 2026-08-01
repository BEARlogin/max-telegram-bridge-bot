//go:build addon

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSwitchWorkspaceUsesInternalCommenterEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/internal/workspace/switch" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		var in struct {
			UserID      int64  `json:"user_id"`
			Platform    string `json:"platform"`
			WorkspaceID int64  `json:"workspace_id"`
			Secret      string `json:"secret"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Fatal(err)
		}
		if in.UserID != 123 || in.Platform != "tg" || in.WorkspaceID != 77 || in.Secret != "test-secret" {
			t.Fatalf("payload=%+v", in)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"ok":true,"workspace_id":77,"workspace_name":"Команда"}`)
	}))
	defer server.Close()
	t.Setenv("COMMENTER_URL", server.URL)
	t.Setenv("COMMENT_SYNC_SECRET", "test-secret")

	name, err := (&Bridge{}).switchWorkspace(context.Background(), 123, "tg", 77)
	if err != nil || name != "Команда" {
		t.Fatalf("name=%q err=%v", name, err)
	}
}

func TestBuyMirrorSlotsShowsActualNextPeriodPrice(t *testing.T) {
	tests := []struct {
		name         string
		increase     uint64
		total        uint64
		wantIncrease string
		wantTotal    string
	}{
		{
			name:         "legacy 49 ruble slot cohort",
			increase:     24500,
			total:        54400,
			wantIncrease: "+245 ₽/мес",
			wantTotal:    "общий платёж PRO — 544 ₽/мес",
		},
		{
			name:         "new 99 ruble slot cohort",
			increase:     49500,
			total:        99400,
			wantIncrease: "+495 ₽/мес",
			wantTotal:    "общий платёж PRO — 994 ₽/мес",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/internal/mirror-slots" {
					t.Fatalf("path = %q", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{
					"ok": true,
					"pay_url": "https://pay.example/slot-order",
					"amount_kopecks": 13000,
					"slot_price_kopecks": 4900,
					"next_period_increase_kopecks": %d,
					"next_period_total_kopecks": %d
				}`, tt.increase, tt.total)
			}))
			defer server.Close()

			t.Setenv("COMMENTER_URL", server.URL)
			t.Setenv("COMMENT_SYNC_SECRET", "test-secret")
			ok, message := (&Bridge{}).buyMirrorSlots(context.Background(), 879822263, 5)
			if !ok {
				t.Fatalf("buyMirrorSlots failed: %s", message)
			}
			for _, want := range []string{
				"Докупка 5 слотов — 130 ₽",
				tt.wantIncrease,
				tt.wantTotal,
			} {
				if !strings.Contains(message, want) {
					t.Fatalf("message %q does not contain %q", message, want)
				}
			}
			if strings.Contains(message, "1500") || strings.Contains(message, "× 300") {
				t.Fatalf("stale hard-coded price leaked into message: %q", message)
			}
		})
	}
}
