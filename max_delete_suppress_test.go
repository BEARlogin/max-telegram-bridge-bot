package main

import "testing"

func TestSuppressedMaxDeleteCoversDuplicateWebhookUpdates(t *testing.T) {
	b := &Bridge{}
	b.suppressMaxDelete("mid-1")
	if !b.isSuppressedMaxDelete("mid-1") {
		t.Fatal("locally initiated deletion was not suppressed")
	}
	if !b.isSuppressedMaxDelete("mid-1") {
		t.Fatal("duplicate webhook update was not suppressed")
	}
	if b.isSuppressedMaxDelete("") {
		t.Fatal("empty message id must not be suppressed")
	}
}
