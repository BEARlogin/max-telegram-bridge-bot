package main

import (
	"html"
	"regexp"
	"strings"

	maxschemes "github.com/max-messenger/max-bot-api-client-go/schemes"
)

func tgName(msg *TGMessage) string {
	if isTgAnonymousAdmin(msg) {
		return ""
	}
	if msg.From == nil {
		if msg.SenderChat != nil {
			return msg.SenderChat.Title
		}
		return "Unknown"
	}
	name := msg.From.FirstName
	if msg.From.LastName != "" {
		name += " " + msg.From.LastName
	}
	return name
}

// formatAttribution собирает строку "Имя: текст" или "Имя:\nтекст" в зависимости от настройки.
func formatAttribution(name, text string, newline bool) string {
	if strings.TrimSpace(name) == "" {
		return text
	}
	if newline {
		return name + ":\n" + text
	}
	return name + ": " + text
}

// formatAttributionMD собирает строку с жирным именем в markdown: "**Имя**: текст".
func formatAttributionMD(name, text string, newline bool) string {
	if strings.TrimSpace(name) == "" {
		return text
	}
	bold := "**" + name + "**"
	if newline {
		return bold + ":\n" + text
	}
	return bold + ": " + text
}

// formatAttributionHTML — имя жирным в HTML: "<b>Имя</b>: текст". Имя экранируется;
// text уже должен быть HTML (прошёл через tgEntitiesToHTML).
func formatAttributionHTML(name, text string, newline bool) string {
	if strings.TrimSpace(name) == "" {
		return text
	}
	bold := "<b>" + html.EscapeString(name) + "</b>"
	if newline {
		return bold + ":\n" + text
	}
	return bold + ": " + text
}

// senderAttributionName возвращает имя для подписи bridge-сообщения.
// Отключённая подпись означает полностью пустую атрибуцию: остаётся только текст.
func senderAttributionName(name string, showName bool) string {
	if !showName {
		return ""
	}
	return name
}

// tgForwardLine — метка «↪️ Переслано из X\n» (HTML, italic) для зеркалируемых форвардов.
// "" если сообщение не форвард. Подмешивается в начало тела (после атрибуции автора).
func tgForwardLine(msg *TGMessage) string {
	// Авто-форвард поста в связанную группу уже подписан именем SenderChat.
	// Повторная строка «Переслано из того же канала» только дублирует атрибуцию.
	if msg.ForwardFrom == "" || msg.IsAutomaticForward {
		return ""
	}
	return "↪️ <i>Переслано из " + html.EscapeString(msg.ForwardFrom) + "</i>\n"
}

// tgContactText — текстовое представление контакта (sharing телефона), чтобы зеркало
// не было пустым. "" если контакта нет.
func tgContactText(msg *TGMessage) string {
	if msg.Contact == nil {
		return ""
	}
	name := strings.TrimSpace(msg.Contact.FirstName + " " + msg.Contact.LastName)
	if name == "" {
		name = "Контакт"
	}
	return "📇 " + name + "\n📞 " + msg.Contact.PhoneNumber
}

// tgHasContent — есть ли в сообщении что пересылать: текст/подпись/контакт или
// поддерживаемое вложение. Неподдерживаемые типы (сторис, опрос, кубик, локация,
// видеочат-события и будущие) без текста дают пустое «Имя:» в MAX — их скипаем.
func tgHasContent(msg *TGMessage) bool {
	if strings.TrimSpace(msg.Text) != "" || strings.TrimSpace(msg.Caption) != "" || tgContactText(msg) != "" {
		return true
	}
	return len(msg.Photo) > 0 || msg.Video != nil || msg.VideoNote != nil || msg.Document != nil ||
		msg.Animation != nil || msg.Sticker != nil || msg.Voice != nil || msg.Audio != nil
}

// formatTgCaption — для пересылки (текст или caption)
func formatTgCaption(msg *TGMessage, prefix, newline bool) string {
	return formatTgCaptionWithName(msg, tgName(msg), prefix, newline)
}

// tgMessageBody — сырое содержимое сообщения: текст, подпись или представление
// контакта. Без атрибуции автора: она накладывается вызывающей стороной ровно раз.
func tgMessageBody(msg *TGMessage) string {
	if msg == nil {
		return ""
	}
	if msg.Text != "" {
		return msg.Text
	}
	if msg.Caption != "" {
		return msg.Caption
	}
	return tgContactText(msg)
}

func formatTgCaptionWithName(msg *TGMessage, name string, prefix, newline bool) string {
	text := tgMessageBody(msg)
	if name == "" {
		return text
	}
	if prefix {
		return formatAttribution("[TG] "+name, text, newline)
	}
	return formatAttribution(name, text, newline)
}

