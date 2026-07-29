package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func TestSendRichMediaMessageUploadsVideo(t *testing.T) {
	var gotRichHTML string
	var gotVideo []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bot123:ABC/sendRichMessage" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("ParseMultipartForm: %v", err)
		}
		if got := r.FormValue("chat_id"); got != "-10042" {
			t.Errorf("chat_id = %q", got)
		}
		if got := r.FormValue("message_thread_id"); got != "7" {
			t.Errorf("message_thread_id = %q", got)
		}
		var rich tgInputRichMessage
		if err := json.Unmarshal([]byte(r.FormValue("rich_message")), &rich); err != nil {
			t.Fatalf("rich_message: %v", err)
		}
		gotRichHTML = rich.HTML
		if len(rich.Media) != 1 || rich.Media[0].ID != tgRichMediaID ||
			rich.Media[0].Media.Type != "video" ||
			rich.Media[0].Media.Media != "attach://"+tgRichMediaID {
			t.Errorf("media = %#v", rich.Media)
		}
		file, _, err := r.FormFile(tgRichMediaID)
		if err != nil {
			t.Fatalf("media file: %v", err)
		}
		defer file.Close()
		gotVideo, _ = io.ReadAll(file)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":314}}`)
	}))
	defer server.Close()

	s := &tgBotSender{
		token:      "123:ABC",
		apiURL:     server.URL,
		httpClient: server.Client(),
	}
	msgID, err := s.SendRichMediaMessage(context.Background(), -10042,
		`<video src="tg://video?id=bridge_media"></video><p>Длинный текст</p>`,
		TGRichMedia{Type: "video", File: FileArg{Name: "post.mp4", Bytes: []byte("video-data")}},
		&SendOpts{ThreadID: 7, ReplyToID: 99})
	if err != nil {
		t.Fatalf("SendRichMediaMessage: %v", err)
	}
	if msgID != 314 {
		t.Fatalf("message_id = %d", msgID)
	}
	if !strings.Contains(gotRichHTML, `tg://video?id=bridge_media`) {
		t.Errorf("html = %q", gotRichHTML)
	}
	if string(gotVideo) != "video-data" {
		t.Errorf("video = %q", gotVideo)
	}
}

func TestSendMessageUsesEphemeralCommandContext(t *testing.T) {
	var got tgSendEphemeralMessageParams
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bot123:ABC/sendMessage" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"result":{"ephemeral_message_id":314}}`)
	}))
	defer server.Close()

	s := &tgBotSender{token: "123:ABC", apiURL: server.URL, httpClient: server.Client()}
	ctx := withTGEphemeralTarget(context.Background(), tgEphemeralTarget{
		ChatID:                     -10042,
		ReceiverUserID:             7001,
		IncomingEphemeralMessageID: 99,
	})
	msgID, err := s.SendMessage(ctx, -10042, "Только для вас", &SendOpts{
		ThreadID:  7,
		ParseMode: "HTML",
		ReplyMarkup: NewInlineKeyboard(NewInlineRow(
			NewInlineButton("Готово", "done"),
		)),
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if msgID != 0 {
		t.Fatalf("ordinary message id = %d, want 0 for ephemeral", msgID)
	}
	if got.ChatID != -10042 || got.ReceiverUserID != 7001 || got.MessageThreadID != 7 {
		t.Fatalf("target = %+v", got)
	}
	if got.ReplyParameters == nil || got.ReplyParameters.EphemeralMessageID != 99 {
		t.Fatalf("reply_parameters = %+v", got.ReplyParameters)
	}
	if got.ReplyMarkup == nil {
		t.Fatal("inline keyboard was lost")
	}
}

func TestSendMessageUsesExplicitEphemeralReceiverForWelcome(t *testing.T) {
	var receiver int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		receiver = int64(body["receiver_user_id"].(float64))
		_, _ = io.WriteString(w, `{"ok":true,"result":{"ephemeral_message_id":15}}`)
	}))
	defer server.Close()

	s := &tgBotSender{token: "123:ABC", apiURL: server.URL, httpClient: server.Client()}
	if _, err := s.SendMessage(context.Background(), -10042, "Добро пожаловать", &SendOpts{
		ReceiverUserID: 42,
	}); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if receiver != 42 {
		t.Fatalf("receiver_user_id = %d", receiver)
	}
}

