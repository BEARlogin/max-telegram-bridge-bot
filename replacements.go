package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"

	maxbot "github.com/max-messenger/max-bot-api-client-go"
	maxschemes "github.com/max-messenger/max-bot-api-client-go/schemes"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// parseCrosspostReplacements парсит JSON из БД в структуру.
func parseCrosspostReplacements(raw string) CrosspostReplacements {
	if raw == "" {
		return CrosspostReplacements{}
	}
	var r CrosspostReplacements
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		slog.Warn("failed to parse replacements", "err", err)
		return CrosspostReplacements{}
	}
	return r
}

// marshalCrosspostReplacements сериализует структуру в JSON.
func marshalCrosspostReplacements(r CrosspostReplacements) string {
	if len(r.TgToMax) == 0 && len(r.MaxToTg) == 0 {
		return ""
	}
	data, _ := json.Marshal(r)
	return string(data)
}

// urlRegex матчит URL в тексте.
var urlRegex = regexp.MustCompile(`https?://[^\s<>"]+`)

// applyReplacements применяет список замен к тексту.
func applyReplacements(text string, rules []Replacement) string {
	for _, r := range rules {
		if r.From == "" {
			continue
		}
		if r.Target == "links" {
			text = applyToLinks(text, r)
		} else {
			text = applyToAll(text, r)
		}
	}
	return text
}

func applyToAll(text string, r Replacement) string {
	if r.Regex {
		re, err := regexp.Compile(r.From)
		if err != nil {
			slog.Warn("invalid replacement regex", "pattern", r.From, "err", err)
			return text
		}
		return re.ReplaceAllString(text, r.To)
	}
	return strings.ReplaceAll(text, r.From, r.To)
}

func applyToLinks(text string, r Replacement) string {
	return urlRegex.ReplaceAllStringFunc(text, func(url string) string {
		if r.Regex {
			re, err := regexp.Compile(r.From)
			if err != nil {
				return url
			}
			return re.ReplaceAllString(url, r.To)
		}
		return strings.ReplaceAll(url, r.From, r.To)
	})
}

// formatReplacementItem форматирует одну замену для отдельного сообщения.
func formatReplacementItem(r Replacement, dir string) string {
	dirLabel := "TG → MAX"
	if dir == "max>tg" {
		dirLabel = "MAX → TG"
	}
	targetLabel := "весь текст"
	if r.Target == "links" {
		targetLabel = "только ссылки"
	}
	return fmt.Sprintf("%s %s\n<code>%s</code> → <code>%s</code>\nТип: %s", dirLabel, replacementTags(r), r.From, r.To, targetLabel)
}

// formatReplacementsHeader формирует заголовок для списка замен.
func formatReplacementsHeader(repl CrosspostReplacements) string {
	total := len(repl.TgToMax) + len(repl.MaxToTg)
	if total == 0 {
		return "🔄 Замен нет.\n\nДобавьте замену — текст в пересылаемых постах будет автоматически заменяться."
	}
	return fmt.Sprintf("🔄 Замены (%d):", total)
}

// replacementTags возвращает теги для отображения замены.
func replacementTags(r Replacement) string {
	var tags []string
	if r.Regex {
		tags = append(tags, "regex")
	}
	if r.Target == "links" {
		tags = append(tags, "ссылки")
	}
	if len(tags) == 0 {
		return ""
	}
	return "[" + strings.Join(tags, ", ") + "] "
}

// tgReplacementsKeyboard строит inline-клавиатуру для управления заменами.
func tgReplacementsKeyboard(maxChatID int64) tgbotapi.InlineKeyboardMarkup {
	id := fmt.Sprintf("%d", maxChatID)
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("+ TG→MAX", "cpra:tg>max:"+id),
			tgbotapi.NewInlineKeyboardButtonData("+ MAX→TG", "cpra:max>tg:"+id),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🗑 Очистить всё", "cprc:"+id),
			tgbotapi.NewInlineKeyboardButtonData("◀ Назад", "cprb:"+id),
		),
	)
}

