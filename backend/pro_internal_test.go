package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPaidSubscriptionActive(t *testing.T) {
	now := int64(100)
	for _, tc := range []struct {
		status string
		until  int64
		want   bool
	}{
		{status: "active", until: 101, want: true},
		{status: "canceled", until: 101, want: true},
		{status: "trial", until: 101, want: false},
		{status: "pending", until: 101, want: false},
		{status: "active", until: 100, want: false},
	} {
		if got := paidSubscriptionActive(tc.status, tc.until, now); got != tc.want {
			t.Fatalf("paidSubscriptionActive(%q, %d, %d) = %v, want %v", tc.status, tc.until, now, got, tc.want)
		}
	}
}

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
