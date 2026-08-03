package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

var (
	errTgCrosspostBotNoAccess  = errors.New("telegram bot cannot access crosspost channel")
	errTgCrosspostUserNotAdmin = errors.New("telegram user is not a crosspost channel admin")
)

// validateTgCrosspostSource verifies the two conditions that Telegram requires
// before it will deliver channel_post updates to us: the bot must see the
// channel and the user who registered it must be an administrator. requesterID
// may be zero for legacy flows where only bot access can be checked.
func (b *Bridge) validateTgCrosspostSource(ctx context.Context, channelID, requesterID int64) (string, error) {
	title, err := b.tg.GetChat(ctx, channelID)
	if err != nil {
		slog.Warn("TG crosspost source unavailable", "tgChannel", channelID, "err", err)
		return "", errTgCrosspostBotNoAccess
	}
	if requesterID != 0 {
		status, memberErr := b.tg.GetChatMember(ctx, channelID, requesterID)
		if memberErr != nil || !isTgAdmin(status) {
			slog.Warn("TG crosspost source owner is not admin", "tgChannel", channelID,
				"tgUser", requesterID, "status", status, "err", memberErr)
			return title, errTgCrosspostUserNotAdmin
		}
	}
	return title, nil
}

func (b *Bridge) tgCrosspostAccessText(channelID int64, err error) string {
	botName := strings.TrimSpace(b.tg.BotUsername())
	if botName == "" {
		botName = "Telegram-бота «Моста»"
	} else if !strings.HasPrefix(botName, "@") {
		botName = "@" + botName
	}
	if errors.Is(err, errTgCrosspostUserNotAdmin) {
		return fmt.Sprintf("Не удалось подключить TG-канал %d: ваш Telegram-аккаунт не является его администратором. Настройку должен выполнить администратор канала.", channelID)
	}
	return fmt.Sprintf("Не удалось подключить TG-канал %d: бот не получает его публикации.\n\nДобавьте %s администратором в этот Telegram-канал и повторите настройку.", channelID, botName)
}
