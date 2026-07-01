// Package spamfilter — эвристический детектор спама на регулярках и нормализации
// юникода. Без ML. Чистые функции, не зависит от бриджа/аддона — легко тестировать.
//
// Идея: спамеры обходят простые фильтры подменой символов (кириллица↔латиница),
// невидимыми символами, разрядкой, leet, fullwidth-юникодом. Поэтому сначала
// приводим текст к «скелету» (нормализуем), затем считаем взвешенные сигналы.
package spamfilter

import (
	"regexp"
	"strings"
	"unicode"
)

// Signal — отдельная сработавшая эвристика.
type Signal struct {
	Code   string // машинный код (mixed-script, link-new, kw-money, …)
	Detail string // человекочитаемая деталь (что именно нашли)
	Weight int    // вклад в score
}

// Result — итог анализа.
type Result struct {
	Spam       bool
	Score      int
	Signals    []Signal
	Normalized string // «скелет» текста (для логов/отладки)
}

// Config — настройки детектора (per-chat).
type Config struct {
	Threshold     int      // порог score для вердикта «спам» (0 ⇒ DefaultThreshold)
	ExtraKeywords []string // доп. стоп-слова владельца (матчатся по скелету)
	Allowlist     []string // если найдено — не считаем спамом (имена, термины)
	HasLink       bool     // в сообщении есть ссылка/URL-entity (от вызывающего; TG отдаёт entities)
	IsNewUser     bool     // автор — «новичок» (для правила задержки ссылок)
	AllowLinks    bool     // ссылки разрешены этому автору (доверенный/админ)
}

// DefaultThreshold — score, начиная с которого сообщение считается спамом.
const DefaultThreshold = 5

var (
	reURL       = regexp.MustCompile(`(?i)(https?://|www\.)\S+`)
	reTme       = regexp.MustCompile(`(?i)(t\.me/|telegram\.me/|telega\.|joinchat|@[a-z0-9_]{4,})`)
	reShortener = regexp.MustCompile(`(?i)\b(bit\.ly|goo\.gl|tinyurl|clck\.ru|vk\.cc|cutt\.ly|is\.gd|t\.co|rb\.gy|surl\.li)\b`)
	rePhone     = regexp.MustCompile(`(?:\+?\d[\s\-\(\)]?){10,15}`)
)

// hasLongRepeat — есть ли прогон из 5+ одинаковых рун подряд (RE2 без backref).
func hasLongRepeat(s string) bool {
	var prev rune
	run := 0
	for _, r := range s {
		if r == prev {
			run++
			if run >= 5 {
				return true
			}
		} else {
			prev = r
			run = 1
		}
	}
	return false
}

// Стоп-слова двух тиров (матчатся по скелету; писать можно читаемо с пробелами).
//
// hard — скам-призывы и тематика, которая почти всегда спам (казино/дроги/интим).
// Одно совпадение — умеренный сигнал (нужен ещё один), два+ — почти гарантия.
// soft — тематические слова (крипта, биржа, инвестиции, заработок): сами по себе
// НЕ спам (легитимное обсуждение), считаются слабым добавочным сигналом.
var hardKeywords = []string{
	"пиши в личку", "пишите в лс", "пиши в лс", "подробности в личку", "жду в личке", "пишите в личку",
	"от 1000 в день", "от 5000 в день", "от 10000 в день", "пассивный доход", "доход в день", "доход от",
	"набор людей", "требуются люди", "удаленная работа", "работа на дому", "ищу партнеров", "нужны люди",
	"казино", "1xbet", "1win", "букмекер", "фрибет", "ставки на спорт", "рулетка онлайн", "вавада", "джекпот",
	"гашиш", "мефедрон", "амфетамин", "закладк", "соли меф",
	"интим услуги", "секс знакомства", "вебкам", "онлайн вебка",
	// латиница-транслит (skeleton нормализует так же, как текст; с англ. не пересекается)
	"pishi v lichku", "pishite v ls", "dohod v den", "dohod ot", "nuzhny lyudi",
	"trebuyutsya lyudi", "udalennaya rabota", "rabota na domu", "passivnyi dohod",
	"kazino", "casino", "cazino", "vavada", "bukmeker", "fribet", "stavki na sport", "ruletka",
	"mefedron", "amfetamin", "zakladka", "intim uslugi",
}
var softKeywords = []string{
	// стем «зарабо» ловит заработок/заработать/заработай/зарабатывать (substring по скелету)
	"зарабо", "миллион", "млн", "инвестиции", "инвестируй", "крипта", "криптовалют", "биржа", "трейдинг", "трейдер", "выплаты",
	// крипто-раздачи/скам-подарки
	"usdt", "биткоин", "бонус", "промокод", "подарок", "забери", "розыгрыш", "приз",
	"zarabo", "million", "investicii", "kripta", "birzha", "treyding", "bitcoin", "bonus", "promokod",
}

