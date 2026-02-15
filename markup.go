package main

import (
	"html"
	"sort"
	"strings"
	"unicode/utf16"

	maxschemes "github.com/max-messenger/max-bot-api-client-go/schemes"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// --- TG Entities → MAX Markups ---

// tgEntitiesToMaxMarkups конвертирует TG entities в MAX markups.
// TG entities используют UTF-16 offsets — MAX тоже.
func tgEntitiesToMaxMarkups(entities []tgbotapi.MessageEntity) []maxschemes.MarkUp {
	if len(entities) == 0 {
		return nil
	}
	var markups []maxschemes.MarkUp
	for _, e := range entities {
		var mt maxschemes.MarkupType
		var url string
		switch e.Type {
		case "bold":
			mt = maxschemes.MarkupStrong
		case "italic":
			mt = maxschemes.MarkupEmphasized
		case "code", "pre":
			mt = maxschemes.MarkupMonospaced
		case "strikethrough":
			mt = maxschemes.MarkupStrikethrough
		case "underline":
			mt = maxschemes.MarkupUnderline
		case "text_link":
			mt = maxschemes.MarkupLink
			url = e.URL
		default:
			continue
		}
		m := maxschemes.MarkUp{
			From:   e.Offset,
			Length: e.Length,
			Type:   mt,
		}
		if url != "" {
			m.URL = url
		}
		markups = append(markups, m)
	}
	return markups
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
