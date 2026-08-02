package main

import "testing"

func TestShouldSkipDiscussionRelay(t *testing.T) {
	tests := []struct {
		name         string
		threadLinked bool
		groupLinked  bool
		wantSkip     bool
	}{
		{name: "commenter only", wantSkip: true},
		{name: "explicit group bridge", groupLinked: true},
		{name: "explicit thread bridge", threadLinked: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldSkipDiscussionRelay(tt.threadLinked, tt.groupLinked); got != tt.wantSkip {
				t.Fatalf("skip=%v, want %v", got, tt.wantSkip)
			}
		})
	}
}

func TestShouldSkipAutomaticForwardRelay(t *testing.T) {
	tests := []struct {
		name         string
		threadLinked bool
		groupLinked  bool
		wantSkip     bool
	}{
		{name: "discussion mapping only", wantSkip: true},
		{name: "explicit group bridge", groupLinked: true},
		{name: "explicit thread bridge", threadLinked: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldSkipAutomaticForwardRelay(tt.threadLinked, tt.groupLinked); got != tt.wantSkip {
				t.Fatalf("skip=%v, want %v", got, tt.wantSkip)
			}
		})
	}
}