func TestSetMyCommandsSendsEphemeralFlagAndGroupScope(t *testing.T) {
	var got tgSetMyCommandsParams
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bot123:ABC/setMyCommands" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_, _ = io.WriteString(w, `{"ok":true,"result":true}`)
	}))
	defer server.Close()

	s := &tgBotSender{token: "123:ABC", apiURL: server.URL, httpClient: server.Client()}
	err := s.SetMyCommands(context.Background(), []BotCommand{{
		Command: "bridge", Description: "Связать", IsEphemeral: true,
	}}, &CommandScope{Type: "all_group_chats"})
	if err != nil {
		t.Fatalf("SetMyCommands: %v", err)
	}
	if got.Scope == nil || got.Scope.Type != "all_group_chats" {
		t.Fatalf("scope = %+v", got.Scope)
	}
	if len(got.Commands) != 1 || !got.Commands[0].IsEphemeral {
		t.Fatalf("commands = %+v", got.Commands)
	}
}

func TestEphemeralCallbackMessageCanBeEditedAndDeleted(t *testing.T) {
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.URL.Path)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if got := int(body["ephemeral_message_id"].(float64)); got != 89 {
			t.Fatalf("ephemeral_message_id = %d", got)
		}
		if got := int64(body["receiver_user_id"].(float64)); got != 7001 {
			t.Fatalf("receiver_user_id = %d", got)
		}
		_, _ = io.WriteString(w, `{"ok":true,"result":true}`)
	}))
	defer server.Close()

	s := &tgBotSender{token: "123:ABC", apiURL: server.URL, httpClient: server.Client()}
	ctx := withTGEphemeralTarget(context.Background(), tgEphemeralTarget{
		ChatID:                     -10042,
		ReceiverUserID:             7001,
		IncomingEphemeralMessageID: 89,
		CallbackQueryID:            "cb-1",
	})
	if err := s.EditMessageText(ctx, -10042, 0, "Новый экран", &SendOpts{
		ReplyMarkup: NewInlineKeyboard(NewInlineRow(NewInlineButton("Дальше", "next"))),
	}); err != nil {
		t.Fatalf("EditMessageText: %v", err)
	}
	if err := s.DeleteMessage(ctx, -10042, 0); err != nil {
		t.Fatalf("DeleteMessage: %v", err)
	}
	want := []string{
		"/bot123:ABC/editEphemeralMessageText",
		"/bot123:ABC/deleteEphemeralMessage",
	}
	if len(methods) != len(want) || methods[0] != want[0] || methods[1] != want[1] {
		t.Fatalf("methods = %#v, want %#v", methods, want)
	}
}

func TestRawWebhookEphemeralIDsArePreserved(t *testing.T) {
	s := &tgBotSender{}
	s.captureRawEphemeralUpdate([]byte(`{
		"update_id": 77,
		"message": {"ephemeral_message_id": 88}
	}`))
	update := TGUpdate{Message: &TGMessage{}}
	s.applyRawEphemeralUpdate(77, &update)
	if update.Message.EphemeralMessageID != 88 {
		t.Fatalf("message ephemeral id = %d", update.Message.EphemeralMessageID)
	}

	s.captureRawEphemeralUpdate([]byte(`{
		"update_id": 78,
		"callback_query": {"message": {"ephemeral_message_id": 89}}
	}`))
	update = TGUpdate{CallbackQuery: &TGCallback{Message: &TGMessage{}}}
	s.applyRawEphemeralUpdate(78, &update)
	if update.CallbackQuery.Message.EphemeralMessageID != 89 {
		t.Fatalf("callback ephemeral id = %d", update.CallbackQuery.Message.EphemeralMessageID)
	}
}

// --- convertMsg ---

func TestConvertMsg_Nil(t *testing.T) {
	if got := convertMsg(nil); got != nil {
		t.Errorf("convertMsg(nil) = %v, want nil", got)
	}
}