// При старте приводим стоп-слова к скелету (как и текст при анализе).
func init() {
	for i := range hardKeywords {
		hardKeywords[i] = skeleton(hardKeywords[i])
	}
	for i := range softKeywords {
		softKeywords[i] = skeleton(softKeywords[i])
	}
}

// confusable — строчные буквы-двойники → канонической кириллице. Только надёжные
// визуальные омоглифы (после ToLower): a с e o p x y k. Этого достаточно, чтобы
// «зaрaбoтoк» (латинские a/o) сложился к кириллическому канону. Расширять опасно —
// неомоглифы (b,n,t,h,m) искажают легитимные слова.
var confusable = map[rune]rune{
	'a': 'а', 'c': 'с', 'e': 'е', 'o': 'о', 'p': 'р', 'x': 'х', 'y': 'у', 'k': 'к',
	'ё': 'е',
	// греческие двойники → кириллица (после ToLower; заглавные тоже сюда складываются)
	'α': 'а', 'ε': 'е', 'ο': 'о', 'ρ': 'р', 'χ': 'х', 'κ': 'к', 'ι': 'и', 'υ': 'у',
	'γ': 'у', 'τ': 'т', 'π': 'п', 'ν': 'н', 'β': 'в', 'μ': 'м', 'σ': 'с', 'ς': 'с', 'η': 'н', 'ζ': 'з',
}

// leet — цифры-двойники букв (0→о, 3→з, 6→б). Применяются симметрично к тексту и
// к стоп-словам, поэтому числовые слова («от 5000 в день») не ломаются. 1/5 не
// трогаем — нужны в «1xbet/1win» и суммах.
var leet = map[rune]rune{'0': 'о', '3': 'з', '6': 'б'}

// isInvisible — невидимые символы, которыми разбивают слова (коды, не литералы).
func isInvisible(r rune) bool {
	switch r {
	case 0x200B, 0x200C, 0x200D, 0x2060, 0xFEFF, 0x00AD, 0x034F, 0x061C,
		0x115F, 0x1160, 0x3164, 0x180E, 0x2061, 0x2062, 0x2063, 0x2064:
		return true
	}
	return false
}

// scriptOf — грубое определение алфавита руны (для mixed-script).
func scriptOf(r rune) string {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		return "lat"
	case r >= 'Ѐ' && r <= 'ӿ':
		return "cyr"
	case (r >= 0x0370 && r <= 0x03FF) || (r >= 0x1F00 && r <= 0x1FFF):
		return "grk" // греческий (и расширенный) — частый источник гомоглифов
	default:
		return ""
	}
}

// Deobfuscate приводит текст к читаемому виду для LLM: снимает невидимые символы,
// маппит буквоподобные эмодзи/математический юникод в обычные буквы и схлопывает
// разрядку (склеивает прогоны одиночных букв). Сохраняет слова/пробелы — в отличие
// от skeleton (тот всё слепляет для матчинга ключевиков). Нужен, чтобы LLM получала
// нормальный текст, а не декоративные байты, которые модель не читает.
func Deobfuscate(text string) string {
	clean, _ := stripInvisible(text)
	var mapped []rune
	for _, r := range clean {
		if e, ok := emojiLetter(r); ok {
			r = e
		}
		mapped = append(mapped, foldWidth(r))
	}
	// Схлопываем разрядку: прогоны из 2+ одиночных букв подряд склеиваем в слово.
	toks := strings.Fields(string(mapped))
	var out, run []string
	flushRun := func() {
		if len(run) >= 2 {
			out = append(out, strings.Join(run, ""))
		} else {
			out = append(out, run...)
		}
		run = nil
	}
	for _, t := range toks {
		rt := []rune(t)
		if len(rt) == 1 && unicode.IsLetter(rt[0]) {
			run = append(run, t)
		} else {
			flushRun()
			out = append(out, t)
		}
	}
	flushRun()
	return strings.Join(out, " ")
}

