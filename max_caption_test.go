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