func TestApplySendMessageOptsRequestsExistingMemberChatWithoutAdmin(t *testing.T) {
	p := &bot.SendMessageParams{}
	applySendMessageOpts(p, &SendOpts{RequestChat: &RequestChatSpec{
		Text:        "Выбрать группу",
		RequestID:   7302,
		BotIsMember: true,
	}})
	markup, ok := p.ReplyMarkup.(*models.ReplyKeyboardMarkup)
	if !ok || len(markup.Keyboard) != 1 || len(markup.Keyboard[0]) != 1 {
		t.Fatalf("reply markup = %#v", p.ReplyMarkup)
	}
	req := markup.Keyboard[0][0].RequestChat
	if req == nil || !req.BotIsMember || req.BotAdministratorRights != nil || req.UserAdministratorRights != nil {
		t.Fatalf("request chat = %+v", req)
	}
}

func TestApplySendMessageOptsRequestsAdminChannel(t *testing.T) {
	p := &bot.SendMessageParams{}
	applySendMessageOpts(p, &SendOpts{RequestChat: &RequestChatSpec{
		Text: "Выбрать канал", RequestID: 7402, ChatIsChannel: true, RequireAdmin: true,
	}})
	markup, ok := p.ReplyMarkup.(*models.ReplyKeyboardMarkup)
	if !ok || len(markup.Keyboard) != 1 || len(markup.Keyboard[0]) != 1 {
		t.Fatalf("reply markup = %#v", p.ReplyMarkup)
	}
	req := markup.Keyboard[0][0].RequestChat
	if req == nil || !req.ChatIsChannel || req.BotAdministratorRights == nil ||
		!req.BotAdministratorRights.CanPostMessages || !req.BotAdministratorRights.CanEditMessages {
		t.Fatalf("request channel = %+v", req)
	}
}

func TestConvertMsg_Basic(t *testing.T) {
	m := &models.Message{
		ID:              42,
		MessageThreadID: 7,
		Chat:            models.Chat{ID: -100, Type: "supergroup", Title: "Test"},
		Text:            "hello",
		Caption:         "cap",
		MediaGroupID:    "mg1",
		MigrateToChatID: -200,
	}
	got := convertMsg(m)
	if got.MessageID != 42 {
		t.Errorf("MessageID = %d, want 42", got.MessageID)
	}
	if got.MessageThreadID != 7 {
		t.Errorf("MessageThreadID = %d, want 7", got.MessageThreadID)
	}
	if got.Chat.ID != -100 || got.Chat.Type != "supergroup" || got.Chat.Title != "Test" {
		t.Errorf("Chat = %+v", got.Chat)
	}
	if got.Text != "hello" {
		t.Errorf("Text = %q", got.Text)
	}
	if got.Caption != "cap" {
		t.Errorf("Caption = %q", got.Caption)
	}
	if got.MediaGroupID != "mg1" {
		t.Errorf("MediaGroupID = %q", got.MediaGroupID)
	}
	if got.MigrateToChatID != -200 {
		t.Errorf("MigrateToChatID = %d", got.MigrateToChatID)
	}
	if got.From != nil {
		t.Errorf("From should be nil when input From is nil")
	}
	if got.SenderChat != nil {
		t.Errorf("SenderChat should be nil")
	}
}

func TestConvertUpdateBusinessFields(t *testing.T) {
	u := &models.Update{
		BusinessConnection: &models.BusinessConnection{
			ID:         "connection",
			User:       models.User{ID: 101},
			UserChatID: 101,
			IsEnabled:  true,
			Rights:     &models.BusinessBotRights{CanReply: true, CanReadMessages: true},
		},
		BusinessMessage: &models.Message{
			ID:                   7,
			BusinessConnectionID: "connection",
			Chat:                 models.Chat{ID: 404, Type: "private"},
			From:                 &models.User{ID: 404, FirstName: "Иван"},
			Text:                 "Привет",
		},
	}
	got := convertUpdate(u)
	if got.BusinessConnection == nil || got.BusinessConnection.ID != "connection" ||
		!got.BusinessConnection.CanReply || !got.BusinessConnection.CanRead {
		t.Fatalf("connection=%+v", got.BusinessConnection)
	}
	if got.BusinessMessage == nil || got.BusinessMessage.BusinessConnectionID != "connection" || got.BusinessMessage.Chat.ID != 404 {
		t.Fatalf("message=%+v", got.BusinessMessage)
	}
}

