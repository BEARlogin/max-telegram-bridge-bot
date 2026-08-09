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
		// символом вплотную ("** жирный**" не станет bold) И не даёт emphasis
		// пересекать пустую строку ("**a\n\nb**" не отрендерится). TG же отдаёт такое
		// одним entity. Поэтому: код-блок (pre) оставляем цельным, остальное дробим по
		// параграфам и каждый сегмент оборачиваем отдельно с обрезкой пробельных границ.
		var segs [][2]int
		if e.Type == "pre" {
			s, en := e.Offset, end
			for s < en && isMarkupSpace(utf16units[s]) {
				s++
			}
			for en > s && isMarkupSpace(utf16units[en-1]) {
				en--
			}
			if s < en {
				segs = append(segs, [2]int{s, en})
			}
		} else {
			segs = markupSegments(utf16units, e.Offset, end)
		}
		for _, sg := range segs {
			tags = append(tags, tag{pos: sg[0], open: true, idx: i, text: open})
			tags = append(tags, tag{pos: sg[1], open: false, idx: i, text: close})
		}
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

// tgEntitiesToHTML конвертирует TG text + entities в HTML для MAX (format="html").
// MAX-HTML поддерживает <b> <i> <u> <s> <code> <pre> <blockquote> <a> — в отличие
// от MAX-markdown, который не рендерит код-блоки и цитаты. Текст экранируется.
// HTML-теги спокойно охватывают пробелы и пустые строки, поэтому ни подрезка
// границ, ни деление по параграфам (как в markdown) тут не нужны.
func tgEntitiesToHTML(text string, entities []Entity) string {
	if len(entities) == 0 {
		return html.EscapeString(text)
	}

	// Telegram не обещает порядок entities с одинаковыми границами. Для HTML это
	// важно: блочный <blockquote> должен оборачивать inline-разметку, а не попадать
	// внутрь <b>/<i>. Сначала выстраиваем entities от внешних к внутренним; stable
	// сохраняет исходный порядок для равноценных inline-тегов.
	ordered := append([]Entity(nil), entities...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Offset != ordered[j].Offset {
			return ordered[i].Offset < ordered[j].Offset
		}
		iEnd := ordered[i].Offset + ordered[i].Length
		jEnd := ordered[j].Offset + ordered[j].Length
		if iEnd != jEnd {
			return iEnd > jEnd
		}
		return htmlEntityOuterRank(ordered[i].Type) < htmlEntityOuterRank(ordered[j].Type)
	})

	runes := []rune(text)
	utf16units := utf16.Encode(runes)

	// skip — код-юниты, которые НЕ выводим (кастомные эмодзи Telegram: MAX их не
	// рендерит, а fallback-символ засоряет пересылку). Режем весь диапазон энтити.
	skip := make([]bool, len(utf16units))
	hasSkip := false

	type tag struct {
		pos  int
		open bool
		idx  int
		text string
	}

	var tags []tag
	for i, e := range ordered {
		if e.Type == "custom_emoji" {
			end := e.Offset + e.Length
			if end > len(utf16units) {
				end = len(utf16units)
			}
			for p := e.Offset; p < end; p++ {
				if p >= 0 {
					skip[p] = true
					hasSkip = true
				}
			}
			continue
		}
		var openTag, closeTag string
		switch e.Type {
		case "bold":
			openTag, closeTag = "<b>", "</b>"
		case "italic":
			openTag, closeTag = "<i>", "</i>"
		case "underline":
			openTag, closeTag = "<u>", "</u>"
		case "strikethrough":
			openTag, closeTag = "<s>", "</s>"
		case "code":
			openTag, closeTag = "<code>", "</code>"
		case "pre":
			openTag, closeTag = "<pre>", "</pre>"
		case "blockquote", "expandable_blockquote":
			openTag, closeTag = "<blockquote>", "</blockquote>"
		case "text_link":
			openTag = `<a href="` + html.EscapeString(e.URL) + `">`
			closeTag = "</a>"
		default:
			continue
		}
		end := e.Offset + e.Length
		if end > len(utf16units) {
			end = len(utf16units)
		}
		if e.Offset >= end {
			continue
		}
		tags = append(tags, tag{pos: e.Offset, open: true, idx: i, text: openTag})
		tags = append(tags, tag{pos: end, open: false, idx: i, text: closeTag})
	}

	if len(tags) == 0 && !hasSkip {
		return html.EscapeString(text)
	}

	sort.Slice(tags, func(i, j int) bool {
		if tags[i].pos != tags[j].pos {
			return tags[i].pos < tags[j].pos
		}
		if tags[i].open != tags[j].open {
			return !tags[i].open // close раньше open на одной позиции
		}
		if tags[i].open {
			return tags[i].idx < tags[j].idx
		}
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
				if !skip[i] {
					r := utf16.DecodeRune(rune(utf16units[i]), rune(utf16units[i+1]))
					sb.WriteString(html.EscapeString(string(r)))
				}
				i++ // суррогатная пара — пропускаем и низкий код-юнит
			} else if !skip[i] {
				sb.WriteString(html.EscapeString(string(rune(utf16units[i]))))
			}
		}
	}
	return sb.String()
}

