package main

import (
	"testing"

	maxschemes "github.com/max-messenger/max-bot-api-client-go/schemes"
)

// --- tgEntitiesToMarkdown ---

func TestTgEntitiesToMarkdown_NoEntities(t *testing.T) {
	got := tgEntitiesToMarkdown("hello world", nil)
	if got != "hello world" {
		t.Errorf("got %q", got)
	}
}

func TestTgEntitiesToMarkdown_Empty(t *testing.T) {
	got := tgEntitiesToMarkdown("hello", []Entity{})
	if got != "hello" {
		t.Errorf("got %q", got)
	}
}

func TestTgEntitiesToMarkdown_Bold(t *testing.T) {
	got := tgEntitiesToMarkdown("hello world", []Entity{
		{Type: "bold", Offset: 0, Length: 5},
	})
	if got != "**hello** world" {
		t.Errorf("got %q, want %q", got, "**hello** world")
	}
}

func TestTgEntitiesToMarkdown_BoldAcrossBlankLine(t *testing.T) {
	// Жирный через пустую строку: emphasis нельзя пересекать пустую строку,
	// поэтому маркер разбивается по параграфам.
	text := "Traefik\n\nОбычно"
	got := tgEntitiesToMarkdown(text, []Entity{
		{Type: "bold", Offset: 0, Length: 15}, // вся строка (UTF-16: 7 + 2 + 6)
	})
	want := "**Traefik**\n\n**Обычно**"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestTgEntitiesToMarkdown_BoldSingleNewline(t *testing.T) {
	// Одиночный перенос строки эмфазу не разрывает — сегмент остаётся цельным.
	got := tgEntitiesToMarkdown("line1\nline2", []Entity{
		{Type: "bold", Offset: 0, Length: 11},
	})
	want := "**line1\nline2**"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestTgEntitiesToHTML_CodeAndBlockquote(t *testing.T) {
	// pre → <pre>, blockquote → <blockquote>; то, что MAX-markdown не рендерит.
	text := "code:\nfn()\nquote"
	got := tgEntitiesToHTML(text, []Entity{
		{Type: "pre", Offset: 6, Length: 4},         // "fn()"
		{Type: "blockquote", Offset: 11, Length: 5}, // "quote"
	})
	want := "code:\n<pre>fn()</pre>\n<blockquote>quote</blockquote>"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestTgEntitiesToHTML_BlockquoteWrapsBoldRegardlessOfEntityOrder(t *testing.T) {
	// Telegram не гарантирует, что внешняя цитата придёт раньше вложенного bold.
	// Невалидное <b><blockquote> MAX может разобрать как обычный текст без разметки.
	got := tgEntitiesToHTML("важная цитата", []Entity{
		{Type: "bold", Offset: 0, Length: 6},
		{Type: "blockquote", Offset: 0, Length: 13},
	})
	want := "<blockquote><b>важная</b> цитата</blockquote>"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestTgEntitiesToHTML_BlockquoteWinsIdenticalRange(t *testing.T) {
	got := tgEntitiesToHTML("цитата", []Entity{
		{Type: "bold", Offset: 0, Length: 6},
		{Type: "blockquote", Offset: 0, Length: 6},
	})
	want := "<blockquote><b>цитата</b></blockquote>"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestTgEntitiesToHTML_Escaping(t *testing.T) {
	// Спецсимволы HTML должны экранироваться, чтобы не ломать разметку MAX.
	got := tgEntitiesToHTML("a < b & c", nil)
	want := "a &lt; b &amp; c"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestTgEntitiesToHTML_Link(t *testing.T) {
	got := tgEntitiesToHTML("click here", []Entity{
		{Type: "text_link", Offset: 0, Length: 5, URL: "https://x.dev?a=1&b=2"},
	})
	want := `<a href="https://x.dev?a=1&amp;b=2">click</a> here`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestTgEntitiesToMarkdown_Italic(t *testing.T) {
	got := tgEntitiesToMarkdown("hello world", []Entity{
		{Type: "italic", Offset: 6, Length: 5},
	})
	if got != "hello _world_" {
		t.Errorf("got %q", got)
	}
}

func TestTgEntitiesToMarkdown_Code(t *testing.T) {
	got := tgEntitiesToMarkdown("use fmt.Println please", []Entity{
		{Type: "code", Offset: 4, Length: 11},
	})
	if got != "use `fmt.Println` please" {
		t.Errorf("got %q", got)
	}
}

func TestTgEntitiesToMarkdown_Pre(t *testing.T) {
	got := tgEntitiesToMarkdown("code: func main()", []Entity{
		{Type: "pre", Offset: 6, Length: 11},
	})
	want := "code: ```\nfunc main()\n```"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestTgEntitiesToMarkdown_Strikethrough(t *testing.T) {
	got := tgEntitiesToMarkdown("old new", []Entity{
		{Type: "strikethrough", Offset: 0, Length: 3},
	})
	if got != "~~old~~ new" {
		t.Errorf("got %q", got)
	}
}

func TestTgEntitiesToMarkdown_TextLink(t *testing.T) {
	got := tgEntitiesToMarkdown("click here now", []Entity{
		{Type: "text_link", Offset: 6, Length: 4, URL: "https://example.com"},
	})
	want := "click [here](https://example.com) now"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestTgEntitiesToMarkdown_MultipleEntities(t *testing.T) {
	got := tgEntitiesToMarkdown("hello world test", []Entity{
		{Type: "bold", Offset: 0, Length: 5},
		{Type: "italic", Offset: 6, Length: 5},
	})
	want := "**hello** _world_ test"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestTgEntitiesToMarkdown_TrailingSpaces(t *testing.T) {
	// Хвостовой пробел внутри bold-entity нужно вытолкнуть наружу — иначе
	// markdown-парсер MAX/CommonMark не закрывает `**` перед пробелом.
	got := tgEntitiesToMarkdown("hello world", []Entity{
		{Type: "bold", Offset: 0, Length: 6}, // "hello "
	})
	want := "**hello** world"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestTgEntitiesToMarkdown_LeadingSpaces(t *testing.T) {
	// Ведущие пробелы внутри bold-entity нужно вытолкнуть наружу — иначе
	// открывающий `**` стоит перед пробелом и парсер его не подхватывает.
	got := tgEntitiesToMarkdown("a  bold rest", []Entity{
		{Type: "bold", Offset: 1, Length: 6}, // "  bold"
	})
	want := "a  **bold** rest"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestTgEntitiesToMarkdown_OverlappingBoldItalic(t *testing.T) {
	// Same text is both bold and italic — should produce nested markers, not duplicate text
	got := tgEntitiesToMarkdown("Тест разметки", []Entity{
		{Type: "bold", Offset: 5, Length: 8},
		{Type: "italic", Offset: 5, Length: 8},
	})
	want := "Тест **_разметки_**"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestTgEntitiesToMarkdown_OverlappingBoldItalicStrike(t *testing.T) {
	got := tgEntitiesToMarkdown("Тест разметки", []Entity{
		{Type: "bold", Offset: 5, Length: 8},
		{Type: "italic", Offset: 5, Length: 8},
		{Type: "strikethrough", Offset: 5, Length: 8},
	})
	want := "Тест **_~~разметки~~_**"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestTgEntitiesToMarkdown_UnknownType(t *testing.T) {
	got := tgEntitiesToMarkdown("hello world", []Entity{
		{Type: "mention", Offset: 0, Length: 5},
	})
	if got != "hello world" {
		t.Errorf("unknown entity type should be skipped, got %q", got)
	}
}

func TestTgEntitiesToMarkdown_Emoji(t *testing.T) {
	// Emoji "🔥" is 2 UTF-16 code units (surrogate pair)
	text := "🔥hello"
	got := tgEntitiesToMarkdown(text, []Entity{
		{Type: "bold", Offset: 2, Length: 5}, // "hello" starts at UTF-16 offset 2
	})
	want := "🔥**hello**"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestTgEntitiesToMarkdown_EntityBeyondText(t *testing.T) {
	got := tgEntitiesToMarkdown("hi", []Entity{
		{Type: "bold", Offset: 0, Length: 100},
	})
	want := "**hi**"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// --- maxMarkupsToHTML ---

func TestMaxMarkupsToHTML_NoMarkups(t *testing.T) {
	got := maxMarkupsToHTML("hello <world>", nil)
	if got != "hello &lt;world&gt;" {
		t.Errorf("got %q", got)
	}
}

func TestMaxMarkupsToHTML_Bold(t *testing.T) {
	got := maxMarkupsToHTML("hello world", []maxschemes.MarkUp{
		{Type: maxschemes.MarkupStrong, From: 0, Length: 5},
	})
	want := "<b>hello</b> world"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestMaxMarkupsToHTML_Italic(t *testing.T) {
	got := maxMarkupsToHTML("hello world", []maxschemes.MarkUp{
		{Type: maxschemes.MarkupEmphasized, From: 6, Length: 5},
	})
	want := "hello <i>world</i>"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestMaxMarkupsToHTML_Code(t *testing.T) {
	got := maxMarkupsToHTML("use fmt.Println", []maxschemes.MarkUp{
		{Type: maxschemes.MarkupMonospaced, From: 4, Length: 11},
	})
	want := "use <code>fmt.Println</code>"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestMaxMarkupsToHTML_Strikethrough(t *testing.T) {
	got := maxMarkupsToHTML("old new", []maxschemes.MarkUp{
		{Type: maxschemes.MarkupStrikethrough, From: 0, Length: 3},
	})
	want := "<s>old</s> new"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestMaxMarkupsToHTML_Link(t *testing.T) {
	got := maxMarkupsToHTML("click here", []maxschemes.MarkUp{
		{Type: maxschemes.MarkupLink, From: 6, Length: 4, URL: "https://example.com"},
	})
	want := `click <a href="https://example.com">here</a>`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestMaxMarkupsToHTML_EscapesHTML(t *testing.T) {
	got := maxMarkupsToHTML("<b>not bold</b>", nil)
	want := "&lt;b&gt;not bold&lt;/b&gt;"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestMaxMarkupsToHTML_Multiple(t *testing.T) {
	got := maxMarkupsToHTML("hello world test", []maxschemes.MarkUp{
		{Type: maxschemes.MarkupStrong, From: 0, Length: 5},
		{Type: maxschemes.MarkupEmphasized, From: 6, Length: 5},
	})
	want := "<b>hello</b> <i>world</i> test"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestMaxMarkupsToHTML_Emoji(t *testing.T) {
	// "🔥test" — emoji is surrogate pair (2 UTF-16 units)
	text := "🔥test"
	got := maxMarkupsToHTML(text, []maxschemes.MarkUp{
		{Type: maxschemes.MarkupStrong, From: 2, Length: 4}, // "test"
	})
	want := "🔥<b>test</b>"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestMaxMarkupsToHTML_Underline(t *testing.T) {
	got := maxMarkupsToHTML("hello", []maxschemes.MarkUp{
		{Type: maxschemes.MarkupUnderline, From: 0, Length: 5},
	})
	want := "<u>hello</u>"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestMaxMarkupsToHTML_LinkEscapesURL(t *testing.T) {
	got := maxMarkupsToHTML("link", []maxschemes.MarkUp{
		{Type: maxschemes.MarkupLink, From: 0, Length: 4, URL: "https://example.com/?a=1&b=2"},
	})
	want := `<a href="https://example.com/?a=1&amp;b=2">link</a>`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestTgEntitiesToMarkdown_TrailingNewlines(t *testing.T) {
	// Bold-entity с хвостовыми \n\n — маркеры должны встать вплотную к тексту.
	got := tgEntitiesToMarkdown("Header\n\nbody", []Entity{
		{Type: "bold", Offset: 0, Length: 8}, // "Header\n\n"
	})
	want := "**Header**\n\nbody"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
