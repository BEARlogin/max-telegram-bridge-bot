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