func TestConvertMsg_From(t *testing.T) {
	m := &models.Message{
		ID: 1,
		From: &models.User{
			ID:        123,
			IsBot:     true,
			Username:  "testbot",
			FirstName: "Test",
			LastName:  "Bot",
		},
		Chat: models.Chat{ID: 1},
	}
	got := convertMsg(m)
	if got.From == nil {
		t.Fatal("From is nil")
	}
	if got.From.ID != 123 {
		t.Errorf("From.ID = %d", got.From.ID)
	}
	if !got.From.IsBot {
		t.Error("From.IsBot = false")
	}
	if got.From.UserName != "testbot" {
		t.Errorf("From.UserName = %q", got.From.UserName)
	}
	if got.From.FirstName != "Test" || got.From.LastName != "Bot" {
		t.Errorf("From name = %q %q", got.From.FirstName, got.From.LastName)
	}
}

func TestConvertMsg_SenderChat(t *testing.T) {
	m := &models.Message{
		ID:         1,
		Chat:       models.Chat{ID: 1},
		SenderChat: &models.Chat{ID: -500, Type: "channel", Title: "Chan"},
	}
	got := convertMsg(m)
	if got.SenderChat == nil {
		t.Fatal("SenderChat nil")
	}
	if got.SenderChat.ID != -500 || got.SenderChat.Type != "channel" || got.SenderChat.Title != "Chan" {
		t.Errorf("SenderChat = %+v", got.SenderChat)
	}
}

func TestConvertMsg_ForwardOriginChannel(t *testing.T) {
	m := &models.Message{
		ID:   1,
		Chat: models.Chat{ID: 1},
		ForwardOrigin: &models.MessageOrigin{
			MessageOriginChannel: &models.MessageOriginChannel{
				Chat: models.Chat{ID: -999, Type: "channel", Title: "News"},
			},
		},
	}
	got := convertMsg(m)
	if got.ForwardOriginChat == nil {
		t.Fatal("ForwardOriginChat nil")
	}
	if got.ForwardOriginChat.ID != -999 || got.ForwardOriginChat.Title != "News" {
		t.Errorf("ForwardOriginChat = %+v", got.ForwardOriginChat)
	}
}

func TestConvertMsg_ForwardOriginNonChannel(t *testing.T) {
	m := &models.Message{
		ID:   1,
		Chat: models.Chat{ID: 1},
		ForwardOrigin: &models.MessageOrigin{
			MessageOriginUser: &models.MessageOriginUser{},
		},
	}
	got := convertMsg(m)
	if got.ForwardOriginChat != nil {
		t.Errorf("ForwardOriginChat should be nil for non-channel origin, got %+v", got.ForwardOriginChat)
	}
}

func TestConvertMsg_Media(t *testing.T) {
	m := &models.Message{
		ID:   1,
		Chat: models.Chat{ID: 1},
		Photo: []models.PhotoSize{
			{FileID: "p1", FileSize: 100},
			{FileID: "p2", FileSize: 200},
		},
		Video:     &models.Video{FileID: "v1", FileName: "vid.mp4", FileSize: 5000},
		Document:  &models.Document{FileID: "d1", FileName: "doc.pdf", FileSize: 3000, MimeType: "application/pdf"},
		Animation: &models.Animation{FileID: "a1", FileName: "anim.gif", FileSize: 1000},
		Sticker:   &models.Sticker{FileID: "s1", FileSize: 50, IsAnimated: true},
		Voice:     &models.Voice{FileID: "vo1", FileSize: 800},
		Audio:     &models.Audio{FileID: "au1", FileName: "song.mp3", FileSize: 4000},
		VideoNote: &models.VideoNote{FileID: "vn1", FileSize: 600},
	}
	got := convertMsg(m)

	if len(got.Photo) != 2 || got.Photo[0].FileID != "p1" || got.Photo[1].FileSize != 200 {
		t.Errorf("Photo = %+v", got.Photo)
	}
	if got.Video == nil || got.Video.FileID != "v1" || got.Video.FileName != "vid.mp4" || got.Video.FileSize != 5000 {
		t.Errorf("Video = %+v", got.Video)
	}
	if got.Document == nil || got.Document.FileID != "d1" || got.Document.MimeType != "application/pdf" {
		t.Errorf("Document = %+v", got.Document)
	}
	if got.Animation == nil || got.Animation.FileID != "a1" {
		t.Errorf("Animation = %+v", got.Animation)
	}
	if got.Sticker == nil || got.Sticker.FileID != "s1" || !got.Sticker.IsAnimated {
		t.Errorf("Sticker = %+v", got.Sticker)
	}
	if got.Voice == nil || got.Voice.FileID != "vo1" || got.Voice.FileSize != 800 {
		t.Errorf("Voice = %+v", got.Voice)
	}
	if got.Audio == nil || got.Audio.FileID != "au1" || got.Audio.FileName != "song.mp3" {
		t.Errorf("Audio = %+v", got.Audio)
	}
	if got.VideoNote == nil || got.VideoNote.FileID != "vn1" {
		t.Errorf("VideoNote = %+v", got.VideoNote)
	}
}