// stripInvisible убирает невидимые символы и комбинирующие диакритики (Zalgo).
func stripInvisible(s string) (string, bool) {
	var b strings.Builder
	found := false
	for _, r := range s {
		if isInvisible(r) {
			found = true
			continue
		}
		if unicode.Is(unicode.Mn, r) { // combining mark
			found = true
			continue
		}
		b.WriteRune(r)
	}
	return b.String(), found
}

// hasRTLOverride — символы смены направления письма (маскировка).
func hasRTLOverride(s string) bool {
	for _, r := range s {
		if (r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069) {
			return true
		}
	}
	return false
}

// foldWidth приводит fullwidth и математический алфавит к ASCII.
func foldWidth(r rune) rune {
	switch {
	case r >= 0xFF01 && r <= 0xFF5E: // fullwidth ASCII
		return r - 0xFEE0
	// Математические алфавитно-цифровые символы (U+1D400–U+1D7FF): все стили буква→ASCII.
	// Каждый стиль — 26 заглавных, затем 26 строчных; %26 + чёт/нечёт половины.
	case r >= 0x1D400 && r <= 0x1D6A3: // bold/italic/script/fraktur/dblstruck/sans/mono буквы
		off := (r - 0x1D400) % 52
		if off < 26 {
			return 'A' + off
		}
		return 'a' + (off - 26)
	case r >= 0x1D7CE && r <= 0x1D7FF: // математические цифры (bold/dblstruck/sans/mono)
		return '0' + (r-0x1D7CE)%10
	}
	return r
}

// emojiLetter маппит «буквоподобные» эмодзи/символы в обычную латиницу a-z.
// Спамеры собирают из них слова, обходя текстовые фильтры: региональные индикаторы
// 🇸🇵🇦🇲, квадратные/круглые латинские эмодзи 🄰🅰🅐, обведённые буквы Ⓢⓟⓐⓜ.
func emojiLetter(r rune) (rune, bool) {
	switch {
	case r >= 0x1F1E6 && r <= 0x1F1FF: // 🇦-🇿 региональные индикаторы
		return 'a' + (r - 0x1F1E6), true
	case r >= 0x1F130 && r <= 0x1F149: // 🄰-🄩 squared latin
		return 'a' + (r - 0x1F130), true
	case r >= 0x1F150 && r <= 0x1F169: // 🅐-🅩 negative circled
		return 'a' + (r - 0x1F150), true
	case r >= 0x1F170 && r <= 0x1F189: // 🅰-🆉 negative squared
		return 'a' + (r - 0x1F170), true
	case r >= 0x24B6 && r <= 0x24CF: // Ⓐ-Ⓩ circled
		return 'a' + (r - 0x24B6), true
	case r >= 0x24D0 && r <= 0x24E9: // ⓐ-ⓩ circled small
		return 'a' + (r - 0x24D0), true
	case r >= 0x249C && r <= 0x24B5: // ⒜-⒵ parenthesized
		return 'a' + (r - 0x249C), true
	}
	return r, false
}

// emojiLetterCount — сколько в тексте буквоподобных эмодзи (для сигнала «слово из эмодзи»).
func emojiLetterCount(s string) int {
	n := 0
	for _, r := range s {
		if _, ok := emojiLetter(r); ok {
			n++
		}
	}
	return n
}

// skeleton строит каноническую форму: нижний регистр → foldWidth (fullwidth/мат.
// юникод → ascii) → confusable (латинские двойники → кириллица) → оставляем только
// буквы и цифры. Пробелы/знаки/эмодзи выкидываем — это схлопывает разрядку
// («з а р а б о т о к» → «заработок»). Канон в кириллице; стоп-слова — кириллицей.
func skeleton(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if e, ok := emojiLetter(r); ok { // 🇸🇵🇦🇲 / 🅰🅱 / Ⓢⓟⓐⓜ → буквы
			r = e
		}
		r = foldWidth(r)
		// lower ПОСЛЕ fold и ДО confusable: ToLower в начале не кейс-фолдит мат.символы,
		// а foldWidth даёт заглавные ASCII — иначе confusable (нижний регистр) и ключевики не сработают.
		r = unicode.ToLower(r)
		if c, ok := confusable[r]; ok {
			r = c
		}
		if l, ok := leet[r]; ok {
			r = l
		}
		if unicode.IsLetter(r) || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// mixedScriptTokens считает токены, где намешаны кириллица и латиница.
func mixedScriptTokens(s string) int {
	n := 0
	for _, tok := range strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) }) {
		scripts := map[string]bool{}
		for _, r := range tok {
			if sc := scriptOf(r); sc != "" {
				scripts[sc] = true
			}
		}
		if len(scripts) >= 2 { // намешано ≥2 алфавитов в одном слове (lat/cyr/grk)
			n++
		}
	}
	return n
}

