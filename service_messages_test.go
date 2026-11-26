package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
	maxbot "github.com/max-messenger/max-bot-api-client-go"
	maxschemes "github.com/max-messenger/max-bot-api-client-go/schemes"
)

func TestServiceMembershipSubscriptionTypes(t *testing.T) {
	if !slices.Contains(telegramAllowedUpdates(), "message") {
		t.Fatal("Telegram service messages require message updates")
	}
	for _, kind := range []string{"user_added", "user_removed"} {
		if !slices.Contains(maxWebhookUpdateTypes(), kind) {
			t.Fatalf("MAX webhook missing %s", kind)
		}
	}
}

func TestTelegramServicePollingReceivesMembershipUpdates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/bot123:TEST/getMe":
			fmt.Fprint(w, `{"ok":true,"result":{"id":123,"is_bot":true,"first_name":"Test","username":"bridge_test_bot"}}`)
		case "/bot123:TEST/getUpdates":
			if calls.Add(1) > 1 {
				<-ctx.Done()
				return
			}
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Error(err)
			}
			var allowed []string
			if json.Unmarshal([]byte(r.FormValue("allowed_updates")), &allowed) != nil || !slices.Contains(allowed, "message") {
				t.Error("polling did not request message updates")
			}
			fmt.Fprint(w, `{"ok":true,"result":[{"update_id":1,"message":{"message_id":1,"chat":{"id":-101,"type":"supergroup"},"new_chat_members":[{"id":7,"first_name":"Анна"}]}},{"update_id":2,"message":{"message_id":2,"chat":{"id":-101,"type":"supergroup"},"left_chat_member":{"id":7,"first_name":"Анна"}}}]}`)
		default:
			t.Errorf("unexpected Telegram API method %s", r.URL.Path)
		}
	}))
	defer func() { cancel(); srv.Close() }()
	sender, err := NewTGBotSender(ctx, "123:TEST", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	updates := sender.StartPolling(ctx)
	var joins, leaves int
	for i := 0; i < 2; i++ {
		select {
		case update := <-updates:
			if update.Message == nil || !update.Message.IsService {
				t.Fatalf("service update lost: %+v", update)
			}
			if len(update.Message.NewChatMembers) == 1 {
				joins++
			}
			if update.Message.LeftChatMember != nil {
				leaves++
			}
		case <-time.After(2 * time.Second):
			t.Fatal("polling did not deliver membership updates")
		}
	}
	if joins != 1 || leaves != 1 {
		t.Fatalf("joins=%d leaves=%d", joins, leaves)
	}
	cancel()
}

func TestServiceMessagesPostgres(t *testing.T) {
	dsn := os.Getenv("SERVICE_MESSAGE_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set SERVICE_MESSAGE_TEST_POSTGRES_DSN to an isolated empty test database")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := runMigrations(db, "postgres"); err != nil {
		t.Fatal(err)
	}
	r := &pgRepo{db: db}
	if _, err := db.Exec(`INSERT INTO pairs(tg_chat_id,max_chat_id) VALUES(-101,-201),(-102,-201)`); err != nil {
		t.Fatal(err)
	}
	if r.PairServiceMessages(-101, -201).Enabled {
		t.Fatal("postgres default on")
	}
	if _, err := db.Exec(`UPDATE pairs SET service_messages=$1 WHERE tg_chat_id=-101`, `{"enabled":true,"types":["member_joined"]}`); err != nil {
		t.Fatal(err)
	}
	if !r.PairServiceMessages(-101, -201).allows(serviceMemberJoined) || r.PairServiceMessages(-102, -201).Enabled {
		t.Fatal("postgres pair scope")
	}
	if !r.Unpair("tg", -101) || r.PairServiceMessages(-101, -201).Enabled {
		t.Fatal("postgres lifecycle")
	}
}

type serviceTGSender struct {
	TGSender
	sends []struct {
		chat int64
		text string
		opts SendOpts
	}
}

func (s *serviceTGSender) BotID() int64        { return 999 }
func (s *serviceTGSender) BotUsername() string { return "bridge_test_bot" }
func (s *serviceTGSender) SendMessage(_ context.Context, chat int64, text string, opts *SendOpts) (int, error) {
	s.sends = append(s.sends, struct {
		chat int64
		text string
		opts SendOpts
	}{chat, text, *opts})
	return len(s.sends), nil
}