// formatTgMessage — для edit (полный формат)
func formatTgMessage(msg *TGMessage, prefix, newline bool) string {
	name := tgName(msg)
	text := msg.Text
	if text == "" {
		text = msg.Caption
	}
	if text == "" {
		text = tgContactText(msg)
	}
	if text == "" {
		return ""
	}
	if name == "" {
		return text
	}
	if prefix {
		return formatAttribution("[TG] "+name, text, newline)
	}
	return formatAttribution(name, text, newline)
}

func maxName(upd *maxschemes.MessageCreatedUpdate) string {
	name := upd.Message.Sender.Name
	if name == "" {
		name = upd.Message.Sender.Username
	}
	return name
}

// formatMaxCaption — для пересылки
func formatMaxCaption(upd *maxschemes.MessageCreatedUpdate, prefix, newline bool) string {
	return formatMaxCaptionWithName(upd, maxName(upd), prefix, newline)
}

func formatMaxCaptionWithName(upd *maxschemes.MessageCreatedUpdate, name string, prefix, newline bool) string {
	text := upd.Message.Body.Text
	if prefix {
		return formatAttribution("[MAX] "+name, text, newline)
	}
	return formatAttribution(name, text, newline)
}

// formatTgCrosspostCaption — для кросспостинга каналов (без attribution и префиксов).
// Конвертирует entities в HTML (format="html" в MAX), чтобы сохранить ссылки,
// форматирование, КОД и ЦИТАТЫ (MAX-markdown их не рендерит). Replacements
// применяются поверх HTML.
func formatTgCrosspostCaption(msg *TGMessage) string {
	return formatTgCrosspostCaptionRepl(msg, nil)
}

// formatTgCrosspostCaptionRepl — то же, но с применением TG→MAX замен на уровне
// (текст+entities) ДО конвертации в HTML. Так снос видимого текста ссылки удаляет и
// сам text_link (иначе «ВК / MAX» разрывается <a>-тегами и правило по видимому тексту
// не матчит по готовому HTML).
func formatTgCrosspostCaptionRepl(msg *TGMessage, rules []Replacement) string {
	text := msg.Text
	entities := msg.Entities
	if text == "" {
		text = msg.Caption
		entities = msg.CaptionEntities
	}
	if len(rules) > 0 {
		text, entities = applyReplacementsToEntities(text, entities, rules)
	}
	return tgEntitiesToHTML(text, entities)
}

// formatMaxCrosspostCaption — для кросспостинга каналов (без attribution и префиксов)
func formatMaxCrosspostCaption(upd *maxschemes.MessageCreatedUpdate) string {
	return upd.Message.Body.Text
}

// collapseWhitespace убирает «паддинг» из пересылаемого текста: длинные пробельные
// прогоны и лишние пустые строки (часто в постах-простынях), которые раздувают длину
// (лимит MAX ~4000) и уродуют вид. Схлопывает 2+ пробелов/табов/nbsp → 1, обрезает
// хвостовые пробелы строк, 3+ переносов → максимум одна пустая строка.
var (
	reTrailSpace = regexp.MustCompile(`[ \t\x{00A0}]+\n`)
	reManyBlank  = regexp.MustCompile(`\n{3,}`)
	reManySpace  = regexp.MustCompile(`[ \t\x{00A0}]{2,}`)
)

func collapseWhitespace(s string) string {
	if s == "" {
		return s
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = reTrailSpace.ReplaceAllString(s, "\n")
	s = reManyBlank.ReplaceAllString(s, "\n\n")
	s = reManySpace.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// mimeToFilename генерирует имя файла из MIME-типа, если оригинальное имя отсутствует.
func mimeToFilename(base, mime string) string {
	ext := ""
	// sub = часть после "/" в mime type
	if i := strings.Index(mime, "/"); i >= 0 {
		sub := mime[i+1:]
		switch sub {
		case "mp4":
			ext = ".mp4"
		case "webm":
			ext = ".webm"
		case "x-matroska":
			ext = ".mkv"
		case "quicktime":
			ext = ".mov"
		case "mpeg":
			ext = ".mpeg"
		case "ogg":
			ext = ".ogg"
		case "pdf":
			ext = ".pdf"
		case "gif":
			ext = ".gif"
		default:
			ext = "." + sub
		}
	}
	return base + ext
}

// fileNameFromURL извлекает имя файла из URL, fallback "file".
func fileNameFromURL(rawURL string) string {
	if idx := strings.LastIndex(rawURL, "/"); idx >= 0 {
		name := rawURL[idx+1:]
		if q := strings.Index(name, "?"); q >= 0 {
			name = name[:q]
		}
		if name != "" {
			return name
		}
	}
	return "file"
}
