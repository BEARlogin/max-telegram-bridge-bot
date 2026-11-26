package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	maxbot "github.com/max-messenger/max-bot-api-client-go"
	maxschemes "github.com/max-messenger/max-bot-api-client-go/schemes"
)

// Run real SDK webhook decoding and destination HTTP clients against local
// servers. Permission denials here model destination API responses; source
// platform permissions still need verification in an authorized live test chat.
func TestServiceMembershipLocalWebhookDelivery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	repo := testRepo(t)
	insertPair(t, repo, -101, -201, 42)
	if err := repo.SetTgThreadID(-101, 15); err != nil {
		t.Fatal(err)
	}
	var tgCalls, maxCalls atomic.Int64
	var deny atomic.Bool
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/bot123:TEST/getMe":
			fmt.Fprint(w, `{"ok":true,"result":{"id":123,"is_bot":true,"first_name":"Test","username":"bridge_test_bot"}}`)
		case "/bot123:TEST/sendMessage":
			tgCalls.Add(1)
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Error(err)
			}
			if r.FormValue("chat_id") != "-101" || r.FormValue("message_thread_id") != "15" {
				t.Errorf("wrong TG route: %v", r.Form)
			}
			if deny.Load() {
				w.WriteHeader(403)
				fmt.Fprint(w, `{"ok":false,"error_code":403,"description":"Forbidden: not enough rights to send"}`)
				return
			}
			fmt.Fprint(w, `{"ok":true,"result":{"message_id":55,"chat":{"id":-101,"type":"supergroup"}}}`)
		case "/messages":
			maxCalls.Add(1)
			if r.URL.Query().Get("chat_id") != "-201" {
				t.Errorf("wrong MAX route: %s", r.URL)
			}
			if deny.Load() {
				w.WriteHeader(403)
				fmt.Fprint(w, `{"code":"chat.denied","message":"not enough rights"}`)
				return
			}
			fmt.Fprint(w, `{"message":{"body":{"mid":"local-mid"}}}`)
		default:
			t.Errorf("unexpected destination request: %s", r.URL)
			w.WriteHeader(404)
		}
	}))
	defer apiServer.Close()
	tg, err := NewTGBotSender(ctx, "123:TEST", apiServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	go tg.b.StartWebhook(ctx)
	maxAPI, err := maxbot.New("TEST", maxbot.WithBaseURL(apiServer.URL+"/"))
	if err != nil {
		t.Fatal(err)
	}
	maxUpdates := make(chan maxschemes.UpdateInterface, 2)
	mux := http.NewServeMux()
	mux.Handle("/tg", tg.b.WebhookHandler())
	mux.Handle("/max", maxAPI.GetHandler(maxUpdates))
	webhooks := httptest.NewServer(mux)
	defer webhooks.Close()
	localURL, _ := url.Parse(apiServer.URL)
	b := &Bridge{repo: repo, tg: tg, maxBotUID: 999, cfg: Config{MaxToken: "TEST"}, apiClient: &http.Client{Transport: crosspostEditRoundTripper(func(req *http.Request) (*http.Response, error) {
		clone := req.Clone(req.Context())
		clone.URL.Scheme, clone.URL.Host = localURL.Scheme, localURL.Host
		return http.DefaultTransport.RoundTrip(clone)
	})}}
	post := func(path, raw string) {
		t.Helper()
		resp, err := http.Post(webhooks.URL+path, "application/json", strings.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("webhook: %d", resp.StatusCode)
		}
	}
	for n, state := range []struct {
		raw             string
		deliver, denied bool
	}{
		{`{"enabled":false,"types":["member_joined","member_left"]}`, false, false},
		{`{"enabled":true,"types":[]}`, false, false},
		{`{"enabled":true,"types":["member_joined","member_left"]}`, true, false},
		{`{"enabled":true,"types":["member_joined","member_left"]}`, true, true},
	} {
		setServiceSettings(t, repo, -101, -201, state.raw)
		deny.Store(state.denied)
		beforeTG, beforeMax := tgCalls.Load(), maxCalls.Load()
		for i, event := range []string{"new_chat_members", "left_chat_member"} {
			id := n*10 + i + 1
			member := `{"id":7,"first_name":"Member"}`
			if i == 0 {
				member = "[" + member + "]"
			}
			post("/tg", fmt.Sprintf(`{"update_id":%d,"message":{"message_id":%d,"chat":{"id":-101,"type":"supergroup"},"from":{"id":7,"first_name":"Member"},%q:%s}}`, id, id, event, member))
			select {
			case upd := <-tg.updates:
				b.relayTgServiceMessage(ctx, upd.Message)
				b.relayTgServiceMessage(ctx, upd.Message)
			case <-time.After(2 * time.Second):
				t.Fatal("TG webhook did not reach adapter")
			}
			maxEvent, kind := "user_added", serviceMemberJoined
			if i == 1 {
				maxEvent, kind = "user_removed", serviceMemberLeft
			}
			post("/max", fmt.Sprintf(`{"update_type":%q,"timestamp":%d,"chat_id":-201,"user":{"user_id":7,"first_name":"Member"}}`, maxEvent, id))
			select {
			case upd := <-maxUpdates:
				data, _ := json.Marshal(upd)
				if !strings.Contains(string(data), maxEvent) {
					t.Fatal("MAX event decoding lost type")
				}
				b.relayMaxServiceMessage(ctx, kind, -201, int64(id), maxschemes.User{UserId: 7, FirstName: "Member"}, 0)
				b.relayMaxServiceMessage(ctx, kind, -201, int64(id), maxschemes.User{UserId: 7, FirstName: "Member"}, 0)
			case <-time.After(2 * time.Second):
				t.Fatal("MAX webhook did not reach adapter")
			}
		}
		want := int64(0)
		if state.deliver {
			want = 2
		}
		if tgCalls.Load()-beforeTG != want || maxCalls.Load()-beforeMax != want {
			t.Fatalf("state %d calls TG=%d MAX=%d want=%d", n, tgCalls.Load()-beforeTG, maxCalls.Load()-beforeMax, want)
		}
	}
}
