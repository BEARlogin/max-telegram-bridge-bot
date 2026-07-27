package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type crosspostEditRoundTripper func(*http.Request) (*http.Response, error)

func (f crosspostEditRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestTgCrosspostEditPreservesMaxMedia(t *testing.T) {
	repo := testRepo(t)
	const (
		tgChatID  = int64(-100101)
		maxChatID = int64(-200202)
		tgMsgID   = 77
		maxMsgID  = "mid-with-media"
	)
	if err := repo.PairCrosspost(tgChatID, maxChatID, 11, 22); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetCrosspostSyncEdits(maxChatID, true); err != nil {
		t.Fatal(err)
	}
	repo.SaveMsgOrigin(tgChatID, tgMsgID, maxChatID, maxMsgID, 0, "tg")

	var requestBody []byte
	bridge := &Bridge{
		repo: repo,
		apiClient: &http.Client{Transport: crosspostEditRoundTripper(func(req *http.Request) (*http.Response, error) {
			var err error
			requestBody, err = io.ReadAll(req.Body)
			if err != nil {
				t.Fatal(err)
			}
			if req.Method != http.MethodPut {
				t.Fatalf("method=%s, want PUT", req.Method)
			}
			if !strings.Contains(req.URL.RawQuery, "message_id="+maxMsgID) {
				t.Fatalf("url=%s", req.URL)
			}
			if req.Header.Get("Authorization") != "test-token" {
				t.Fatalf("authorization=%q", req.Header.Get("Authorization"))
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader(nil)),
				Header:     make(http.Header),
			}, nil
		})},
		maxBotCache: make(map[int64]string),
	}
	ctx := bridge.withMaxToken(context.Background(), "test-token")
	bridge.handleTgEditedChannelPost(ctx, &TGMessage{
		MessageID: tgMsgID,
		Chat:      ChatInfo{ID: tgChatID, Type: "channel"},
		Caption:   "Исправленная подпись",
		Photo:     []PhotoSize{{FileID: "existing-photo"}},
	})

	if !bytes.Contains(requestBody, []byte(`"text":"Исправленная подпись"`)) {
		t.Fatalf("body=%s", requestBody)
	}
	if bytes.Contains(requestBody, []byte("attachments")) {
		t.Fatalf("edit must omit attachments to preserve MAX media: %s", requestBody)
	}
}
