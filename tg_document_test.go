package main

import (
	"testing"

	maxschemes "github.com/max-messenger/max-bot-api-client-go/schemes"
)

func TestTgDocumentVideoStaysFileInMax(t *testing.T) {
	name, uploadType, attachmentType := tgDocumentMaxSpec("original.mp4", "video/mp4")
	if name != "original.mp4" {
		t.Fatalf("name = %q", name)
	}
	if uploadType != maxschemes.FILE {
		t.Fatalf("upload type = %q, want file", uploadType)
	}
	if attachmentType != "file" {
		t.Fatalf("attachment type = %q, want file", attachmentType)
	}
}

func TestTgDocumentVideoWithoutNameGetsFileName(t *testing.T) {
	name, uploadType, attachmentType := tgDocumentMaxSpec("", "video/mp4")
	if name != "document.mp4" {
		t.Fatalf("name = %q, want document.mp4", name)
	}
	if uploadType != maxschemes.FILE || attachmentType != "file" {
		t.Fatalf("upload type = %q, attachment type = %q", uploadType, attachmentType)
	}
}

func TestMaxFileUploadUsesOneGiBLimit(t *testing.T) {
	if got := maxUploadLimit(maxschemes.FILE); got != 1<<30 {
		t.Fatalf("file upload limit = %d, want %d", got, int64(1<<30))
	}
	if got := maxUploadLimit(maxschemes.VIDEO); got != mediaMaxBytes {
		t.Fatalf("video upload limit = %d, want %d", got, mediaMaxBytes)
	}
}
