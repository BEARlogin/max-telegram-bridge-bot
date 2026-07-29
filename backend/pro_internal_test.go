package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInternalProSubscribeRejectsWrongMethod(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/internal/pro-subscribe", nil)
	rec := httptest.NewRecorder()

	(&server{}).handleInternalProSubscribe(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestInternalProSubscribeRejectsMissingSecret(t *testing.T) {
	t.Setenv("COMMENT_SYNC_SECRET", "expected")
	req := httptest.NewRequest(http.MethodPost, "/api/internal/pro-subscribe",
		strings.NewReader(`{"user_id":42,"secret":"wrong"}`))
	rec := httptest.NewRecorder()

	(&server{}).handleInternalProSubscribe(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestPaidSubscriptionActive(t *testing.T) {
	const now int64 = 100
	tests := []struct {
		name      string
		status    string
		paidUntil int64
		want      bool
	}{
		{name: "active future", status: "active", paidUntil: 200, want: true},
		{name: "canceled future", status: "canceled", paidUntil: 200, want: true},
		{name: "trial future", status: "trial", paidUntil: 200, want: false},
		{name: "pending future", status: "pending", paidUntil: 200, want: false},
		{name: "active expired", status: "active", paidUntil: 100, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := paidSubscriptionActive(tt.status, tt.paidUntil, now); got != tt.want {
				t.Fatalf("paidSubscriptionActive(%q, %d, %d) = %v, want %v",
					tt.status, tt.paidUntil, now, got, tt.want)
			}
		})
	}
}