// leetInWord — есть ли leet-цифра (0/3/6) с буквой-соседом, т.е. подмена внутри
// слова («ка3ино»). Отдельно стоящие числа («5000») не считаются — у них соседи
// не буквы. Легитимный текст так буквы не подменяет → сильный сигнал обхода.
func leetInWord(s string) bool {
	rs := []rune(s)
	for i, r := range rs {
		if _, ok := leet[r]; !ok {
			continue
		}
		prevLetter := i > 0 && unicode.IsLetter(rs[i-1])
		nextLetter := i+1 < len(rs) && unicode.IsLetter(rs[i+1])
		if prevLetter || nextLetter {
			return true
		}
	}
	return false
}

// digitsInsideWords — цифры, у которых буква и слева, И справа («к4зино», «за6аб0ток»).
// Легит-цифры стоят в конце (iphone15, airpods3) или в начале (1с, 3д, 2гис, 4к) — их
// не считаем. Цифра, зажатая буквами с обеих сторон, — почти всегда подмена буквы.
func digitsInsideWords(s string) int {
	rs := []rune(s)
	n := 0
	for i := 1; i < len(rs)-1; i++ {
		if rs[i] >= '0' && rs[i] <= '9' && unicode.IsLetter(rs[i-1]) && unicode.IsLetter(rs[i+1]) {
			n++
		}
	}
	return n
}

// spacingEvasion — много однобуквенных «слов» = разрядка («з а р а б о т о к»).
func spacingEvasion(s string) int {
	single := 0
	for _, tok := range strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) }) {
		if len([]rune(tok)) == 1 {
			single++
		}
	}
	return single
}

// capsRatio — доля заглавных среди букв (для крик-спама).
func capsRatio(s string) float64 {
	var letters, upper int
	for _, r := range s {
		if unicode.IsLetter(r) {
			letters++
			if unicode.IsUpper(r) {
				upper++
			}
		}
	}
	if letters < 8 {
		return 0
	}
	return float64(upper) / float64(letters)
}

// emojiCount — грубый счётчик эмодзи/символов вне базовых алфавитов.
func emojiCount(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x1F000 || (r >= 0x2600 && r <= 0x27BF) {
			n++
		}
	}
	return n
}

// countHits — сколько различных слов из списка встретилось в скелете (+ образец).
func countHits(skel string, list []string) (int, string) {
	n := 0
	sample := ""
	for _, kw := range list {
		if kw != "" && strings.Contains(skel, kw) {
			n++
			if sample == "" {
				sample = kw
			}
		}
	}
	return n, sample
}

func containsAny(hay string, needles []string) (string, bool) {
	for _, n := range needles {
		if n == "" {
			continue
		}
		if strings.Contains(hay, n) {
			return n, true
		}
	}
	return "", false
}

