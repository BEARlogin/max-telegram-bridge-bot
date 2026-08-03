package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"
	"unicode"

	maxbot "github.com/max-messenger/max-bot-api-client-go"
)

var errInvalidDoctorPrincipal = errors.New("invalid doctor principal")

var doctorMSK = time.FixedZone("МСК", 3*60*60)

const doctorRateWindow = 10 * time.Second

// DoctorConnection contains delivery metadata only. Message text, captions,
// attachment names and URLs must never be selected for this report.
type DoctorConnection struct {
	Kind      string // bridge | crosspost
	TgChatID  int64
	MaxChatID int64
	TgTitle   string
	MaxTitle  string
	Direction string
	Paused    bool
	CreatedAt int64

	LastTgToMax  int64
	LastMaxToTg  int64
	TodayTgToMax int
	TodayMaxToTg int

	PendingTgToMax int
	PendingMaxToTg int
	OldestPending  int64
	MaxAttempts    int
	RuntimeStatus  string
}

func doctorDayStart(now time.Time) int64 {
	local := now.In(doctorMSK)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, doctorMSK).Unix()
}

func doctorFormatTime(unix int64) string {
	if unix <= 0 {
		return "не было"
	}
	return time.Unix(unix, 0).In(doctorMSK).Format("02.01.2006 15:04 МСК")
}

func doctorConnectedAt(unix int64) string {
	if unix <= 0 {
		return "да (дата создания не сохранена)"
	}
	return doctorFormatTime(unix)
}

func doctorSafeTitle(title string) string {
	title = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, title)
	title = strings.Join(strings.Fields(title), " ")
	runes := []rune(title)
	if len(runes) > 80 {
		title = string(runes[:79]) + "…"
	}
	return title
}

func doctorChatLabel(title string, chatID int64) string {
	title = doctorSafeTitle(title)
	if title == "" {
		return fmt.Sprintf("%d", chatID)
	}
	return fmt.Sprintf("«%s» (%d)", title, chatID)
}

func doctorDirectionLabel(direction string) string {
	switch normalizePairDirection(direction) {
	case "tg>max":
		return "TG → MAX"
	case "max>tg":
		return "MAX → TG"
	default:
		return "TG ↔ MAX"
	}
}

func doctorConnectionStatus(c DoctorConnection) string {
	if c.RuntimeStatus != "" {
		return c.RuntimeStatus
	}
	if c.Paused {
		return "⏸ на паузе"
	}
	pending := c.PendingTgToMax + c.PendingMaxToTg
	if pending > 0 {
		return fmt.Sprintf("⚠️ ожидают повторной отправки: %d", pending)
	}
	return "✅ активна"
}

func doctorConnectionKind(kind string) string {
	if kind == "crosspost" {
		return "Каналы"
	}
	return "Группы"
}

func formatDoctorReport(now time.Time, connections []DoctorConnection) string {
	var out strings.Builder
	fmt.Fprintf(&out, "🩺 Doctor · %s\n", now.In(doctorMSK).Format("02.01.2006 15:04 МСК"))

	if len(connections) == 0 {
		out.WriteString("\nАктивных подключений с подтверждённым владельцем не найдено.\n")
		out.WriteString("Для старой связи выполните /bridge_update в группе или перешлите пост связанного канала боту.")
		return out.String()
	}

	for i, c := range connections {
		fmt.Fprintf(&out, "\n%d. %s\n", i+1, doctorConnectionKind(c.Kind))
		fmt.Fprintf(&out, "TG: %s\n", doctorChatLabel(c.TgTitle, c.TgChatID))
		fmt.Fprintf(&out, "MAX: %s\n", doctorChatLabel(c.MaxTitle, c.MaxChatID))
		fmt.Fprintf(&out, "Статус: %s\n", doctorConnectionStatus(c))
		fmt.Fprintf(&out, "Направление: %s\n", doctorDirectionLabel(c.Direction))
		fmt.Fprintf(&out, "Связано: %s\n", doctorConnectedAt(c.CreatedAt))
		fmt.Fprintf(&out, "Сегодня: TG→MAX — %d; MAX→TG — %d\n", c.TodayTgToMax, c.TodayMaxToTg)
		fmt.Fprintf(&out, "Последняя TG→MAX: %s\n", doctorFormatTime(c.LastTgToMax))
		fmt.Fprintf(&out, "Последняя MAX→TG: %s\n", doctorFormatTime(c.LastMaxToTg))
		if c.PendingTgToMax+c.PendingMaxToTg == 0 {
			out.WriteString("Очередь: пусто\n")
		} else {
			fmt.Fprintf(&out, "Очередь: TG→MAX — %d; MAX→TG — %d; с %s; попыток до %d\n",
				c.PendingTgToMax, c.PendingMaxToTg, doctorFormatTime(c.OldestPending), c.MaxAttempts)
		}
	}
	return strings.TrimSpace(out.String())
}