func TestConvertMsg_Entities(t *testing.T) {
	m := &models.Message{
		ID:   1,
		Chat: models.Chat{ID: 1},
		Text: "hello world",
		Entities: []models.MessageEntity{
			{Type: "bold", Offset: 0, Length: 5},
			{Type: "text_link", Offset: 6, Length: 5, URL: "https://example.com"},
		},
		CaptionEntities: []models.MessageEntity{
			{Type: "italic", Offset: 0, Length: 3},
		},
	}
	got := convertMsg(m)
	if len(got.Entities) != 2 {
		t.Fatalf("Entities len = %d, want 2", len(got.Entities))
	}
	if got.Entities[0].Type != "bold" || got.Entities[0].Offset != 0 || got.Entities[0].Length != 5 {
		t.Errorf("Entities[0] = %+v", got.Entities[0])
	}
	if got.Entities[1].URL != "https://example.com" {
		t.Errorf("Entities[1].URL = %q", got.Entities[1].URL)
	}
	if len(got.CaptionEntities) != 1 || got.CaptionEntities[0].Type != "italic" {
		t.Errorf("CaptionEntities = %+v", got.CaptionEntities)
	}
}

func TestConvertMsg_ReplyToMessage(t *testing.T) {
	m := &models.Message{
		ID:   10,
		Chat: models.Chat{ID: 1},
		ReplyToMessage: &models.Message{
			ID:   5,
			Chat: models.Chat{ID: 1},
			Text: "original",
		},
	}
	got := convertMsg(m)
	if got.ReplyToMessage == nil {
		t.Fatal("ReplyToMessage nil")
	}
	if got.ReplyToMessage.MessageID != 5 || got.ReplyToMessage.Text != "original" {
		t.Errorf("ReplyToMessage = %+v", got.ReplyToMessage)
	}
}

// --- convertCallback ---

func TestConvertCallback_Nil(t *testing.T) {
	if got := convertCallback(nil); got != nil {
		t.Errorf("convertCallback(nil) = %v, want nil", got)
	}
}

func TestConvertCallback_Full(t *testing.T) {
	cb := &models.CallbackQuery{
		ID:   "cb123",
		Data: "cpd:both:999",
		From: models.User{ID: 42, Username: "user1", FirstName: "John"},
		Message: models.MaybeInaccessibleMessage{
			Message: &models.Message{
				ID:   77,
				Chat: models.Chat{ID: -100, Title: "Group"},
				Text: "old text",
			},
		},
	}
	got := convertCallback(cb)
	if got.ID != "cb123" || got.Data != "cpd:both:999" {
		t.Errorf("ID=%q Data=%q", got.ID, got.Data)
	}
	if got.From == nil || got.From.ID != 42 {
		t.Errorf("From = %+v", got.From)
	}
	if got.Message == nil || got.Message.MessageID != 77 || got.Message.Text != "old text" {
		t.Errorf("Message = %+v", got.Message)
	}
}

func TestConvertCallback_NoFrom(t *testing.T) {
	cb := &models.CallbackQuery{
		ID:   "cb1",
		From: models.User{}, // zero value, ID=0
	}
	got := convertCallback(cb)
	if got.From != nil {
		t.Errorf("From should be nil when ID=0, got %+v", got.From)
	}
}

