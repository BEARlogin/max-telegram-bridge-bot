package main

import (
	"context"
	"errors"
	"strings"
	"time"

	maxbot "github.com/max-messenger/max-bot-api-client-go"
)

type crosspostAccessState uint8

const (
	crosspostAccessUnknown crosspostAccessState = iota
	crosspostAccessReady
	crosspostAccessMissing
)

func (b *Bridge) tgCrosspostBotAccess(ctx context.Context, tgChatID int64) crosspostAccessState {
	if b.tg == nil || b.tg.BotID() == 0 || tgChatID == 0 {
		return crosspostAccessUnknown
	}
	status, err := b.tg.GetChatMember(ctx, tgChatID, b.tg.BotID())
	if err == nil {
		if isTgAdmin(status) {
			return crosspostAccessReady
		}
		return crosspostAccessMissing
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
		return crosspostAccessUnknown
	}
	return crosspostAccessMissing
}

func (b *Bridge) maxCrosspostBotAccess(ctx context.Context, maxChatID int64) crosspostAccessState {
	if maxChatID == 0 {
		return crosspostAccessUnknown
	}
	clients := []*maxbot.Api{b.maxApiOld, b.maxApi}
	known := false
	seen := make(map[*maxbot.Api]struct{}, len(clients))
	for _, api := range clients {
		if api == nil {
			continue
		}
		if _, duplicate := seen[api]; duplicate {
			continue
		}
		seen[api] = struct{}{}
		membership, err := api.Chats.GetChatMembership(ctx, maxChatID)
		if err != nil {
			continue
		}
		known = true
		if membership.IsAdmin {
			return crosspostAccessReady
		}
	}
	if known {
		return crosspostAccessMissing
	}
	if ctx.Err() != nil {
		return crosspostAccessUnknown
	}
	return crosspostAccessMissing
}

func crosspostHealthLabel(paused bool, tg, max crosspostAccessState) string {
	if paused {
		return "⏸ на паузе"
	}
	warnings := make([]string, 0, 2)
	unknown := false
	switch tg {
	case crosspostAccessMissing:
		warnings = append(warnings, "Telegram-бот не администратор TG-канала")
	case crosspostAccessUnknown:
		unknown = true
	}
	switch max {
	case crosspostAccessMissing:
		warnings = append(warnings, "MAX-бот не администратор MAX-канала")
	case crosspostAccessUnknown:
		unknown = true
	}
	if len(warnings) > 0 {
		return "⚠️ " + strings.Join(warnings, "; ")
	}
	if unknown {
		return "⚠️ не удалось проверить права ботов"
	}
	return "✅ работает"
}

func (b *Bridge) crosspostRuntimeStatus(ctx context.Context, tgChatID, maxChatID int64) string {
	if b.repo.CrosspostPaused(maxChatID) {
		return crosspostHealthLabel(true, crosspostAccessUnknown, crosspostAccessUnknown)
	}
	statusCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	tgResult := make(chan crosspostAccessState, 1)
	maxResult := make(chan crosspostAccessState, 1)
	go func() { tgResult <- b.tgCrosspostBotAccess(statusCtx, tgChatID) }()
	go func() { maxResult <- b.maxCrosspostBotAccess(statusCtx, maxChatID) }()

	tgState, maxState := crosspostAccessUnknown, crosspostAccessUnknown
	for received := 0; received < 2; received++ {
		select {
		case tgState = <-tgResult:
			tgResult = nil
		case maxState = <-maxResult:
			maxResult = nil
		case <-statusCtx.Done():
			return crosspostHealthLabel(false, tgState, maxState)
		}
	}
	return crosspostHealthLabel(false, tgState, maxState)
}

func (b *Bridge) appendCrosspostRuntimeStatus(ctx context.Context, text string, tgChatID, maxChatID int64) string {
	return strings.TrimSpace(text) + "\nСтатус: " + b.crosspostRuntimeStatus(ctx, tgChatID, maxChatID)
}

func (b *Bridge) maxCrosspostCardText(ctx context.Context, tgChatID int64, direction string, maxChatID int64) string {
	return b.appendCrosspostRuntimeStatus(ctx, maxCrosspostStatusText(tgChatID, direction), tgChatID, maxChatID)
}
