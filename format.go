package main

import (
	"fmt"

	maxschemes "github.com/max-messenger/max-bot-api-client-go/schemes"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func tgName(msg *tgbotapi.Message) string {
	name := msg.From.FirstName
	if msg.From.LastName != "" {
		name += " " + msg.From.LastName
	}
	return name
}

// formatTgCaption — для пересылки (текст или caption)
func formatTgCaption(msg *tgbotapi.Message, prefix bool) string {
	name := tgName(msg)
	text := msg.Text
	if text == "" {
		text = msg.Caption
	}
	if prefix {
		return fmt.Sprintf("[TG] %s: %s", name, text)
	}
	return fmt.Sprintf("%s: %s", name, text)
}

// formatTgMessage — для edit (полный формат)
func formatTgMessage(msg *tgbotapi.Message, prefix bool) string {
	name := tgName(msg)
	text := msg.Text
	if text == "" {
		text = msg.Caption
	}
	if text == "" {
		return ""
	}
	if prefix {
		return fmt.Sprintf("[TG] %s: %s", name, text)
	}
	return fmt.Sprintf("%s: %s", name, text)
}

func maxName(upd *maxschemes.MessageCreatedUpdate) string {
	name := upd.Message.Sender.Name
	if name == "" {
		name = upd.Message.Sender.Username
	}
	return name
}

// formatMaxCaption — для пересылки
func formatMaxCaption(upd *maxschemes.MessageCreatedUpdate, prefix bool) string {
	name := maxName(upd)
	text := upd.Message.Body.Text
	if prefix {
		return fmt.Sprintf("[MAX] %s: %s", name, text)
	}
	return fmt.Sprintf("%s: %s", name, text)
}

// formatTgCrosspostCaption — для кросспостинга каналов (без attribution и префиксов)
func formatTgCrosspostCaption(msg *tgbotapi.Message) string {
	text := msg.Text
	if text == "" {
		text = msg.Caption
	}
	return text
}

// formatMaxCrosspostCaption — для кросспостинга каналов (без attribution и префиксов)
func formatMaxCrosspostCaption(upd *maxschemes.MessageCreatedUpdate) string {
	return upd.Message.Body.Text
}
