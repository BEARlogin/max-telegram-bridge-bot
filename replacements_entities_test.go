package main

import (
	"strings"
	"testing"
)

// Удаление видимого текста ссылки должно убирать и сам text_link entity.
func TestApplyReplacementsToEntities_DropsLink(t *testing.T) {
	// "shop / ВК / MAX": ВК (offset7,len2) и MAX (offset12,len3) — text_link.
	text := "shop / ВК / MAX"
	ents := []Entity{
		{Type: "text_link", Offset: 7, Length: 2, URL: "https://vk.com/x"},
		{Type: "text_link", Offset: 12, Length: 3, URL: "https://max.ru/x"},
	}
	rules := []Replacement{{From: "ВК / MAX", To: "", Target: "all"}}

	gotText, gotEnts := applyReplacementsToEntities(text, ents, rules)
	if gotText != "shop / " {
		t.Fatalf("text = %q, want %q", gotText, "shop / ")
	}
	if len(gotEnts) != 0 {
		t.Fatalf("entities = %+v, want none (links removed)", gotEnts)
	}
	if html := tgEntitiesToHTML(gotText, gotEnts); strings.Contains(html, "<a") {
		t.Fatalf("html %q still contains a link", html)
	}
}

// Замена в середине сдвигает offset'ы следующих entities, но их формат сохраняется.
func TestApplyReplacementsToEntities_ShiftsFollowing(t *testing.T) {
	// "AAA BOLD": удаляем "AAA " → "BOLD"; bold-entity (offset4,len4) должен уехать в 0.
	text := "AAA BOLD"
	ents := []Entity{{Type: "bold", Offset: 4, Length: 4}}
	rules := []Replacement{{From: "AAA ", To: "", Target: "all"}}

	gotText, gotEnts := applyReplacementsToEntities(text, ents, rules)
	if gotText != "BOLD" {
		t.Fatalf("text = %q, want %q", gotText, "BOLD")
	}
	if len(gotEnts) != 1 || gotEnts[0].Offset != 0 || gotEnts[0].Length != 4 {
		t.Fatalf("entities = %+v, want bold offset0 len4", gotEnts)
	}
	if html := tgEntitiesToHTML(gotText, gotEnts); html != "<b>BOLD</b>" {
		t.Fatalf("html = %q, want <b>BOLD</b>", html)
	}
}