func htmlEntityOuterRank(entityType string) int {
	switch entityType {
	case "blockquote", "expandable_blockquote":
		return 0
	case "pre":
		return 1
	default:
		return 2
	}
}

// isMarkupSpace — пробельный UTF-16 код-юнит (ASCII whitespace), который нельзя
// оставлять вплотную к markdown-delimiter.
func isMarkupSpace(u uint16) bool {
	return u == ' ' || u == '\n' || u == '\r' || u == '\t'
}

// markupSegments разбивает диапазон [start,end) на параграфы по пустым строкам
// (≥2 переносов подряд) — markdown-emphasis не может пересекать пустую строку.
// Внутри каждого сегмента обрезает пробельные границы. Одиночные переносы строк
// сохраняются внутри сегмента (CommonMark их допускает). Возвращает список [from,to).
func markupSegments(units []uint16, start, end int) [][2]int {
	if end > len(units) {
		end = len(units)
	}
	var segs [][2]int
	i := start
	for i < end {
		// Пропускаем ведущие пробелы/переносы.
		for i < end && isMarkupSpace(units[i]) {
			i++
		}
		if i >= end {
			break
		}
		segStart := i
		segEnd := i // позиция за последним непробельным символом сегмента
		for i < end {
			if !isMarkupSpace(units[i]) {
				i++
				segEnd = i
				continue
			}
			// Пробельный прогон: считаем переносы строк.
			j, nl := i, 0
			for j < end && isMarkupSpace(units[j]) {
				if units[j] == '\n' {
					nl++
				}
				j++
			}
			i = j
			if nl >= 2 {
				break // разрыв параграфа — сегмент закончен
			}
			// Одиночный перенос/пробелы внутри параграфа — продолжаем тот же сегмент.
		}
		if segEnd > segStart {
			segs = append(segs, [2]int{segStart, segEnd})
		}
	}
	return segs
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

	// open/close HTML для типа маркапа (для ссылки URL входит в идентичность тега).
	type tagPair struct{ open, close string }
	tagFor := func(m maxschemes.MarkUp) (tagPair, bool) {
		switch m.Type {
		case maxschemes.MarkupStrong:
			return tagPair{"<b>", "</b>"}, true
		case maxschemes.MarkupEmphasized:
			return tagPair{"<i>", "</i>"}, true
		case maxschemes.MarkupMonospaced:
			return tagPair{"<code>", "</code>"}, true
		case maxschemes.MarkupStrikethrough:
			return tagPair{"<s>", "</s>"}, true
		case maxschemes.MarkupUnderline:
			return tagPair{"<u>", "</u>"}, true
		case maxschemes.MarkupLink:
			return tagPair{`<a href="` + html.EscapeString(m.URL) + `">`, "</a>"}, true
		}
		return tagPair{}, false
	}

	// activeAt — детерминированно упорядоченный набор тегов, активных на позиции i.
	// MAX допускает ПЕРЕКРЫВАЮЩИЕСЯ маркапы (ссылка+жирный внахлёст), а HTML требует
	// строгой вложенности. Поэтому на каждой границе закрываем все открытые теги и
	// переоткрываем актуальные — так HTML всегда валиден (Telegram не ругается).
	activeAt := func(i int) []tagPair {
		var out []tagPair
		for _, m := range markups {
			if m.From <= i && i < m.From+m.Length {
				if tp, ok := tagFor(m); ok {
					out = append(out, tp)
				}
			}
		}
		sort.Slice(out, func(a, b int) bool { return out[a].open < out[b].open })
		return out
	}
	sameTags := func(a, b []tagPair) bool {
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

	var sb strings.Builder
	var open []tagPair
	for i := 0; i <= len(utf16units); i++ {
		var cur []tagPair
		if i < len(utf16units) {
			cur = activeAt(i)
		}
		if !sameTags(cur, open) {
			for j := len(open) - 1; j >= 0; j-- {
				sb.WriteString(open[j].close)
			}
			open = cur
			for _, tp := range open {
				sb.WriteString(tp.open)
			}
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