func TestConvertCallback_InaccessibleMessage(t *testing.T) {
	cb := &models.CallbackQuery{
		ID:   "cb2",
		From: models.User{ID: 1},
		Message: models.MaybeInaccessibleMessage{
			InaccessibleMessage: &models.InaccessibleMessage{
				Chat:      models.Chat{ID: -100},
				MessageID: 55,
			},
		},
	}
	got := convertCallback(cb)
	if got.Message != nil {
		t.Errorf("Message should be nil for inaccessible message, got %+v", got.Message)
	}
}

// --- convertUpdate ---

func TestConvertUpdate_Routes(t *testing.T) {
	u := &models.Update{
		Message:           &models.Message{ID: 1, Chat: models.Chat{ID: 1}},
		EditedMessage:     &models.Message{ID: 2, Chat: models.Chat{ID: 1}},
		ChannelPost:       &models.Message{ID: 3, Chat: models.Chat{ID: 1}},
		EditedChannelPost: &models.Message{ID: 4, Chat: models.Chat{ID: 1}},
		CallbackQuery:     &models.CallbackQuery{ID: "cb5", From: models.User{ID: 1}},
	}
	got := convertUpdate(u)
	if got.Message == nil || got.Message.MessageID != 1 {
		t.Errorf("Message = %+v", got.Message)
	}
	if got.EditedMessage == nil || got.EditedMessage.MessageID != 2 {
		t.Errorf("EditedMessage = %+v", got.EditedMessage)
	}
	if got.ChannelPost == nil || got.ChannelPost.MessageID != 3 {
		t.Errorf("ChannelPost = %+v", got.ChannelPost)
	}
	if got.EditedChannelPost == nil || got.EditedChannelPost.MessageID != 4 {
		t.Errorf("EditedChannelPost = %+v", got.EditedChannelPost)
	}
	if got.CallbackQuery == nil || got.CallbackQuery.ID != "cb5" {
		t.Errorf("CallbackQuery = %+v", got.CallbackQuery)
	}
}

func TestConvertUpdate_Empty(t *testing.T) {
	got := convertUpdate(&models.Update{})
	if got.Message != nil || got.EditedMessage != nil || got.ChannelPost != nil || got.EditedChannelPost != nil || got.CallbackQuery != nil {
		t.Errorf("empty update should produce all nils")
	}
}

// --- wrapErr ---

func TestWrapErr_Nil(t *testing.T) {
	if got := wrapErr(nil); got != nil {
		t.Errorf("wrapErr(nil) = %v", got)
	}
}

func TestWrapErr_MigrateError(t *testing.T) {
	err := &bot.MigrateError{Message: "group migrated", MigrateToChatID: -1001234}
	got := wrapErr(err)
	var tgErr *TGError
	if !errors.As(got, &tgErr) {
		t.Fatalf("expected *TGError, got %T", got)
	}
	if tgErr.Code != 400 {
		t.Errorf("Code = %d, want 400", tgErr.Code)
	}
	if tgErr.MigrateToChatID != -1001234 {
		t.Errorf("MigrateToChatID = %d", tgErr.MigrateToChatID)
	}
}

func TestWrapErr_Forbidden(t *testing.T) {
	got := wrapErr(bot.ErrorForbidden)
	var tgErr *TGError
	if !errors.As(got, &tgErr) {
		t.Fatalf("expected *TGError, got %T: %v", got, got)
	}
	if tgErr.Code != 403 {
		t.Errorf("Code = %d, want 403", tgErr.Code)
	}
}

func TestWrapErr_BadRequest(t *testing.T) {
	got := wrapErr(bot.ErrorBadRequest)
	var tgErr *TGError
	if !errors.As(got, &tgErr) {
		t.Fatalf("expected *TGError, got %T", got)
	}
	if tgErr.Code != 400 {
		t.Errorf("Code = %d, want 400", tgErr.Code)
	}
}

func TestWrapErr_NotFound(t *testing.T) {
	got := wrapErr(bot.ErrorNotFound)
	var tgErr *TGError
	if !errors.As(got, &tgErr) {
		t.Fatalf("expected *TGError, got %T", got)
	}
	if tgErr.Code != 404 {
		t.Errorf("Code = %d, want 404", tgErr.Code)
	}
}