// Analyze — главный вход. Возвращает score, сигналы и вердикт.
func Analyze(text string, cfg Config) Result {
	thr := cfg.Threshold
	if thr <= 0 {
		thr = DefaultThreshold
	}

	clean, hadInvisible := stripInvisible(text)
	skel := skeleton(clean)

	// Allowlist: если есть «разрешённое» слово — не трогаем (по скелету).
	if _, ok := containsAny(skel, normSlice(cfg.Allowlist)); ok {
		return Result{Spam: false, Normalized: skel}
	}

	var sigs []Signal
	add := func(code, detail string, w int) { sigs = append(sigs, Signal{code, detail, w}) }

	// Структурные сигналы обхода.
	if hadInvisible {
		add("invisible", "невидимые символы/диакритики", 3)
	}
	if hasRTLOverride(clean) {
		add("rtl", "переопределение направления письма", 4)
	}
	if m := mixedScriptTokens(clean); m > 0 {
		w := 3
		if m > 1 {
			w = 5
		}
		add("mixed-script", "смешанные алфавиты в словах: "+itoa(m), w)
	}
	if sp := spacingEvasion(clean); sp >= 5 {
		// Лёгкая разрядка — умеренный сигнал; тяжёлая (целые слова по буквам) — почти
		// наверняка обход фильтра, нормальные люди так не пишут.
		w := 3
		if sp >= 10 {
			w = 6
		}
		add("spacing", "разрядка слов одиночными буквами: "+itoa(sp), w)
	}
	// Цифра, зажатая буквами с ОБЕИХ сторон («к4зино», «ка3ино») — подмена буквы.
	// Покрывает и leet (0/3/6), и любые цифры; трейлинг (iphone15) и лидинг (1с/3д) не трогает.
	// Leet-нормализация чисел в буквы делается в skeleton — ключевики всё равно матчатся.
	if d := digitsInsideWords(clean); d > 0 {
		w := 3
		if d >= 2 {
			w = 5
		}
		add("digit-in-word", "цифры в середине слов: "+itoa(d), w)
	}
	if el := emojiLetterCount(clean); el >= 3 {
		// слова, собранные из эмодзи/буквоподобных символов — почти всегда обход фильтра
		add("emoji-letters", "слово из эмодзи/буквоподобных символов: "+itoa(el), 6)
	}

	// Ссылки/контакты.
	hasLink := cfg.HasLink || reURL.MatchString(clean) || reTme.MatchString(clean)
	if reShortener.MatchString(clean) {
		add("shortener", "сокращатель ссылок", 4)
	}
	if rePhone.MatchString(strings.ReplaceAll(clean, " ", "")) {
		add("phone", "номер телефона", 2)
	}

	// Правило «задержка ссылок для новичков».
	if hasLink && cfg.IsNewUser && !cfg.AllowLinks {
		add("link-new", "ссылка от новичка", 6)
	}

	// Жёсткие стоп-слова: 1 совпадение — умеренный сигнал (нужен ещё один, чтобы
	// не банить за невинное «пиши в лс»); 2+ — почти гарантированный спам.
	if h, sample := countHits(skel, hardKeywords); h >= 2 {
		add("kw-hard-multi", sample, 8)
	} else if h == 1 {
		add("kw-hard", sample, 4)
	}
	// Мягкие (тематические) слова — слабый добавочный сигнал, сами по себе не спам.
	if s, sample := countHits(skel, softKeywords); s > 0 {
		w := s
		if w > 3 {
			w = 3
		}
		add("kw-soft", sample, w)
	}
	// Кастомные стоп-слова владельца — сразу сильный сигнал (он сам их задал).
	if kw, ok := containsAny(skel, normSlice(cfg.ExtraKeywords)); ok {
		add("kw-custom", kw, 5)
	}

	// Шумовые сигналы.
	if capsRatio(clean) > 0.7 {
		add("caps", "сплошной капс", 1)
	}
	if e := emojiCount(clean); e >= 8 {
		// Много эмодзи — частый способ «написать» слова кастом-эмодзи Telegram или
		// символами, обходя текстовые фильтры. Деобфусцировать кастом-эмодзи нельзя
		// (это проприетарные сущности), но сама их масса — сильный сигнал.
		w := 5
		if e >= 12 {
			w = 7
		}
		add("emoji-flood", "много эмодзи (вероятно текст из эмодзи): "+itoa(e), w)
	}
	if hasLongRepeat(clean) {
		add("repeat", "повтор символов", 1)
	}

	score := 0
	for _, s := range sigs {
		score += s.Weight
	}
	return Result{Spam: score >= thr, Score: score, Signals: sigs, Normalized: skel}
}

// IsObfuscated — сработал ли хоть один сигнал маскировки (невидимые, разрядка,
// смешанные алфавиты, leet, RTL, эмодзи-буквы, флуд эмодзи). На таком тексте LLM
// видит мусор и ненадёжна — доверяем статике, не даём модели понижать вердикт.
func IsObfuscated(r Result) bool {
	for _, s := range r.Signals {
		switch s.Code {
		case "invisible", "rtl", "mixed-script", "spacing", "leet", "digit-in-word", "emoji-letters", "emoji-flood":
			return true
		}
	}
	return false
}

// DescribeSignals — краткая сводка сработавших сигналов (коды через запятую).
func DescribeSignals(r Result) string {
	codes := make([]string, 0, len(r.Signals))
	for _, s := range r.Signals {
		codes = append(codes, s.Code)
	}
	return strings.Join(codes, ",")
}

// normSlice приводит пользовательские слова к скелету (чтобы матч был устойчив).
func normSlice(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if sk := skeleton(s); sk != "" {
			out = append(out, sk)
		}
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
