package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"unicode/utf16"
	"unicode/utf8"

	maxbot "github.com/max-messenger/max-bot-api-client-go"
	maxschemes "github.com/max-messenger/max-bot-api-client-go/schemes"
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
	if len(r.TgToMax) == 0 && len(r.MaxToTg) == 0 && len(r.TgToMaxExcludeContains) == 0 {
		return ""
	}
	data, _ := json.Marshal(r)
	return string(data)
}

func tgCrosspostExcluded(msg *TGMessage, filters []string) bool {
	if msg == nil {
		return false
	}
	for _, filter := range filters {
		if filter != "" && (strings.Contains(msg.Text, filter) || strings.Contains(msg.Caption, filter)) {
			return true
		}
	}
	return false
}

func tgCrosspostAlbumExcluded(items []mediaGroupItem, filters []string) bool {
	for _, item := range items {
		if tgCrosspostExcluded(item.msg, filters) {
			return true
		}
	}
	return false
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

// --- Замены на уровне (текст + entities) ---
//
// applyReplacements работает по готовому HTML, но там текст ссылки разорван <a>-тегами
// (например «ВК / MAX», где «ВК» и «MAX» — text_link), и правило по видимому тексту не
// матчит. Здесь замены применяются к ИСХОДНОМУ тексту с пересчётом offset'ов entity: если
// видимый текст ссылки вырезан правилом — сам text_link тоже удаляется. Результат потом
// идёт в tgEntitiesToHTML.

type utf16Edit struct {
	s, e int      // диапазон в UTF-16 код-юнитах исходного текста
	to   []uint16 // замена
}

// applyReplacementsToEntities применяет правила к тексту+entities. Для target=="all"
// правит видимый текст и пересчитывает/удаляет entities; для "links" — правит URL
// у text_link/url entities и голые URL в тексте.
func applyReplacementsToEntities(text string, entities []Entity, rules []Replacement) (string, []Entity) {
	for _, r := range rules {
		if r.From == "" {
			continue
		}
		if r.Target == "links" {
			text, entities = applyLinksToEntities(text, entities, r)
		} else {
			text, entities = applyAllToEntities(text, entities, r)
		}
	}
	return text, entities
}

// buildUnitByByte строит карту байтовый-offset → UTF-16-offset (для маппинга результатов
// regexp, отдающего байтовые индексы, в код-юниты, в которых заданы entities).
func buildUnitByByte(text string) []int {
	m := make([]int, len(text)+1)
	u, bi := 0, 0
	for _, r := range text {
		rl := utf8.RuneLen(r)
		for k := 0; k < rl; k++ {
			m[bi+k] = u
		}
		bi += rl
		u += len(utf16.Encode([]rune{r}))
	}
	m[len(text)] = u
	return m
}

func applyAllToEntities(text string, entities []Entity, r Replacement) (string, []Entity) {
	units := utf16.Encode([]rune(text))
	var edits []utf16Edit
	if r.Regex {
		re, err := regexp.Compile(r.From)
		if err != nil {
			slog.Warn("invalid replacement regex", "pattern", r.From, "err", err)
			return text, entities
		}
		ubb := buildUnitByByte(text)
		for _, m := range re.FindAllStringSubmatchIndex(text, -1) {
			repl := re.ExpandString(nil, r.To, text, m)
			edits = append(edits, utf16Edit{ubb[m[0]], ubb[m[1]], utf16.Encode([]rune(string(repl)))})
		}
	} else {
		from16 := utf16.Encode([]rune(r.From))
		to16 := utf16.Encode([]rune(r.To))
		if len(from16) == 0 {
			return text, entities
		}
		for i := 0; i+len(from16) <= len(units); {
			if equalUnits(units[i:i+len(from16)], from16) {
				edits = append(edits, utf16Edit{i, i + len(from16), to16})
				i += len(from16)
			} else {
				i++
			}
		}
	}
	if len(edits) == 0 {
		return text, entities
	}
	nu, ne := spliceEntities(units, entities, edits)
	return utf16ToString(nu), ne
}

func applyLinksToEntities(text string, entities []Entity, r Replacement) (string, []Entity) {
	// URL у text_link/url entities — не в тексте; правим на месте, без пересчёта offset'ов.
	out := make([]Entity, len(entities))
	copy(out, entities)
	var re *regexp.Regexp
	if r.Regex {
		var err error
		if re, err = regexp.Compile(r.From); err != nil {
			slog.Warn("invalid replacement regex", "pattern", r.From, "err", err)
			return text, entities
		}
	}
	replURL := func(u string) string {
		if r.Regex {
			return re.ReplaceAllString(u, r.To)
		}
		return strings.ReplaceAll(u, r.From, r.To)
	}
	for i := range out {
		if (out[i].Type == "text_link" || out[i].Type == "url") && out[i].URL != "" {
			out[i].URL = replURL(out[i].URL)
		}
	}
	// Голые URL в видимом тексте (тип "url" без .URL или plain-текст).
	units := utf16.Encode([]rune(text))
	ubb := buildUnitByByte(text)
	var edits []utf16Edit
	for _, m := range urlRegex.FindAllStringIndex(text, -1) {
		orig := text[m[0]:m[1]]
		repl := replURL(orig)
		if repl != orig {
			edits = append(edits, utf16Edit{ubb[m[0]], ubb[m[1]], utf16.Encode([]rune(repl))})
		}
	}
	if len(edits) == 0 {
		return text, out
	}
	nu, ne := spliceEntities(units, out, edits)
	return utf16ToString(nu), ne
}

func equalUnits(a, b []uint16) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// spliceEntities применяет непересекающиеся edits (отсортированы по s) к тексту в UTF-16
// и пересчитывает entities. Entity, чей диапазон полностью вырезан, удаляется.
func spliceEntities(units []uint16, ents []Entity, edits []utf16Edit) ([]uint16, []Entity) {
	var out []uint16
	prev := 0
	for _, ed := range edits {
		out = append(out, units[prev:ed.s]...)
		out = append(out, ed.to...)
		prev = ed.e
	}
	out = append(out, units[prev:]...)

	// mapPos переносит offset исходного текста в новый. right=true — правая граница
	// (для конца entity), чтобы точка внутри вырезанного диапазона схлопывалась в 0.
	mapPos := func(p int, right bool) int {
		np := p
		for _, ed := range edits {
			if p >= ed.e {
				np += len(ed.to) - (ed.e - ed.s)
			} else if p > ed.s {
				earlier := np - p
				if right {
					return ed.s + earlier + len(ed.to)
				}
				return ed.s + earlier
			} else {
				break
			}
		}
		return np
	}

	var ne []Entity
	for _, e := range ents {
		s := mapPos(e.Offset, false)
		en := mapPos(e.Offset+e.Length, true)
		if en <= s {
			continue // текст entity целиком вырезан → удаляем (в т.ч. text_link)
		}
		e.Offset = s
		e.Length = en - s
		ne = append(ne, e)
	}
	return out, ne
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
func tgReplacementsKeyboard(maxChatID int64) *InlineKeyboardMarkup {
	id := fmt.Sprintf("%d", maxChatID)
	return NewInlineKeyboard(
		NewInlineRow(
			NewInlineButton("+ TG→MAX", "cpra:tg>max:"+id),
			NewInlineButton("+ MAX→TG", "cpra:max>tg:"+id),
		),
		NewInlineRow(
			NewInlineButton("🗑 Очистить всё", "cprc:"+id),
			NewInlineButton("◀ Назад", "cprb:"+id),
		),
	)
}

// tgReplItemKeyboard — кнопки для одной замены в TG.
func tgReplItemKeyboard(dir string, idx int, maxChatID string, currentTarget string) *InlineKeyboardMarkup {
	toggleLabel := "🔗 Только ссылки"
	toggleTarget := "links"
	if currentTarget == "links" {
		toggleLabel = "📝 Весь текст"
		toggleTarget = "all"
	}
	return NewInlineKeyboard(
		NewInlineRow(
			NewInlineButton(toggleLabel, fmt.Sprintf("cprt:%s:%d:%s:%s", dir, idx, toggleTarget, maxChatID)),
			NewInlineButton("❌ Удалить", fmt.Sprintf("cprd:%s:%d:%s", dir, idx, maxChatID)),
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
