package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode"

	maxschemes "github.com/max-messenger/max-bot-api-client-go/schemes"
)

const (
	serviceMemberJoined = "member_joined"
	serviceMemberLeft   = "member_left"
)

// ServiceMessageSettings belongs to one ordinary group pair. Missing or invalid
// legacy state is off. Turning it off preserves the user's type selection.
type ServiceMessageSettings struct {
	Enabled bool     `json:"enabled"`
	Types   []string `json:"types"`
}

func parseServiceMessageSettings(raw string) ServiceMessageSettings {
	var settings ServiceMessageSettings
	if json.Unmarshal([]byte(raw), &settings) != nil {
		return ServiceMessageSettings{}
	}
	return settings
}

func (s ServiceMessageSettings) allows(kind string) bool {
	if !s.Enabled || (kind != serviceMemberJoined && kind != serviceMemberLeft) {
		return false
	}
	for _, selected := range s.Types {
		if selected == kind {
			return true
		}
	}
	return false
}

func (r *sqliteRepo) PairServiceMessages(tgChatID, maxChatID int64) ServiceMessageSettings {
	var raw string
	_ = r.db.QueryRow(`SELECT service_messages FROM pairs WHERE tg_chat_id=? AND max_chat_id=?`, tgChatID, maxChatID).Scan(&raw)
	return parseServiceMessageSettings(raw)
}

func (r *pgRepo) PairServiceMessages(tgChatID, maxChatID int64) ServiceMessageSettings {
	var raw string
	_ = r.db.QueryRow(`SELECT service_messages FROM pairs WHERE tg_chat_id=$1 AND max_chat_id=$2`, tgChatID, maxChatID).Scan(&raw)
	return parseServiceMessageSettings(raw)
}

func (b *Bridge) serviceMessageAllowed(ctx context.Context, tgChatID, maxChatID int64, flow, kind string) bool {
	return b.repo.PairServiceMessages(tgChatID, maxChatID).allows(kind) &&
		!b.repo.PairPaused(tgChatID, maxChatID) &&
		b.pairDirectionAllows(ctx, tgChatID, maxChatID, flow) &&
		b.pairDeliverable(ctx, tgChatID, maxChatID)
}

// Render plain text, never forwardMessage or HTML assembled from profile names.
// An actor is attributed only if supplied by the source event and distinct from
// the member. MAX supplies only an actor ID; do not invent a display name.
func serviceMemberName(name string, id int64) string {
	name = strings.Join(strings.FieldsFunc(name, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }), " ")
	runes := []rune(name)
	if len(runes) > 160 {
		name = string(runes[:160])
	}
	if name == "" {
		return fmt.Sprintf("Участник (ID %d)", id)
	}
	return name
}

func serviceMemberText(platform, kind, name string, userID, actorID int64, actorName string) string {
	member := serviceMemberName(name, userID)
	prefix := "[" + platform + "] "
	if actorID > 0 && actorID != userID {
		actor := serviceMemberName(actorName, actorID)
		if kind == serviceMemberJoined {
			return prefix + actor + " добавил(а) участника: " + member + "."
		}
		return prefix + actor + " удалил(а) участника: " + member + "."
	}
	if kind == serviceMemberJoined {
		return prefix + member + " вступил(а) в чат."
	}
	return prefix + member + " покинул(а) чат или был(а) удалён(а)."
}

func (b *Bridge) relayTgServiceMessage(ctx context.Context, msg *TGMessage) {
	if msg == nil || b.serviceSelfTgBot(msg.From) {
		return
	}
	maxChatID, linked := b.repo.GetThreadMaxChat(msg.Chat.ID, msg.MessageThreadID)
	if !linked {
		maxChatID, linked = b.repo.GetMaxChat(msg.Chat.ID)
	}
	if !linked {
		return
	}
	kind := serviceMemberJoined
	members := msg.NewChatMembers
	if msg.LeftChatMember != nil {
		kind, members = serviceMemberLeft, []UserInfo{*msg.LeftChatMember}
	}
	if len(members) == 0 || !b.serviceMessageAllowed(ctx, msg.Chat.ID, maxChatID, "tg>max", kind) {
		return
	}
	var actorID int64
	var actorName string
	// sender_chat represents an anonymous identity, not a known inviting user.
	if msg.From != nil && msg.SenderChat == nil && !isTgServiceSender(msg.From.ID) {
		actorID, actorName = msg.From.ID, strings.TrimSpace(msg.From.FirstName+" "+msg.From.LastName)
	}
	for _, member := range members {
		if member.ID <= 0 || b.serviceSelfTgBot(&member) {
			continue
		}
		key := fmt.Sprintf("service:tg:%d:%d:%d:%s:%d", msg.Chat.ID, maxChatID, msg.MessageID, kind, member.ID)
		if b.serviceMessageDuplicate(key) {
			continue
		}
		text := serviceMemberText("TG", kind, strings.TrimSpace(member.FirstName+" "+member.LastName), member.ID, actorID, actorName)
		if _, err := b.sendMaxDirect(ctx, maxChatID, text, "", "", ""); err != nil {
			slog.Warn("service notification TG to MAX failed", "tgChat", msg.Chat.ID, "maxChat", maxChatID, "err", err)
		}
	}
}

func (b *Bridge) serviceSelfTgBot(user *UserInfo) bool {
	return user != nil && ((user.ID > 0 && user.ID == b.tg.BotID()) || b.isSelfTgBot(user))
}

func (b *Bridge) relayMaxServiceMessage(ctx context.Context, kind string, chatID, timestamp int64, user maxschemes.User, actorID int64) {
	if user.UserId <= 0 || b.isSelfMaxBot(user.UserId) || (actorID > 0 && b.isSelfMaxBot(actorID)) {
		return
	}
	name := strings.TrimSpace(user.FirstName + " " + user.LastName)
	if name == "" {
		name = user.Name
	}
	text := serviceMemberText("MAX", kind, name, user.UserId, actorID, "")
	// The same fan-out as ordinary MAX messages; per-pair direction and policy.
	for _, tgChatID := range b.repo.GetTgChats(chatID) {
		if !b.serviceMessageAllowed(ctx, tgChatID, chatID, "max>tg", kind) {
			continue
		}
		key := fmt.Sprintf("service:max:%d:%d:%d:%s:%d", chatID, tgChatID, timestamp, kind, user.UserId)
		if b.serviceMessageDuplicate(key) {
			continue
		}
		threadID := b.repo.GetTgThreadID(tgChatID)
		if _, tid, ok := b.repo.GetThreadTgPair(chatID); ok {
			threadID = tid
		}
		if _, err := b.tg.SendMessage(ctx, tgChatID, text, &SendOpts{ThreadID: threadID}); err != nil {
			slog.Warn("service notification MAX to TG failed", "tgChat", tgChatID, "maxChat", chatID, "err", err)
		}
	}
}

// Webhook retries and dual-bot updates share the same bounded dedup window.
// Unlike ordinary MAX mid dedup, this also runs in single-bot/polling mode.
func (b *Bridge) serviceMessageDuplicate(key string) bool {
	b.maxSeenMu.Lock()
	defer b.maxSeenMu.Unlock()
	if b.maxSeenMid == nil {
		b.maxSeenMid = make(map[string]int64)
	}
	now := time.Now().Unix()
	if seen, ok := b.maxSeenMid[key]; ok && now-seen < 600 {
		return true
	}
	for key, seen := range b.maxSeenMid {
		if now-seen >= 600 {
			delete(b.maxSeenMid, key)
		}
	}
	b.maxSeenMid[key] = now
	return false
}
