package main

import (
	"strings"
	"testing"
	"time"

	maxschemes "github.com/max-messenger/max-bot-api-client-go/schemes"
)

func TestParseNameCommand(t *testing.T) {
	tests := []struct {
		text  string
		mode  nameCommandMode
		alias string
		ok    bool
	}{
		{text: "/name", mode: nameCommandShow, ok: true},
		{text: "/name Михаил — папа Серёжи", mode: nameCommandSet, alias: "Михаил — папа Серёжи", ok: true},
		{text: "/name reset", mode: nameCommandReset, ok: true},
		{text: "/name удалить", mode: nameCommandReset, ok: true},
		{text: "/names", ok: false},
	}
	for _, tt := range tests {
		mode, alias, ok := parseNameCommand(tt.text)
		if mode != tt.mode || alias != tt.alias || ok != tt.ok {
			t.Errorf("parseNameCommand(%q) = (%v,%q,%v), want (%v,%q,%v)",
				tt.text, mode, alias, ok, tt.mode, tt.alias, tt.ok)
		}
	}
}

func TestValidateUserAlias(t *testing.T) {
	if got, err := validateUserAlias("  Михаил — папа Серёжи  "); err != nil || got != "Михаил — папа Серёжи" {
		t.Fatalf("valid alias = %q, %v", got, err)
	}
	for _, alias := range []string{"", "строка\nвторая", strings.Repeat("я", maxUserAliasRunes+1)} {
		if _, err := validateUserAlias(alias); err == nil {
			t.Fatalf("validateUserAlias(%q) accepted invalid value", alias)
		}
	}
}

func TestUserAliasScopedToSourceChat(t *testing.T) {
	repo := testRepo(t)
	if err := repo.SetUserAlias("tg", -1001, 42, "Папа Серёжи", 7); err != nil {
		t.Fatal(err)
	}
	if got, ok := repo.GetUserAlias("tg", -1001, 42); !ok || got != "Папа Серёжи" {
		t.Fatalf("alias = %q, %v", got, ok)
	}
	if _, ok := repo.GetUserAlias("tg", -1002, 42); ok {
		t.Fatal("alias leaked into another chat")
	}
	if _, ok := repo.GetUserAlias("max", -1001, 42); ok {
		t.Fatal("alias leaked into another platform")
	}
}

func TestMessageAuthorRoutesIdentifyMirroredSource(t *testing.T) {
	repo := testRepo(t)
	author := MessageAuthor{Platform: "tg", ChatID: -1001, UserID: 42}
	repo.SaveMessageAuthor("tg", -1001, "11", author)
	repo.SaveMsgOrigin(-1001, 11, -2001, "max-mid", 0, "tg")
	tgChatID, tgMsgID, maxChatID, origin, ok := repo.LookupMessageRouteByMax("max-mid")
	if !ok || tgChatID != -1001 || tgMsgID != 11 || maxChatID != -2001 || origin != "tg" {
		t.Fatalf("TG route = (%d,%d,%d,%q,%v)", tgChatID, tgMsgID, maxChatID, origin, ok)
	}

	maxAuthor := MessageAuthor{Platform: "max", ChatID: -2001, UserID: 77}
	repo.SaveMessageAuthor("max", -2001, "max-source", maxAuthor)
	repo.SaveMsgOrigin(-1001, 12, -2001, "max-source", 0, "max")
	gotMaxChat, gotMaxMID, gotOrigin, ok := repo.LookupMessageRouteByTg(-1001, 12)
	if !ok || gotMaxChat != -2001 || gotMaxMID != "max-source" || gotOrigin != "max" {
		t.Fatalf("MAX route = (%d,%q,%q,%v)", gotMaxChat, gotMaxMID, gotOrigin, ok)
	}
}