func TestWrapErr_TooManyRequests(t *testing.T) {
	err := &bot.TooManyRequestsError{Message: "slow down", RetryAfter: 30}
	got := wrapErr(err)
	var tgErr *TGError
	if !errors.As(got, &tgErr) {
		t.Fatalf("expected *TGError, got %T", got)
	}
	if tgErr.Code != 429 {
		t.Errorf("Code = %d, want 429", tgErr.Code)
	}
}

func TestWrapErr_UnknownError(t *testing.T) {
	orig := errors.New("something weird")
	got := wrapErr(orig)
	if got != orig {
		t.Errorf("unknown error should pass through, got %v", got)
	}
}

// --- toInputFile ---

func TestToInputFile_URL(t *testing.T) {
	f := toInputFile(FileArg{URL: "https://example.com/photo.jpg"})
	ifs, ok := f.(*models.InputFileString)
	if !ok {
		t.Fatalf("expected *InputFileString, got %T", f)
	}
	if ifs.Data != "https://example.com/photo.jpg" {
		t.Errorf("Data = %q", ifs.Data)
	}
}

func TestToInputFile_Bytes(t *testing.T) {
	f := toInputFile(FileArg{Name: "test.jpg", Bytes: []byte("data")})
	ifu, ok := f.(*models.InputFileUpload)
	if !ok {
		t.Fatalf("expected *InputFileUpload, got %T", f)
	}
	if ifu.Filename != "test.jpg" {
		t.Errorf("Filename = %q", ifu.Filename)
	}
}

func TestToInputFile_DefaultName(t *testing.T) {
	f := toInputFile(FileArg{Bytes: []byte("data")})
	ifu, ok := f.(*models.InputFileUpload)
	if !ok {
		t.Fatalf("expected *InputFileUpload, got %T", f)
	}
	if ifu.Filename != "file" {
		t.Errorf("Filename = %q, want 'file'", ifu.Filename)
	}
}

// --- toLibInputMedia ---

func TestToLibInputMedia_PhotoURL(t *testing.T) {
	m := toLibInputMedia(TGInputMedia{
		Type:      "photo",
		File:      FileArg{URL: "https://example.com/img.jpg"},
		Caption:   "nice",
		ParseMode: "HTML",
	})
	p, ok := m.(*models.InputMediaPhoto)
	if !ok {
		t.Fatalf("expected *InputMediaPhoto, got %T", m)
	}
	if p.Media != "https://example.com/img.jpg" {
		t.Errorf("Media = %q", p.Media)
	}
	if p.Caption != "nice" {
		t.Errorf("Caption = %q", p.Caption)
	}
	if p.ParseMode != "HTML" {
		t.Errorf("ParseMode = %q", p.ParseMode)
	}
}

func TestToLibInputMedia_VideoBytes(t *testing.T) {
	m := toLibInputMedia(TGInputMedia{
		Type: "video",
		File: FileArg{Name: "clip.mp4", Bytes: []byte("vid")},
	})
	v, ok := m.(*models.InputMediaVideo)
	if !ok {
		t.Fatalf("expected *InputMediaVideo, got %T", m)
	}
	if v.Media != "attach://clip.mp4" {
		t.Errorf("Media = %q", v.Media)
	}
	if v.MediaAttachment == nil {
		t.Error("MediaAttachment is nil")
	}
}

func TestToLibInputMedia_AudioURL(t *testing.T) {
	m := toLibInputMedia(TGInputMedia{Type: "audio", File: FileArg{URL: "https://x.com/a.mp3"}})
	if _, ok := m.(*models.InputMediaAudio); !ok {
		t.Fatalf("expected *InputMediaAudio, got %T", m)
	}
}

func TestToLibInputMedia_DocumentBytes(t *testing.T) {
	m := toLibInputMedia(TGInputMedia{Type: "document", File: FileArg{Name: "f.pdf", Bytes: []byte("pdf")}})
	d, ok := m.(*models.InputMediaDocument)
	if !ok {
		t.Fatalf("expected *InputMediaDocument, got %T", m)
	}
	if d.Media != "attach://f.pdf" {
		t.Errorf("Media = %q", d.Media)
	}
}

