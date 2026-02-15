package main

import (
	"fmt"
	"html"
	"sort"
	"strings"
	"unicode/utf16"

	maxschemes "github.com/max-messenger/max-bot-api-client-go/schemes"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// --- TG Entities → Markdown (для MAX) ---

// tgEntitiesToMarkdown конвертирует TG text + entities в markdown-текст для MAX.
// Обрабатывает edge cases: пробелы перед/после маркеров выносятся за пределы тегов.
func tgEntitiesToMarkdown(text string, entities []tgbotapi.MessageEntity) string {
	if len(entities) == 0 {
		return text
	}

	// Конвертируем в UTF-16 для корректных offsets (TG использует UTF-16)
	runes := []rune(text)
	utf16units := utf16.Encode(runes)

	// Собираем фрагменты: чередуя plain text и форматированные куски
	// Работаем в UTF-16 координатах
	type fragment struct {
		start, end int // UTF-16 offsets
		entity     *tgbotapi.MessageEntity
	}

	// Сортируем entities по offset
	sorted := make([]tgbotapi.MessageEntity, len(entities))
	copy(sorted, entities)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Offset < sorted[j].Offset
	})

	var sb strings.Builder
	pos := 0

	for i := range sorted {
		e := &sorted[i]
		var open, close string
		switch e.Type {
		case "bold":
			open, close = "**", "**"
		case "italic":
			open, close = "_", "_"
		case "code":
			open, close = "`", "`"
		case "pre":
			open, close = "```\n", "\n```"
		case "strikethrough":
			open, close = "~~", "~~"
		case "text_link":
			open = "["
			close = fmt.Sprintf("](%s)", e.URL)
		default:
			continue
		}

		// Текст до entity
		if e.Offset > pos {
			sb.WriteString(utf16ToString(utf16units[pos:e.Offset]))
		}

		// Текст entity
		end := e.Offset + e.Length
		if end > len(utf16units) {
			end = len(utf16units)
		}
		inner := utf16ToString(utf16units[e.Offset:end])

		// Trim пробелов: выносим leading/trailing пробелы за маркеры
		trimmed := strings.TrimRight(inner, " \t\n")
		trailingSpaces := inner[len(trimmed):]
		trimmed2 := strings.TrimLeft(trimmed, " \t\n")
		leadingSpaces := trimmed[:len(trimmed)-len(trimmed2)]

		sb.WriteString(leadingSpaces)
		if trimmed2 != "" {
			sb.WriteString(open)
			sb.WriteString(trimmed2)
			sb.WriteString(close)
		}
		sb.WriteString(trailingSpaces)

		pos = end
	}

	// Остаток текста
	if pos < len(utf16units) {
		sb.WriteString(utf16ToString(utf16units[pos:]))
	}

	return sb.String()
}

// utf16ToString конвертирует UTF-16 slice обратно в Go string.
func utf16ToString(units []uint16) string {
	runes := utf16.Decode(units)
	return string(runes)
}

// --- MAX Markups → TG HTML ---

// maxMarkupsToHTML конвертирует MAX text + markups в TG-совместимый HTML.
func maxMarkupsToHTML(text string, markups []maxschemes.MarkUp) string {
	if len(markups) == 0 {
		return html.EscapeString(text)
	}

	runes := []rune(text)
	utf16units := utf16.Encode(runes)

	type tag struct {
		pos   int
		open  bool
		order int
		tag   string
	}

	var tags []tag
	for _, m := range markups {
		var openTag, closeTag string
		switch m.Type {
		case maxschemes.MarkupStrong:
			openTag, closeTag = "<b>", "</b>"
		case maxschemes.MarkupEmphasized:
			openTag, closeTag = "<i>", "</i>"
		case maxschemes.MarkupMonospaced:
			openTag, closeTag = "<code>", "</code>"
		case maxschemes.MarkupStrikethrough:
			openTag, closeTag = "<s>", "</s>"
		case maxschemes.MarkupUnderline:
			openTag, closeTag = "<u>", "</u>"
		case maxschemes.MarkupLink:
			openTag = `<a href="` + html.EscapeString(m.URL) + `">`
			closeTag = "</a>"
		default:
			continue
		}
		tags = append(tags, tag{pos: m.From, open: true, order: 0, tag: openTag})
		tags = append(tags, tag{pos: m.From + m.Length, open: false, order: 1, tag: closeTag})
	}

	sort.Slice(tags, func(i, j int) bool {
		if tags[i].pos != tags[j].pos {
			return tags[i].pos < tags[j].pos
		}
		return tags[i].order > tags[j].order
	})

	var sb strings.Builder
	tagIdx := 0
	for i := 0; i <= len(utf16units); i++ {
		for tagIdx < len(tags) && tags[tagIdx].pos == i {
			sb.WriteString(tags[tagIdx].tag)
			tagIdx++
		}
		if i < len(utf16units) {
			if utf16.IsSurrogate(rune(utf16units[i])) && i+1 < len(utf16units) {
				r := utf16.DecodeRune(rune(utf16units[i]), rune(utf16units[i+1]))
				sb.WriteString(html.EscapeString(string(r)))
				i++
			} else {
				sb.WriteString(html.EscapeString(string(rune(utf16units[i]))))
			}
		}
	}
	return sb.String()
}
