package main

import (
	"context"
	"strings"
)

// --- /start и /help как меню ---
// Вместо «портянки» по /start показываем короткий интро + inline-кнопки. Детали
// (как связать группы/каналы, команды, FAQ) — за кнопками (callback help:*),
// редактируем то же сообщение. Кнопки «добавить в группу/канал» — deep-link Telegram.

// tgStartMenu — короткий интро + главное меню.
func (b *Bridge) tgStartMenu() (string, *InlineKeyboardMarkup) {
	text := "🔗 <b>Мост между Telegram и MAX</b>\n\n" +
		"Пишете в одном мессенджере — сообщение автоматически появляется в другом. " +
		"Группы и каналы: текст, фото, видео, голосовые, файлы.\n\n" +
		"🔒 Бот не хранит тексты сообщений — только ID для синхронизации правок, " +
		"и те удаляются через 30 дней.\n\n" +
		"Выберите действие 👇"

	kb := NewInlineKeyboard(
		NewInlineRow(NewInlineButton("➕ Добавить бота", "help:add")),
		NewInlineRow(NewInlineButton("📋 Как связать группы", "help:groups")),
		NewInlineRow(NewInlineButton("🧵 Темы форума", "help:threads")),
		NewInlineRow(NewInlineButton("📡 Кросспостинг каналов", "help:channels")),
		NewInlineRow(
			NewInlineButton("⌨️ Команды", "help:cmds"),
			NewInlineButton("❓ Вопросы", "help:faq"),
		),
	)
	// Дополнительный раздел приходит от расширения.
	if strings.TrimSpace(b.extraHelp) != "" {
		kb.Rows = append(kb.Rows, NewInlineRow(NewInlineButton("✨ Дополнительные возможности", "help:more")))
	}
	// Если задана подробная инструкция (HELP_FILE/help.html) — кнопка на полный текст.
	if customHelp() != "" {
		kb.Rows = append(kb.Rows, NewInlineRow(NewInlineButton("📖 Полная инструкция", "help:full")))
	}
	return text, kb
}

// helpBackKb — клавиатура с одной кнопкой «назад в меню».
func helpBackKb() *InlineKeyboardMarkup {
	return NewInlineKeyboard(NewInlineRow(NewInlineButton("⬅️ Назад", "help:home")))
}

func (b *Bridge) helpGroupsText() string {
	return "📋 <b>Как связать группы</b>\n\n" +
		"1. Добавьте бота в обе группы:\n" +
		"   • Telegram — кнопка «➕ В группу Telegram»\n" +
		"   • MAX — " + b.cfg.MaxBotURL + "\n" +
		"      (если поиск в MAX не находит по полному имени — введите имя <b>без суффикса</b> <code>_bot</code>)\n" +
		"2. В MAX сделайте бота администратором группы <b>с правом «Доступ к сообщениям»</b> " +
		"(читать все сообщения). Без этого права бот не видит команды и сообщения — связка не сработает.\n" +
		"3. В Telegram <b>тоже сделайте бота администратором</b> — иначе он не видит все сообщения.\n" +
		"4. В одной из групп отправьте <code>/bridge</code> — бот выдаст ключ.\n" +
		"5. Отправьте <code>/bridge ключ</code> во второй группе.\n" +
		"6. Готово — сообщения зеркалятся в обе стороны.\n\n" +
		"⚠️ Команду <code>/bridge</code> нельзя отправлять <b>анонимным админом</b> («Оставаться анонимным»): " +
		"бот не сможет определить ваш аккаунт для привязки. Отключите анонимность в правах админа, свяжите группу — потом можно включить обратно." + b.reserveBotHint()
}

func (b *Bridge) helpThreadsText() string {
	return "🧵 <b>Темы форума (threads)</b>\n\n" +
		"Если TG-группа — форум с темами (topics):\n\n" +
		"<code>/thread</code> — выполните <b>внутри нужной темы</b>: сообщения из связанной " +
		"MAX-группы будут приходить именно в эту тему.\n\n" +
		"<code>/thread_bridge</code> — связать <b>отдельную</b> тему с <b>отдельной</b> MAX-группой: " +
		"выполните в теме → бот выдаст ключ → в MAX-группе отправьте <code>/thread_bridge ключ</code>.\n\n" +
		"<code>/thread_unbridge</code> — разорвать связку темы.\n\n" +
		"⚠️ При обычном <code>/bridge</code> на всю группу в MAX уходят сообщения из ВСЕХ тем. " +
		"Чтобы зеркалить только одну тему: <code>/unbridge</code> на группе → <code>/thread_bridge</code> в нужной теме."
}