func (b *Bridge) doctorReport(ctx context.Context, platform string, userID int64, now time.Time) (string, error) {
	ownerIDs := []int64{userID}
	if h, ok := b.addon.(interface {
		DoctorBillingOwnerIDs(context.Context, string, int64) []int64
	}); ok {
		ownerIDs = append(ownerIDs, h.DoctorBillingOwnerIDs(ctx, platform, userID)...)
	}
	connections := make([]DoctorConnection, 0)
	seenOwners := make(map[int64]struct{}, len(ownerIDs))
	seenConnections := make(map[string]struct{})
	for _, ownerID := range ownerIDs {
		if ownerID <= 0 {
			continue
		}
		if _, seen := seenOwners[ownerID]; seen {
			continue
		}
		seenOwners[ownerID] = struct{}{}
		owned, err := b.repo.DoctorConnections(platform, ownerID, doctorDayStart(now))
		if err != nil {
			return "", err
		}
		for _, connection := range owned {
			key := fmt.Sprintf("%s:%d:%d", connection.Kind, connection.TgChatID, connection.MaxChatID)
			if _, seen := seenConnections[key]; seen {
				continue
			}
			seenConnections[key] = struct{}{}
			connections = append(connections, connection)
		}
	}
	for i := range connections {
		if connections[i].Kind == "bridge" {
			connections[i].Direction = b.pairDirection(ctx, connections[i].TgChatID, connections[i].MaxChatID)
		} else if connections[i].Kind == "crosspost" {
			connections[i].RuntimeStatus = b.crosspostRuntimeStatus(ctx, connections[i].TgChatID, connections[i].MaxChatID)
		}
	}
	sort.SliceStable(connections, func(i, j int) bool {
		if connections[i].CreatedAt == connections[j].CreatedAt {
			return connections[i].Kind < connections[j].Kind
		}
		return connections[i].CreatedAt < connections[j].CreatedAt
	})
	return formatDoctorReport(now, connections), nil
}

func (b *Bridge) doctorTakeRate(platform string, userID int64, now time.Time) bool {
	if userID <= 0 || (platform != "tg" && platform != "max") {
		return false
	}
	key := fmt.Sprintf("%s:%d", platform, userID)
	b.doctorMu.Lock()
	defer b.doctorMu.Unlock()
	if b.doctorLast == nil {
		b.doctorLast = make(map[string]time.Time)
	}
	if last, ok := b.doctorLast[key]; ok && now.Sub(last) < doctorRateWindow {
		return false
	}
	b.doctorLast[key] = now
	for k, last := range b.doctorLast {
		if now.Sub(last) > 10*time.Minute {
			delete(b.doctorLast, k)
		}
	}
	return true
}

func (b *Bridge) sendDoctorTG(ctx context.Context, chatID, userID int64, threadID int) {
	report, err := b.doctorReport(ctx, "tg", userID, time.Now())
	if err != nil {
		slog.Warn("doctor report failed", "platform", "tg", "user_id", userID, "err", err)
		b.tg.SendMessage(ctx, chatID, "Не удалось собрать отчёт. Попробуйте позже.", &SendOpts{ThreadID: threadID})
		return
	}
	for _, chunk := range splitMaxText(report, 3500) {
		if _, err := b.tg.SendMessage(ctx, chatID, chunk, &SendOpts{ThreadID: threadID}); err != nil {
			slog.Warn("doctor report send failed", "platform", "tg", "user_id", userID, "err", err)
			return
		}
	}
}

func (b *Bridge) sendDoctorMax(ctx context.Context, chatID, userID int64) {
	report, err := b.doctorReport(ctx, "max", userID, time.Now())
	if err != nil {
		slog.Warn("doctor report failed", "platform", "max", "user_id", userID, "err", err)
		m := maxbot.NewMessage().SetChat(chatID).SetText("Не удалось собрать отчёт. Попробуйте позже.")
		_ = b.maxClientFor(ctx, chatID).Messages.Send(ctx, m)
		return
	}
	for _, chunk := range splitMaxText(report, 3500) {
		m := maxbot.NewMessage().SetChat(chatID).SetText(chunk)
		if err := b.maxClientFor(ctx, chatID).Messages.Send(ctx, m); err != nil {
			slog.Warn("doctor report send failed", "platform", "max", "user_id", userID, "err", err)
			return
		}
	}
}
