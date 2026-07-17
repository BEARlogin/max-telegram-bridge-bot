//go:build addon

package main

import "testing"

func TestIsUnforwardable_RecipientRestrictions(t *testing.T) {
	// Пост СУЩЕСТВУЕТ, но не форвардится в скретч-ЛС (приватность получателя) —
	// должен трактоваться как unforwardable (exists=true), а не как фатал/«поста нет».
	unforwardable := []string{
		"telegram: bad request, Bad Request: VOICE_MESSAGES_FORBIDDEN (400)",
		"Bad Request: VIDEO_MESSAGES_FORBIDDEN (400)",
		"Bad Request: CHAT_FORWARDS_RESTRICTED",
		"message can't be copied",
		"This message can't be forwarded",
	}
	for _, s := range unforwardable {
		if !isUnforwardable(s) {
			t.Errorf("isUnforwardable(%q) = false, want true", s)
		}
		if isAddonMsgGone(s) {
			t.Errorf("isAddonMsgGone(%q) = true, want false (пост есть)", s)
		}
	}
	// Реально отсутствующий пост — НЕ unforwardable, это «поста нет».
	gone := []string{"Bad Request: message to forward not found", "MESSAGE_ID_INVALID"}
	for _, s := range gone {
		if isUnforwardable(s) {
			t.Errorf("isUnforwardable(%q) = true, want false", s)
		}
		if !isAddonMsgGone(s) {
			t.Errorf("isAddonMsgGone(%q) = false, want true", s)
		}
	}
}
