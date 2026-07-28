package main

import (
	"strings"
	"testing"
)

func TestSplitMaxMediaCaption(t *testing.T) {
	tests := []struct {
		name     string
		caption  string
		wantMain bool
	}{
		{name: "empty", caption: "", wantMain: true},
		{name: "limit", caption: strings.Repeat("я", 1024), wantMain: true},
		{name: "overflow", caption: strings.Repeat("я", 1025), wantMain: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			main, overflow := splitMaxMediaCaption(tt.caption)
			if tt.wantMain {
				if main != tt.caption || overflow != "" {
					t.Fatalf("main=%q overflow=%q", main, overflow)
				}
				return
			}
			if main != "" || overflow != tt.caption {
				t.Fatalf("main=%q overflow length=%d", main, len(overflow))
			}
		})
	}
}

func TestBuildTGRichMediaHTML(t *testing.T) {
	long := strings.Repeat("я", 1025)
	got, ok := buildTGRichMediaHTML(long+"\n<b>важно</b>", "HTML", "video")
	if !ok {
		t.Fatal("long video caption must use rich message")
	}
	if !strings.Contains(got, `<video src="tg://video?id=bridge_media"></video>`) {
		t.Fatalf("missing video block: %q", got)
	}
	if !strings.Contains(got, "<br><b>важно</b>") {
		t.Fatalf("formatting/newline lost: %q", got)
	}

	escaped, ok := buildTGRichMediaHTML(long+" <тег>", "", "photo")
	if !ok || !strings.Contains(escaped, "&lt;тег&gt;") {
		t.Fatalf("plain text is not escaped: %q", escaped)
	}
	if _, ok := buildTGRichMediaHTML("short", "HTML", "video"); ok {
		t.Fatal("short caption must keep regular Telegram media")
	}
	if _, ok := buildTGRichMediaHTML(long, "HTML", "document"); ok {
		t.Fatal("documents are not rich photo/video blocks")
	}
}
