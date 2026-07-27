package main

import (
	"encoding/json"
	"testing"
	"time"

	maxschemes "github.com/max-messenger/max-bot-api-client-go/schemes"
)

func TestQueueTimeoutAllowsMediaTransfer(t *testing.T) {
	if got := queueTimeout(QueueItem{}); got != 45*time.Second {
		t.Fatalf("text timeout=%s", got)
	}
	if got := queueTimeout(QueueItem{AttType: "video"}); got != 5*time.Minute {
		t.Fatalf("video timeout=%s", got)
	}
	if got := queueTimeout(QueueItem{AttType: "album"}); got != 5*time.Minute {
		t.Fatalf("album timeout=%s", got)
	}
}

func TestTgQueueMediaSourceRoundTrip(t *testing.T) {
	raw := encodeTgQueueMediaSource("tg-file-id", "video.mp4", maxschemes.VIDEO)
	got, ok := decodeTgQueueMediaSource(raw)
	if !ok {
		t.Fatal("encoded source did not decode")
	}
	if got.FileID != "tg-file-id" || got.FileName != "video.mp4" || got.UploadType != "video" {
		t.Fatalf("source=%+v", got)
	}
	if _, ok := decodeTgQueueMediaSource("https://legacy.example/video.mp4"); ok {
		t.Fatal("legacy URL unexpectedly decoded as TG source")
	}
}

func TestMaxMessageAttachmentsFindsForwardedVideoForQueueRefresh(t *testing.T) {
	raw, err := json.Marshal(map[string]any{
		"type": "video",
		"payload": map[string]any{
			"token": "video-token",
			"url":   "https://cdn.example/fresh-video.mp4",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	msg := &maxschemes.Message{
		Link: &maxschemes.LinkedMessage{
			Type: maxschemes.FORWARD,
			Message: maxschemes.MessageBody{
				RawAttachments: []json.RawMessage{raw},
			},
		},
	}

	got := maxVideoURLFromAttachments(maxMessageAttachments(msg), "")
	if got != "https://cdn.example/fresh-video.mp4" {
		t.Fatalf("fresh video URL=%q", got)
	}
}

func TestMaxMessageAttachmentsPrefersForwardedMedia(t *testing.T) {
	msg := &maxschemes.Message{
		Body: maxschemes.MessageBody{Attachments: []interface{}{
			&maxschemes.VideoAttachment{Payload: maxschemes.MediaAttachmentPayload{Url: "https://cdn.example/wrapper.mp4"}},
		}},
		Link: &maxschemes.LinkedMessage{
			Type: maxschemes.FORWARD,
			Message: maxschemes.MessageBody{Attachments: []interface{}{
				&maxschemes.VideoAttachment{Payload: maxschemes.MediaAttachmentPayload{Url: "https://cdn.example/original.mp4"}},
			}},
		},
	}

	got := maxVideoURLFromAttachments(maxMessageAttachments(msg), "")
	if got != "https://cdn.example/original.mp4" {
		t.Fatalf("video URL=%q, want forwarded original", got)
	}
}

func TestMaxAlbumItemsFromAttachmentsKeepsPhotoVideoOrder(t *testing.T) {
	items := maxAlbumItemsFromAttachments([]interface{}{
		&maxschemes.PhotoAttachment{Payload: maxschemes.PhotoAttachmentPayload{Url: "https://cdn.example/one.jpg"}},
		&maxschemes.VideoAttachment{Payload: maxschemes.MediaAttachmentPayload{Url: "https://cdn.example/two.mp4"}},
	})
	if len(items) != 2 {
		t.Fatalf("album items=%d", len(items))
	}
	if items[0].Type != "photo" || items[0].URL != "https://cdn.example/one.jpg" {
		t.Fatalf("first item=%+v", items[0])
	}
	if items[1].Type != "video" || items[1].URL != "https://cdn.example/two.mp4" {
		t.Fatalf("second item=%+v", items[1])
	}
}