type servicePolicy struct {
	Addon
	direction string
	blocked   bool
}

func (s servicePolicy) PairDirection(context.Context, int64, int64) string { return s.direction }
func (s servicePolicy) PairDeliverable(context.Context, int64, int64, int64, int64) bool {
	return !s.blocked
}

func setServiceSettings(t *testing.T, repo *sqliteRepo, tg, max int64, raw string) {
	t.Helper()
	if _, err := repo.db.Exec(`UPDATE pairs SET service_messages=? WHERE tg_chat_id=? AND max_chat_id=?`, raw, tg, max); err != nil {
		t.Fatal(err)
	}
}

func TestServiceSettingsLegacyNewPairAndLifecycle(t *testing.T) {
	repo := testRepo(t)
	// Recreate a pre-feature state with a real existing pair, then migrate it.
	insertPair(t, repo, -101, -201, 42)
	if _, err := repo.db.Exec(`ALTER TABLE pairs DROP COLUMN service_messages; UPDATE schema_migrations SET version=26`); err != nil {
		t.Fatal(err)
	}
	if err := runMigrations(repo.db, "sqlite3"); err != nil {
		t.Fatal(err)
	}
	insertPair(t, repo, -102, -201, 43)
	for _, chat := range []int64{-101, -102} {
		if repo.PairServiceMessages(chat, -201).allows(serviceMemberJoined) {
			t.Fatal("existing/new pair enabled by default")
		}
	}
	setServiceSettings(t, repo, -101, -201, `{"enabled":true,"types":["member_joined"]}`)
	if !repo.PairServiceMessages(-101, -201).allows(serviceMemberJoined) || repo.PairServiceMessages(-102, -201).allows(serviceMemberJoined) {
		t.Fatal("pair isolation")
	}
	if !repo.Unpair("tg", -101) {
		t.Fatal("unpair")
	}
	insertPair(t, repo, -101, -201, 42)
	if repo.PairServiceMessages(-101, -201).Enabled {
		t.Fatal("recreated pair inherited settings")
	}
}

func TestServiceSettingsSelection(t *testing.T) {
	for _, tc := range []struct {
		raw        string
		join, left bool
	}{
		{"", false, false}, {"garbled", false, false}, {`{"enabled":true,"types":[]}`, false, false},
		{`{"enabled":false,"types":["member_joined","member_left"]}`, false, false},
		{`{"enabled":true,"types":["member_joined"]}`, true, false},
		{`{"enabled":true,"types":["member_left"]}`, false, true},
		{`{"enabled":true,"types":["member_joined","member_left"]}`, true, true},
		{`{"enabled":true,"types":["unknown"]}`, false, false},
	} {
		settings := parseServiceMessageSettings(tc.raw)
		if settings.allows(serviceMemberJoined) != tc.join || settings.allows(serviceMemberLeft) != tc.left || settings.allows("unknown") {
			t.Fatalf("selection %s", tc.raw)
		}
	}
}

func TestServiceMemberTextAttribution(t *testing.T) {
	for _, tc := range []struct {
		kind            string
		actor           int64
		actorName, want string
	}{
		{serviceMemberJoined, 5, "Ирина", "Ирина добавил(а) участника: Анна"},
		{serviceMemberJoined, 7, "Анна", "Анна вступил(а) в чат"},
		{serviceMemberJoined, 0, "", "Анна вступил(а) в чат"},
		{serviceMemberLeft, 5, "", "Участник (ID 5) удалил(а) участника: Анна"},
		{serviceMemberLeft, 0, "", "Анна покинул(а) чат или был(а) удалён(а)"},
	} {
		got := serviceMemberText("MAX", tc.kind, "Анна", 7, tc.actor, tc.actorName)
		if !strings.Contains(got, tc.want) {
			t.Fatalf("got %q, want %q", got, tc.want)
		}
	}
}

