package main

import (
	"encoding/json"
	"testing"

	maxschemes "github.com/max-messenger/max-bot-api-client-go/schemes"
)

func TestAppendMaxShareURLForChannelRepost(t *testing.T) {
	attachments := []interface{}{&maxschemes.ShareAttachment{
		Payload: maxschemes.AttachmentPayload{Url: "https://max.ru/channel/post/42"},
	}}

	got := appendMaxShareURLs("Комментарий", attachments)
	want := "Комментарий\n\nhttps://max.ru/channel/post/42"
	if got != want {
		t.Fatalf("appendMaxShareURLs() = %q, want %q", got, want)
	}
}

func TestAppendMaxShareURLDoesNotDuplicateExistingLink(t *testing.T) {
	const url = "https://max.ru/channel/post/42"
	attachments := []interface{}{&maxschemes.ShareAttachment{
		Payload: maxschemes.AttachmentPayload{Url: url},
	}}

	if got := appendMaxShareURLs("Смотрите "+url, attachments); got != "Смотрите "+url {
		t.Fatalf("appendMaxShareURLs() duplicated URL: %q", got)
	}
}

func TestParseMaxRawShareAttachment(t *testing.T) {
	raw := []json.RawMessage{json.RawMessage(`{"type":"share","payload":{"url":"https://max.ru/channel/post/42"}}`)}

	got := parseMaxRawAttachments(raw)
	if len(got) != 1 {
		t.Fatalf("parseMaxRawAttachments() returned %d attachments", len(got))
	}
	share, ok := got[0].(*maxschemes.ShareAttachment)
	if !ok || share.Payload.Url != "https://max.ru/channel/post/42" {
		t.Fatalf("parsed attachment = %#v", got[0])
	}
}