// tgReplItemKeyboard — кнопки для одной замены в TG.
func tgReplItemKeyboard(dir string, idx int, maxChatID string, currentTarget string) tgbotapi.InlineKeyboardMarkup {
	toggleLabel := "🔗 Только ссылки"
	toggleTarget := "links"
	if currentTarget == "links" {
		toggleLabel = "📝 Весь текст"
		toggleTarget = "all"
	}
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(toggleLabel, fmt.Sprintf("cprt:%s:%d:%s:%s", dir, idx, toggleTarget, maxChatID)),
			tgbotapi.NewInlineKeyboardButtonData("❌ Удалить", fmt.Sprintf("cprd:%s:%d:%s", dir, idx, maxChatID)),
		),
	)
}

// maxReplacementsKeyboard строит inline-клавиатуру для управления заменами в MAX.
func maxReplacementsKeyboard(api *maxbot.Api, maxChatID int64) *maxbot.Keyboard {
	id := fmt.Sprintf("%d", maxChatID)
	kb := api.Messages.NewKeyboardBuilder()
	kb.AddRow().
		AddCallback("+ TG→MAX", maxschemes.DEFAULT, "cpra:tg>max:"+id).
		AddCallback("+ MAX→TG", maxschemes.DEFAULT, "cpra:max>tg:"+id)
	kb.AddRow().
		AddCallback("🗑 Очистить всё", maxschemes.NEGATIVE, "cprc:"+id).
		AddCallback("◀ Назад", maxschemes.DEFAULT, "cprb:"+id)
	return kb
}

// maxReplItemKeyboard — кнопки для одной замены в MAX.
func maxReplItemKeyboard(api *maxbot.Api, dir string, idx int, maxChatID string, currentTarget string) *maxbot.Keyboard {
	toggleLabel := "🔗 Только ссылки"
	toggleTarget := "links"
	if currentTarget == "links" {
		toggleLabel = "📝 Весь текст"
		toggleTarget = "all"
	}
	kb := api.Messages.NewKeyboardBuilder()
	kb.AddRow().
		AddCallback(toggleLabel, maxschemes.DEFAULT, fmt.Sprintf("cprt:%s:%d:%s:%s", dir, idx, toggleTarget, maxChatID)).
		AddCallback("❌ Удалить", maxschemes.NEGATIVE, fmt.Sprintf("cprd:%s:%d:%s", dir, idx, maxChatID))
	return kb
}

// replWait хранит состояние ожидания ввода замены.
type replWait struct {
	maxChatID int64
	direction string // "tg>max" or "max>tg"
	target    string // "all" or "links"
}

// replWaitMap — глобальное хранилище ожиданий (по userID).
var (
	replWaits   = make(map[int64]replWait)
	replWaitsMu sync.Mutex
)

func (b *Bridge) setReplWait(userID, maxChatID int64, direction, target string) {
	replWaitsMu.Lock()
	replWaits[userID] = replWait{maxChatID: maxChatID, direction: direction, target: target}
	replWaitsMu.Unlock()
}

func (b *Bridge) getReplWait(userID int64) (replWait, bool) {
	replWaitsMu.Lock()
	w, ok := replWaits[userID]
	replWaitsMu.Unlock()
	return w, ok
}

func (b *Bridge) clearReplWait(userID int64) {
	replWaitsMu.Lock()
	delete(replWaits, userID)
	replWaitsMu.Unlock()
}

// parseReplacementInput парсит ввод пользователя "from | to" или "/regex/ | to".
func parseReplacementInput(input string) (Replacement, bool) {
	idx := strings.Index(input, "|")
	if idx < 0 {
		return Replacement{}, false
	}

	from := strings.TrimSpace(input[:idx])
	to := strings.TrimSpace(input[idx+1:])

	if from == "" {
		return Replacement{}, false
	}

	// Regex: /pattern/
	isRegex := false
	if len(from) >= 2 && from[0] == '/' && from[len(from)-1] == '/' {
		from = from[1 : len(from)-1]
		isRegex = true
		// Проверяем что regex валидный
		if _, err := regexp.Compile(from); err != nil {
			return Replacement{}, false
		}
	}

	return Replacement{From: from, To: to, Regex: isRegex}, true
}
