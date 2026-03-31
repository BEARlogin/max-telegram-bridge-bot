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
// Корректно обрабатывает перекрывающиеся диапазоны через interval splitting:
// разбивает текст на непересекающиеся отрезки и для каждого выводит
// правильно вложенные теги.
func maxMarkupsToHTML(text string, markups []maxschemes.MarkUp) string {
	if len(markups) == 0 {
		return html.EscapeString(text)
	}

	runes := []rune(text)
	utf16units := utf16.Encode(runes)
	n := len(utf16units)

	// Описание одного markup с HTML-тегами
	type markupInfo struct {
		from, to        int // UTF-16 offsets
		openTag, closeTag string
	}

	var infos []markupInfo
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
		to := m.From + m.Length
		if to > n {
			to = n
		}
		if m.From >= to {
			continue
		}
		infos = append(infos, markupInfo{m.From, to, openTag, closeTag})
	}

	if len(infos) == 0 {
		return html.EscapeString(text)
	}

	// Собираем все граничные точки → разбиваем на непересекающиеся отрезки
	boundarySet := map[int]struct{}{0: {}, n: {}}
	for _, m := range infos {
		boundarySet[m.from] = struct{}{}
		boundarySet[m.to] = struct{}{}
	}
	boundaries := make([]int, 0, len(boundarySet))
	for b := range boundarySet {
		boundaries = append(boundaries, b)
	}
	sort.Ints(boundaries)

	var sb strings.Builder

	for seg := 0; seg < len(boundaries)-1; seg++ {
		segStart := boundaries[seg]
		segEnd := boundaries[seg+1]
		if segStart >= segEnd {
			continue
		}

		// Определяем какие markups активны на этом отрезке
		var active []markupInfo
		for _, m := range infos {
			if m.from <= segStart && segEnd <= m.to {
				active = append(active, m)
			}
		}

		// Текст отрезка
		segText := html.EscapeString(utf16ToString(utf16units[segStart:segEnd]))

		// Оборачиваем текст в теги (порядок стабилен — по индексу в infos)
		result := segText
		for i := len(active) - 1; i >= 0; i-- {
			result = active[i].openTag + result + active[i].closeTag
		}
		sb.WriteString(result)
	}

	return sb.String()
}
