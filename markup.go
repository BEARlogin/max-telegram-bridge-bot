package main

import (
	"fmt"
	"html"
	"sort"
	"strings"
	"unicode/utf16"

	maxschemes "github.com/max-messenger/max-bot-api-client-go/schemes"
)

// --- TG Entities → Markdown (для MAX) ---

// tgEntitiesToMarkdown конвертирует TG text + entities в markdown-текст для MAX.
// Использует tag-insertion подход для корректной обработки вложенных/перекрывающихся entities
// (например bold+italic на одном тексте).
func tgEntitiesToMarkdown(text string, entities []Entity) string {
	if len(entities) == 0 {
		return text
	}

	// Конвертируем в UTF-16 для корректных offsets (TG использует UTF-16)
	runes := []rune(text)
	utf16units := utf16.Encode(runes)

	type tag struct {
		pos  int
		open bool
		idx  int // индекс entity — для правильного порядка вложенных тегов
		text string
	}

	var tags []tag
	for i, e := range entities {
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
		case "underline":
			// MAX markdown не поддерживает underline — пропускаем
			continue
		case "text_link":
			open = "["
			close = fmt.Sprintf("](%s)", e.URL)
		default:
			continue
		}
		end := e.Offset + e.Length
		if end > len(utf16units) {
			end = len(utf16units)
		}
		// Markdown-парсер MAX (как и CommonMark) не принимает delimiter с пробельным
		// символом вплотную: "** жирный**" / "**жирный \n**" не станут bold. TG часто
		// включает пробелы/переносы внутрь entity → обрезаем границы внутрь, чтобы
		// маркеры стояли вплотную к тексту.
		openPos := e.Offset
		for openPos < end && isMarkupSpace(utf16units[openPos]) {
			openPos++
		}
		closePos := end
		for closePos > openPos && isMarkupSpace(utf16units[closePos-1]) {
			closePos--
		}
		if openPos >= closePos {
			continue
		}
		tags = append(tags, tag{pos: openPos, open: true, idx: i, text: open})
		tags = append(tags, tag{pos: closePos, open: false, idx: i, text: close})
	}

	if len(tags) == 0 {
		return text
	}

	sort.Slice(tags, func(i, j int) bool {
		if tags[i].pos != tags[j].pos {
			return tags[i].pos < tags[j].pos
		}
		// На одной позиции: close перед open (для смежных entities)
		if tags[i].open != tags[j].open {
			return !tags[i].open
		}
		// Среди open на одной позиции: по порядку entity
		if tags[i].open {
			return tags[i].idx < tags[j].idx
		}
		// Среди close на одной позиции: в обратном порядке (правильная вложенность)
		return tags[i].idx > tags[j].idx
	})

	var sb strings.Builder
	tagIdx := 0
	for i := 0; i <= len(utf16units); i++ {
		for tagIdx < len(tags) && tags[tagIdx].pos == i {
			sb.WriteString(tags[tagIdx].text)
			tagIdx++
		}
		if i < len(utf16units) {
			if utf16.IsSurrogate(rune(utf16units[i])) && i+1 < len(utf16units) {
				r := utf16.DecodeRune(rune(utf16units[i]), rune(utf16units[i+1]))
				sb.WriteRune(r)
				i++
			} else {
				sb.WriteRune(rune(utf16units[i]))
			}
		}
	}
	return sb.String()
}

// isMarkupSpace — пробельный UTF-16 код-юнит (ASCII whitespace), который нельзя
// оставлять вплотную к markdown-delimiter.
func isMarkupSpace(u uint16) bool {
	return u == ' ' || u == '\n' || u == '\r' || u == '\t'
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