func TestResolveNameTargetFromMirroredMessages(t *testing.T) {
	repo := testRepo(t)
	bridge := &Bridge{repo: repo}
	tgAuthor := MessageAuthor{Platform: "max", ChatID: -2001, UserID: 77}
	repo.SaveMessageAuthor("max", -2001, "source-max-mid", tgAuthor)
	repo.SaveMsgOrigin(-1001, 15, -2001, "source-max-mid", 0, "max")
	tgMsg := &TGMessage{
		Chat: ChatInfo{ID: -1001},
		ReplyToMessage: &TGMessage{
			MessageID: 15,
			From:      &UserInfo{ID: 999, IsBot: true, UserName: "bridge"},
		},
	}
	if got, ok := bridge.resolveTgNameTarget(tgMsg); !ok || got != tgAuthor {
		t.Fatalf("TG target = %+v, %v", got, ok)
	}

	maxAuthor := MessageAuthor{Platform: "tg", ChatID: -1001, UserID: 42}
	repo.SaveMessageAuthor("tg", -1001, "16", maxAuthor)
	repo.SaveMsgOrigin(-1001, 16, -2001, "mirrored-mid", 0, "tg")
	maxMsg := &maxschemes.MessageCreatedUpdate{Message: maxschemes.Message{
		Recipient: maxschemes.Recipient{ChatId: -2001},
		Body:      maxschemes.MessageBody{ReplyTo: "mirrored-mid"},
	}}
	if got, ok := bridge.resolveMaxNameTarget(maxMsg); !ok || got != maxAuthor {
		t.Fatalf("MAX target = %+v, %v", got, ok)
	}
}

func TestRelayUsesAliasAndEscapesHTML(t *testing.T) {
	repo := testRepo(t)
	bridge := &Bridge{repo: repo}
	if err := repo.SetUserAlias("tg", -1001, 42, "Миша <папа>", 7); err != nil {
		t.Fatal(err)
	}
	msg := &TGMessage{Chat: ChatInfo{ID: -1001}, From: &UserInfo{ID: 42, FirstName: "Михаил"}, Text: "Привет"}
	if got := bridge.tgRelayName(msg); got != "Миша <папа>" {
		t.Fatalf("tgRelayName = %q", got)
	}
	if got := formatAttributionHTML(bridge.tgRelayName(msg), "Привет", false); got != "<b>Миша &lt;папа&gt;</b>: Привет" {
		t.Fatalf("HTML attribution = %q", got)
	}
}

func TestMessageMappingsAndAuthorsAreKeptForThirtyDays(t *testing.T) {
	repo := testRepo(t)
	now := time.Now().Unix()
	repo.SaveMessageAuthor("tg", -1001, "recent", MessageAuthor{Platform: "tg", ChatID: -1001, UserID: 42})
	repo.SaveMessageAuthor("tg", -1001, "old", MessageAuthor{Platform: "tg", ChatID: -1001, UserID: 43})
	repo.SaveMsgOrigin(-1001, 21, -2001, "recent-mid", 0, "tg")
	repo.SaveMsgOrigin(-1001, 22, -2001, "old-mid", 0, "tg")
	_, _ = repo.db.Exec("UPDATE message_authors SET created_at=? WHERE message_id IN (?,?)", now-31*24*3600, "old", "old-mid")
	_, _ = repo.db.Exec("UPDATE messages SET created_at=? WHERE tg_msg_id=?", now-31*24*3600, 22)
	_, _ = repo.db.Exec("UPDATE messages SET created_at=? WHERE tg_msg_id=?", now-29*24*3600, 21)

	repo.CleanOldMessages()
	if _, ok := repo.LookupMessageAuthor("tg", -1001, "old"); ok {
		t.Fatal("31-day-old author mapping was not removed")
	}
	if _, _, _, ok := repo.LookupTgMsgID("old-mid"); ok {
		t.Fatal("31-day-old message mapping was not removed")
	}
	if _, ok := repo.LookupMessageAuthor("tg", -1001, "recent"); !ok {
		t.Fatal("recent author mapping was removed")
	}
	if _, ok := repo.LookupMaxMsgID(-1001, 21); !ok {
		t.Fatal("29-day-old message mapping was removed")
	}
}
