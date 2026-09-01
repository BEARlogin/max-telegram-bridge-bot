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

func TestTgCrosspostCaptionEditSeedsUnknownMediaAndPreservesMaxAttachments(t *testing.T) {
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
	if err := repo.SetCrosspostReplacements(maxChatID, CrosspostReplacements{TgToMax: []Replacement{
		{From: "• Мы в МАХ", To: "", Target: "all"},
	}}); err != nil {
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
		Caption:   "Исправленная подпись\n\n• Мы в МАХ",
		Photo:     []PhotoSize{{FileID: "existing-photo"}},
	})

	if !bytes.Contains(requestBody, []byte(`"text":"Исправленная подпись"`)) {
		t.Fatalf("body=%s", requestBody)
	}
	if !bytes.Contains(requestBody, []byte(`"format":"html"`)) {
		t.Fatalf("edit must preserve MAX HTML formatting: %s", requestBody)
	}
	if bytes.Contains(requestBody, []byte("Мы в МАХ")) {
		t.Fatalf("edit must reapply TG→MAX replacements: %s", requestBody)
	}
	if bytes.Contains(requestBody, []byte("attachments")) {
		t.Fatalf("edit must omit attachments to preserve MAX media: %s", requestBody)
	}
	state, ok := repo.GetTgMediaState(tgChatID, tgMsgID)
	if !ok || state.Fingerprint != "photo:existing-photo" {
		t.Fatalf("legacy media state was not seeded: state=%+v ok=%v", state, ok)
	}
}

func TestTgMediaStateDetectsReplacementAndRebuildsAlbum(t *testing.T) {
	before, ok := tgMediaStateFromMessage(&TGMessage{
		MessageID: 10, MediaGroupID: "album-1",
		Photo: []PhotoSize{{FileID: "small"}, {FileID: "photo-before"}},
	})
	if !ok || before.Fingerprint != "photo:photo-before" {
		t.Fatalf("before=%+v ok=%v", before, ok)
	}
	after, ok := tgMediaStateFromMessage(&TGMessage{
		MessageID: 10, MediaGroupID: "album-1",
		Photo: []PhotoSize{{FileID: "photo-after"}},
	})
	if !ok || before.Fingerprint == after.Fingerprint {
		t.Fatalf("replacement was not detected: before=%+v after=%+v", before, after)
	}
	states := replaceTgMediaState([]TgMediaState{
		before,
		{TgMsgID: 11, MediaGroupID: "album-1", Kind: "video", FileID: "video-2", Fingerprint: "video:video-2"},
	}, after)
	if err := validateAlbumMediaStates(states, "album-1"); err != nil {
		t.Fatal(err)
	}
	if len(states) != 2 || states[0].FileID != "photo-after" || states[1].FileID != "video-2" {
		t.Fatalf("states=%+v", states)
	}
}

func TestTgMediaStatePersistsForCompleteAlbum(t *testing.T) {
	repo := testRepo(t)
	const tgChatID = int64(-100777)
	repo.SaveMsgOrigin(tgChatID, 1, -2001, "mid-album", 0, "tg")
	repo.SaveMsgOrigin(tgChatID, 2, -2001, "mid-album", 0, "tg")
	repo.SaveTgMediaState(tgChatID, TgMediaState{
		TgMsgID: 1, MediaGroupID: "album", Kind: "photo",
		FileID: "photo-1", Fingerprint: "photo:photo-1",
	})
	repo.SaveTgMediaState(tgChatID, TgMediaState{
		TgMsgID: 2, MediaGroupID: "album", Kind: "video",
		FileID: "video-2", Fingerprint: "video:video-2",
	})
	states := repo.ListTgMediaStates(tgChatID, "mid-album")
	if err := validateAlbumMediaStates(states, "album"); err != nil {
		t.Fatal(err)
	}
	if len(states) != 2 {
		t.Fatalf("states=%+v", states)
	}
}