func TestToLibInputMedia_DefaultType(t *testing.T) {
	m := toLibInputMedia(TGInputMedia{Type: "unknown", File: FileArg{URL: "https://x.com/img"}})
	if _, ok := m.(*models.InputMediaPhoto); !ok {
		t.Fatalf("unknown type should default to photo, got %T", m)
	}
}

func TestToLibInputMedia_BytesDefaultName(t *testing.T) {
	m := toLibInputMedia(TGInputMedia{Type: "photo", File: FileArg{Bytes: []byte("x")}})
	p, ok := m.(*models.InputMediaPhoto)
	if !ok {
		t.Fatalf("expected *InputMediaPhoto, got %T", m)
	}
	if p.Media != "attach://file" {
		t.Errorf("Media = %q, want 'attach://file'", p.Media)
	}
}

// --- toLibKeyboard ---

func TestToLibKeyboard_Nil(t *testing.T) {
	if got := toLibKeyboard(nil); got != nil {
		t.Errorf("toLibKeyboard(nil) = %v", got)
	}
}

func TestToLibKeyboard(t *testing.T) {
	kb := &InlineKeyboardMarkup{
		Rows: [][]InlineKeyboardButton{
			{
				{Text: "A", CallbackData: "a"},
				{Text: "B", CallbackData: "b"},
			},
			{
				{Text: "C", CallbackData: "c"},
			},
		},
	}
	got := toLibKeyboard(kb)
	if len(got.InlineKeyboard) != 2 {
		t.Fatalf("rows = %d, want 2", len(got.InlineKeyboard))
	}
	if len(got.InlineKeyboard[0]) != 2 {
		t.Fatalf("row0 cols = %d, want 2", len(got.InlineKeyboard[0]))
	}
	if got.InlineKeyboard[0][0].Text != "A" || got.InlineKeyboard[0][0].CallbackData != "a" {
		t.Errorf("btn[0][0] = %+v", got.InlineKeyboard[0][0])
	}
	if got.InlineKeyboard[1][0].Text != "C" {
		t.Errorf("btn[1][0] = %+v", got.InlineKeyboard[1][0])
	}
}

// --- keyboard helpers ---

func TestNewInlineKeyboard(t *testing.T) {
	kb := NewInlineKeyboard(
		NewInlineRow(NewInlineButton("X", "x"), NewInlineButton("Y", "y")),
		NewInlineRow(NewInlineButton("Z", "z")),
	)
	if len(kb.Rows) != 2 {
		t.Fatalf("rows = %d", len(kb.Rows))
	}
	if kb.Rows[0][0].Text != "X" || kb.Rows[0][1].CallbackData != "y" {
		t.Errorf("row0 = %+v", kb.Rows[0])
	}
}

// --- TGError ---

func TestTGError_Error(t *testing.T) {
	e := &TGError{Code: 403, Description: "Forbidden: bot blocked"}
	got := e.Error()
	if got != "telegram: Forbidden: bot blocked (403)" {
		t.Errorf("Error() = %q", got)
	}
}

// --- GetFileDirectURL ---

func TestGetFileDirectURL_Default(t *testing.T) {
	s := &tgBotSender{token: "123:ABC"}
	got := s.GetFileDirectURL("photos/file_1.jpg")
	want := "https://api.telegram.org/file/bot123:ABC/photos/file_1.jpg"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestGetFileDirectURL_CustomAPI(t *testing.T) {
	s := &tgBotSender{token: "123:ABC", apiURL: "http://localhost:8081"}
	got := s.GetFileDirectURL("photos/file_1.jpg")
	want := "http://localhost:8081/photos/file_1.jpg"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// В режиме --local getFile отдаёт абсолютный путь — срезаем префикс до токена,
// чтобы получился относительный путь, который раздаёт nginx.
func TestGetFileDirectURL_LocalAbsolutePath(t *testing.T) {
	s := &tgBotSender{token: "123:ABC", apiURL: "http://localhost:8081"}
	got := s.GetFileDirectURL("/var/lib/telegram-bot-api/123:ABC/videos/file_5.mp4")
	want := "http://localhost:8081/videos/file_5.mp4"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