func (b *Bridge) helpChannelsText() string {
	return "📡 <b>Кросспостинг каналов</b>\n\n" +
		"1. Добавьте бота администратором (с правом постинга) в оба канала:\n" +
		"   • Telegram — " + b.cfg.TgBotURL + "\n" +
		"   • MAX — " + b.cfg.MaxBotURL + "\n" +
		"2. Перешлите пост из TG-канала в личку TG-боту.\n" +
		"3. Бот покажет ID канала — скопируйте.\n" +
		"4. В личке MAX-бота (" + b.cfg.MaxBotURL + ") отправьте: <code>/crosspost ID</code>\n" +
		"5. Перешлите пост из MAX-канала туда же — готово!\n\n" +
		"Управление: <code>/crosspost</code> — список связок с кнопками, либо перешлите " +
		"пост из связанного канала." + b.reserveBotHint()
}

func (b *Bridge) helpCmdsText() string {
	return "⌨️ <b>Команды</b>\n\n" +
		"<code>/bridge</code> — создать ключ для связки групп\n" +
		"<code>/bridge ключ</code> — связать эту группу по ключу\n" +
		"<code>/bridge prefix on|off</code> — префикс [TG]/[MAX]\n" +
		"<code>/bridge direction tg&gt;max|max&gt;tg|both</code> — направление bridge\n" +
		"<code>/pause</code> — поставить связку на паузу (не зеркалить, не удаляя)\n" +
		"<code>/unpause</code> — возобновить пересылку\n" +
		"<code>/unbridge</code> — удалить связку\n" +
		"<code>/thread</code> — направить MAX → текущий топик (форум)\n" +
		"<code>/thread_bridge</code> — связать тред с отдельной MAX-группой\n" +
		"<code>/thread_unbridge</code> — разорвать связку треда\n" +
		"<code>/crosspost</code> — связки каналов и управление"
}

func (b *Bridge) helpFaqText() string {
	return "❓ <b>Частые вопросы</b>\n\n" +
		"<b>Бот хранит мои сообщения?</b>\n" +
		"Нет. Только ID для синхронизации правок, удаляются через 30 дней.\n\n" +
		"<b>Что пересылается?</b>\n" +
		"Текст, фото, видео, голосовые, документы, альбомы, правки.\n\n" +
		"<b>Нужно ли делать бота админом?</b>\n" +
		"Да, и в MAX, и в Telegram — иначе бот не видит все сообщения. В каналах — для постинга.\n\n" +
		"<b>Сообщения не доходят?</b>\n" +
		"Проверьте, что бот добавлен в нужный чат и является администратором.\n\n" +
		"💬 Поддержка: https://t.me/+0ucbOj4wBwQzMWNi"
}

// handleHelpMenuCallback — навигация по меню /start (help:*). Редактирует сообщение.
// Возвращает true, если callback обработан здесь.
func (b *Bridge) handleHelpMenuCallback(ctx context.Context, query *TGCallback, data string) bool {
	if !strings.HasPrefix(data, "help:") {
		return false
	}
	chatID := query.Message.Chat.ID
	msgID := query.Message.MessageID

	var text string
	var kb *InlineKeyboardMarkup
	switch data {
	case "help:home":
		text, kb = b.tgStartMenu()
	case "help:add":
		text = "➕ <b>Куда добавить бота?</b>\n\n" +
			"• <b>Группа Telegram</b> — синхронизация сообщений группы с MAX-группой.\n" +
			"• <b>Канал Telegram</b> — кросспостинг постов канала в MAX.\n" +
			"• <b>MAX-бот</b> — добавить бота в MAX (вторая сторона моста).\n\n" +
			"Выберите 👇"
		kb = NewInlineKeyboard(
			NewInlineRow(InlineKeyboardButton{Text: "➕ В группу Telegram", URL: b.cfg.TgBotURL + "?startgroup=true"}),
			NewInlineRow(InlineKeyboardButton{Text: "➕ В канал Telegram", URL: b.cfg.TgBotURL + "?startchannel=true"}),
			NewInlineRow(InlineKeyboardButton{Text: "🤖 Добавить MAX-бота", URL: b.cfg.MaxBotURL}),
			NewInlineRow(NewInlineButton("⬅️ Назад", "help:home")),
		)
	case "help:groups":
		text, kb = b.helpGroupsText(), helpBackKb()
	case "help:threads":
		text, kb = b.helpThreadsText(), helpBackKb()
	case "help:channels":
		text, kb = b.helpChannelsText(), helpBackKb()
	case "help:cmds":
		text, kb = b.helpCmdsText(), helpBackKb()
	case "help:faq":
		text, kb = b.helpFaqText(), helpBackKb()
	case "help:more":
		if h := strings.TrimSpace(b.extraHelp); h != "" {
			text, kb = h, helpBackKb()
		} else {
			text, kb = b.tgStartMenu()
		}
	case "help:full":
		if ch := customHelp(); ch != "" {
			text, kb = ch, helpBackKb()
		} else {
			text, kb = b.tgStartMenu()
		}
	default:
		b.tg.AnswerCallback(ctx, query.ID, "")
		return true
	}
	b.tg.EditMessageText(ctx, chatID, msgID, text, &SendOpts{ParseMode: "HTML", ReplyMarkup: kb})
	b.tg.AnswerCallback(ctx, query.ID, "")
	return true
}
