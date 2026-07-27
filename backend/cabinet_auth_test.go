package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestCabinetMagicLinkIsOneTimeAndCreatesSession(t *testing.T) {
	oldSecret := os.Getenv("COMMENT_SYNC_SECRET")
	oldPublic := os.Getenv("CABINET_PUBLIC_URL")
	t.Cleanup(func() {
		_ = os.Setenv("COMMENT_SYNC_SECRET", oldSecret)
		_ = os.Setenv("CABINET_PUBLIC_URL", oldPublic)
		browserAuthDB = nil
	})
	_ = os.Setenv("COMMENT_SYNC_SECRET", "test-secret")
	_ = os.Setenv("CABINET_PUBLIC_URL", "https://example.test/commenter")

	st, err := newSQLiteStore(filepath.Join(t.TempDir(), "comments.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.db.Close()
	s := &server{store: st}

	body, _ := json.Marshal(map[string]any{
		"user_id": int64(42), "platform": "tg", "name": "Тест", "secret": "test-secret",
	})
	issue := httptest.NewRecorder()
	s.handleInternalCabinetLink(issue, httptest.NewRequest(http.MethodPost, "/api/internal/cabinet-link", bytes.NewReader(body)))
	if issue.Code != http.StatusOK {
		t.Fatalf("issue status=%d body=%s", issue.Code, issue.Body.String())
	}
	var issued struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(issue.Body.Bytes(), &issued); err != nil || issued.URL == "" {
		t.Fatalf("bad issue response: %s", issue.Body.String())
	}

	loginReq := httptest.NewRequest(http.MethodGet, issued.URL, nil)
	login := httptest.NewRecorder()
	s.handleCabinetLogin(login, loginReq)
	if login.Code != http.StatusSeeOther {
		t.Fatalf("login status=%d body=%s", login.Code, login.Body.String())
	}
	res := login.Result()
	cookies := res.Cookies()
	if len(cookies) != 1 || cookies[0].Name != cabinetCookieName ||
		!cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].Value == "" {
		t.Fatalf("unsafe session cookie: %#v", cookies)
	}

	apiReq := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	apiReq.AddCookie(cookies[0])
	u := authUser(apiReq)
	if !u.Valid || u.ID != 42 || u.Platform != "tg" || u.Name != "Тест" {
		t.Fatalf("unexpected session user: %#v", u)
	}

	replay := httptest.NewRecorder()
	s.handleCabinetLogin(replay, httptest.NewRequest(http.MethodGet, issued.URL, nil))
	if replay.Code != http.StatusUnauthorized {
		t.Fatalf("magic link replay status=%d, want 401", replay.Code)
	}
}

func TestBrowserCSRFFailsClosed(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "https://example.test/api/action", nil)
	req.Host = "example.test"
	req.AddCookie(&http.Cookie{Name: cabinetCookieName, Value: "session"})
	req.Header.Set("Origin", "https://evil.test")
	rec := httptest.NewRecorder()
	withBrowserCSRF(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("request reached protected handler")
	})).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want 403", rec.Code)
	}
}
