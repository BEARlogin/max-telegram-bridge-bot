package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMaxCommentProbeSummaryKeepsSchemaAndIDsButNotContent(t *testing.T) {
	var root any
	body := []byte(`{
		"update_type":"message_created",
		"message":{
			"post_id":"post-secret",
			"thread_id":"thread-42",
			"sender":{"user_id":123,"name":"Private Name"},
			"recipient":{"chat_id":-77,"chat_type":"channel"},
			"body":{"mid":"mid.1","text":"private probe text","seq":5,
				"attachments":[{"type":"image","payload":{"url":"https://secret.invalid/photo","token":"secret-token"}}]
			}
		}
	}`)
	if err := json.Unmarshal(body, &root); err != nil {
		t.Fatal(err)
	}

	got := summarizeMaxCommentProbe(root)
	if got.UpdateType != "message_created" {
		t.Fatalf("update type = %q", got.UpdateType)
	}
	if got.IDs["message.post_id"] != "post-secret" ||
		got.IDs["message.thread_id"] != "thread-42" ||
		got.IDs["message.body.mid"] != "mid.1" {
		t.Fatalf("ids = %#v", got.IDs)
	}
	joined := strings.Join(got.Paths, ",") + stringifyProbeIDs(got.IDs)
	for _, secret := range []string{"private probe text", "Private Name", "https://secret.invalid/photo", "secret-token"} {
		if strings.Contains(joined, secret) {
			t.Fatalf("probe leaked %q in %s", secret, joined)
		}
	}
	if !strings.Contains(joined, "message.body.text:string") {
		t.Fatalf("schema path missing: %s", joined)
	}
}

func TestMaxCommentProbeWrapDoesNotChangeWebhookBody(t *testing.T) {
	const marker = "max-comment-poc-unique"
	body := `{"update_type":"message_created","message":{"body":{"mid":"mid.2","text":"` + marker + `"}}}`
	var seen string
	next := func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		seen = string(data)
		w.WriteHeader(http.StatusNoContent)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/max", strings.NewReader(body))
	newMaxCommentProbe(marker).Wrap(next)(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d", rec.Code)
	}
	if seen != body {
		t.Fatalf("body changed: %q", seen)
	}
}

func TestMaxCommentProbeAcknowledgesUnknownUpdateType(t *testing.T) {
	const marker = "max-comment-poc-unknown"
	body := `{"update_type":"comment_created","comment":{"post_id":"post.1","text":"` + marker + `"}}`
	called := false
	next := func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/max", strings.NewReader(body))
	newMaxCommentProbe(marker).Wrap(next)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if called {
		t.Fatal("unknown update must not be passed to SDK parser")
	}
}

func TestMaxCommentProbeRejectsUnsafeMarkerLength(t *testing.T) {
	if newMaxCommentProbe("short").Enabled() {
		t.Fatal("short marker must not enable probe")
	}
	if newMaxCommentProbe(strings.Repeat("x", 129)).Enabled() {
		t.Fatal("oversized marker must not enable probe")
	}
}

func TestMaxCommentProbePreservesOversizedWebhookBody(t *testing.T) {
	marker := "max-comment-poc-large"
	body := strings.Repeat("x", maxCommentProbeMaxBody+100)
	var size int
	next := func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		size = len(data)
		w.WriteHeader(http.StatusNoContent)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/max", strings.NewReader(body))
	newMaxCommentProbe(marker).Wrap(next)(rec, req)

	if rec.Code != http.StatusNoContent || size != len(body) {
		t.Fatalf("status=%d size=%d want=%d", rec.Code, size, len(body))
	}
}

func TestMaxCommentProbeTracksReplyAndDeleteByMessageID(t *testing.T) {
	probe := newMaxCommentProbe("max-comment-probe-42")
	probe.inspect([]byte(`{"update_type":"message_created","message":{"body":{"mid":"mid.42","text":"max-comment-probe-42"}}}`))

	reply := summarizeProbeTestJSON(t, []byte(
		`{"update_type":"message_created","message":{"body":{"mid":"mid.43","reply_to":"mid.42","text":"reply"}}}`,
	))
	if !probe.matchesTracked(reply.IDs) {
		t.Fatal("reply_to must match tracked comment")
	}
	probe.track(reply.IDs)

	removed := summarizeProbeTestJSON(t, []byte(
		`{"update_type":"message_removed","message_id":"mid.43","chat_id":-99}`,
	))
	if !probe.matchesTracked(removed.IDs) {
		t.Fatal("message_id must match tracked reply")
	}
}

func summarizeProbeTestJSON(t *testing.T, body []byte) maxCommentProbeSummary {
	t.Helper()
	var root any
	if err := json.Unmarshal(body, &root); err != nil {
		t.Fatal(err)
	}
	return summarizeMaxCommentProbe(root)
}

func stringifyProbeIDs(ids map[string]string) string {
	raw, _ := json.Marshal(ids)
	return string(raw)
}