func TestTgServiceRelayMultipleMembersPolicyAndSelf(t *testing.T) {
	repo := testRepo(t)
	insertPair(t, repo, -101, -201, 42)
	tg := &serviceTGSender{}
	var payloads []string
	b := &Bridge{repo: repo, tg: tg, cfg: Config{MaxToken: "local-test"}, apiClient: &http.Client{Transport: crosspostEditRoundTripper(func(req *http.Request) (*http.Response, error) {
		if req.Method != "POST" || req.URL.Path != "/messages" || req.URL.Query().Get("chat_id") != "-201" {
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL)
		}
		var body struct {
			Text   string
			Format string
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Format != "" {
			t.Fatal("profile names must remain plain text")
		}
		payloads = append(payloads, body.Text)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"message":{"body":{"mid":"local-mid"}}}`))}, nil
	})}}
	msg := convertMsg(&models.Message{ID: 10, Chat: models.Chat{ID: -101, Type: models.ChatTypeSupergroup}, From: &models.User{ID: 5, FirstName: "Ирина"}, NewChatMembers: []models.User{{ID: 7, FirstName: "Анна <b>"}, {ID: 8, FirstName: "Борис"}, {ID: 999, IsBot: true, Username: "bridge_test_bot"}}})
	if !msg.IsService || len(msg.NewChatMembers) != 3 {
		t.Fatal("TG adapter lost membership data")
	}
	b.relayTgServiceMessage(context.Background(), msg)
	if len(payloads) != 0 {
		t.Fatal("default must be off")
	}
	setServiceSettings(t, repo, -101, -201, `{"enabled":true,"types":[]}`)
	b.relayTgServiceMessage(context.Background(), msg)
	if len(payloads) != 0 {
		t.Fatal("empty selection must be off")
	}
	setServiceSettings(t, repo, -101, -201, `{"enabled":true,"types":["member_joined"]}`)
	b.addon = servicePolicy{direction: "max>tg"}
	b.relayTgServiceMessage(context.Background(), msg)
	b.addon = servicePolicy{direction: "both", blocked: true}
	b.relayTgServiceMessage(context.Background(), msg)
	b.addon = nil
	if err := repo.SetPairPaused(-101, -201, true); err != nil {
		t.Fatal(err)
	}
	b.relayTgServiceMessage(context.Background(), msg)
	if len(payloads) != 0 {
		t.Fatal("direction/access/pause bypassed")
	}
	_ = repo.SetPairPaused(-101, -201, false)
	b.relayTgServiceMessage(context.Background(), msg)
	b.relayTgServiceMessage(context.Background(), msg)
	if len(payloads) != 2 || !strings.Contains(payloads[0], "Ирина добавил(а) участника: Анна <b>") {
		t.Fatalf("multiple members, text or dedup: %v", payloads)
	}
	msg.MessageID++
	msg.From = &UserInfo{ID: 999, IsBot: true, UserName: "renamed_bot"}
	b.relayTgServiceMessage(context.Background(), msg)
	if len(payloads) != 2 {
		t.Fatal("own bot action relayed")
	}
	msg.MessageID++
	msg.From = nil
	msg.NewChatMembers = nil
	msg.LeftChatMember = &UserInfo{ID: 7, FirstName: "Анна"}
	b.relayTgServiceMessage(context.Background(), msg)
	if len(payloads) != 2 {
		t.Fatal("unselected leave relayed")
	}
	setServiceSettings(t, repo, -101, -201, `{"enabled":true,"types":["member_left"]}`)
	b.relayTgServiceMessage(context.Background(), msg)
	if len(payloads) != 3 || strings.Contains(payloads[2], "добавил") {
		t.Fatal(payloads)
	}
	// An explicit thread route must not leak to the parent group's destination.
	_, err := repo.db.Exec(`INSERT INTO thread_pairs(tg_chat_id,tg_thread_id,max_chat_id,created_at) VALUES(-101,12,-202,0)`)
	if err != nil {
		t.Fatal(err)
	}
	msg.MessageID++
	msg.MessageThreadID = 12
	b.relayTgServiceMessage(context.Background(), msg)
	if len(payloads) != 3 {
		t.Fatal("thread override ignored")
	}
}

func TestMaxServiceRelayFanoutThreadTypesAndSelf(t *testing.T) {
	repo := testRepo(t)
	insertPair(t, repo, -101, -201, 42)
	insertPair(t, repo, -102, -201, 43)
	setServiceSettings(t, repo, -101, -201, `{"enabled":true,"types":["member_joined","member_left"]}`)
	setServiceSettings(t, repo, -102, -201, `{"enabled":true,"types":["member_left"]}`)
	if err := repo.SetTgThreadID(-101, 15); err != nil {
		t.Fatal(err)
	}
	tg := &serviceTGSender{}
	b := &Bridge{repo: repo, tg: tg, maxBotUID: 999, maxOldUID: 998}
	member := maxschemes.User{UserId: 7, FirstName: "Анна"}
	ctx := context.Background()
	b.relayMaxServiceMessage(ctx, serviceMemberJoined, -201, 100, member, 0)
	b.relayMaxServiceMessage(ctx, serviceMemberJoined, -201, 100, member, 0)
	if len(tg.sends) != 1 || tg.sends[0].chat != -101 || tg.sends[0].opts.ThreadID != 15 || tg.sends[0].opts.ParseMode != "" || !strings.Contains(tg.sends[0].text, "вступил") {
		t.Fatalf("join/fanout: %+v", tg.sends)
	}
	b.relayMaxServiceMessage(ctx, serviceMemberLeft, -201, 101, member, 5)
	if len(tg.sends) != 3 {
		t.Fatal("selected leave did not fan out")
	}
	b.relayMaxServiceMessage(ctx, serviceMemberLeft, -201, 102, member, 999)
	b.relayMaxServiceMessage(ctx, serviceMemberJoined, -201, 103, maxschemes.User{UserId: 998}, 0)
	if len(tg.sends) != 3 {
		t.Fatal("own actions/members relayed")
	}
	b.addon = servicePolicy{direction: "tg>max"}
	b.relayMaxServiceMessage(ctx, serviceMemberLeft, -201, 104, member, 0)
	b.addon = servicePolicy{direction: "both", blocked: true}
	b.relayMaxServiceMessage(ctx, serviceMemberLeft, -201, 105, member, 0)
	if len(tg.sends) != 3 {
		t.Fatal("MAX direction/access bypassed")
	}
}

func TestMaxSDKMembershipWebhookAndPolling(t *testing.T) {
	for _, transport := range []string{"webhook", "polling"} {
		t.Run(transport, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var calls atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/updates" || r.URL.Query().Get("types") != "" {
					t.Errorf("unexpected poll %s", r.URL)
				}
				if calls.Add(1) == 1 {
					fmt.Fprint(w, `{"updates":[{"update_type":"user_added","timestamp":1,"chat_id":-201,"user":{"user_id":7,"first_name":"Анна"},"inviter_id":null},{"update_type":"user_removed","timestamp":2,"chat_id":-201,"user":{"user_id":7,"first_name":"Анна"},"admin_id":5}],"marker":2}`)
				} else {
					fmt.Fprint(w, `{"updates":[]}`)
				}
			}))
			defer srv.Close()
			api, err := maxbot.New("test-token", maxbot.WithBaseURL(srv.URL+"/"), maxbot.WithPauseTimeout(time.Millisecond))
			if err != nil {
				t.Fatal(err)
			}
			var updates <-chan maxschemes.UpdateInterface
			if transport == "polling" {
				updates = api.GetUpdates(ctx)
			} else {
				ch := make(chan maxschemes.UpdateInterface, 2)
				updates = ch
				handler := api.GetHandler(ch)
				for _, raw := range []string{`{"update_type":"user_added","timestamp":1,"chat_id":-201,"user":{"user_id":7,"first_name":"Анна"}}`, `{"update_type":"user_removed","timestamp":2,"chat_id":-201,"user":{"user_id":7},"admin_id":null}`} {
					rec := httptest.NewRecorder()
					handler(rec, httptest.NewRequest("POST", "/", strings.NewReader(raw)))
					if rec.Code != http.StatusOK {
						t.Fatal(rec.Code)
					}
				}
			}
			for i := 0; i < 2; i++ {
				select {
				case update := <-updates:
					if i == 0 {
						event, ok := update.(*maxschemes.UserAddedToChatUpdate)
						if !ok || event.InviterId != 0 || event.User.UserId != 7 {
							t.Fatalf("added: %#v", update)
						}
					} else {
						if _, ok := update.(*maxschemes.UserRemovedFromChatUpdate); !ok {
							t.Fatalf("removed: %#v", update)
						}
					}
				case <-time.After(2 * time.Second):
					t.Fatal("SDK did not deliver update")
				}
			}
		})
	}
}
