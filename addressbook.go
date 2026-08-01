package main

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	maxschemes "github.com/max-messenger/max-bot-api-client-go/schemes"
)

const maxUserAliasRunes = 80

type nameCommandMode int

const (
	nameCommandShow nameCommandMode = iota
	nameCommandSet
	nameCommandReset
)

func parseNameCommand(text string) (nameCommandMode, string, bool) {
	if text == "/name" {
		return nameCommandShow, "", true
	}
	if !strings.HasPrefix(text, "/name ") {
		return 0, "", false
	}
	arg := strings.TrimSpace(strings.TrimPrefix(text, "/name "))
	switch strings.ToLower(arg) {
	case "reset", "delete", "off", "сбросить", "удалить":
		return nameCommandReset, "", true
	default:
		return nameCommandSet, arg, true
	}
}

func validateUserAlias(alias string) (string, error) {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return "", fmt.Errorf("empty alias")
	}
	if !utf8.ValidString(alias) || utf8.RuneCountInString(alias) > maxUserAliasRunes {
		return "", fmt.Errorf("alias too long")
	}
	for _, r := range alias {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("control character")
		}
	}
	return alias, nil
}

func (b *Bridge) observeTgMessageAuthor(msg *TGMessage) {
	if msg == nil || msg.From == nil || msg.From.ID <= 0 || msg.From.IsBot ||
		isTgServiceSender(msg.From.ID) || isTgAnonymousAdmin(msg) {
		return
	}
	b.repo.SaveMessageAuthor("tg", msg.Chat.ID, strconv.Itoa(msg.MessageID), MessageAuthor{
		Platform: "tg",
		ChatID:   msg.Chat.ID,
		UserID:   msg.From.ID,
	})
}

func (b *Bridge) observeMaxMessageAuthor(upd *maxschemes.MessageCreatedUpdate) {
	if upd == nil || upd.Message.Sender.UserId <= 0 || upd.Message.Sender.IsBot ||
		b.isSelfMaxBot(upd.Message.Sender.UserId) {
		return
	}
	b.repo.SaveMessageAuthor("max", upd.Message.Recipient.ChatId, upd.Message.Body.Mid, MessageAuthor{
		Platform: "max",
		ChatID:   upd.Message.Recipient.ChatId,
		UserID:   upd.Message.Sender.UserId,
	})
}

func (b *Bridge) resolveTgNameTarget(msg *TGMessage) (MessageAuthor, bool) {
	if msg == nil || msg.ReplyToMessage == nil {
		return MessageAuthor{}, false
	}
	reply := msg.ReplyToMessage
	if author, ok := b.repo.LookupMessageAuthor("tg", msg.Chat.ID, strconv.Itoa(reply.MessageID)); ok && author.UserID > 0 {
		return author, true
	}
	if maxChatID, maxMsgID, origin, ok := b.repo.LookupMessageRouteByTg(msg.Chat.ID, reply.MessageID); ok && origin == "max" {
		if author, found := b.repo.LookupMessageAuthor("max", maxChatID, maxMsgID); found && author.UserID > 0 {
			return author, true
		}
	}
	if reply.From == nil || reply.From.ID <= 0 || reply.From.IsBot ||
		isTgServiceSender(reply.From.ID) || isTgAnonymousAdmin(reply) {
		return MessageAuthor{}, false
	}
	return MessageAuthor{Platform: "tg", ChatID: msg.Chat.ID, UserID: reply.From.ID}, true
}

func maxReplyMessageID(upd *maxschemes.MessageCreatedUpdate) string {
	if upd == nil {
		return ""
	}
	if upd.Message.Body.ReplyTo != "" {
		return upd.Message.Body.ReplyTo
	}
	if upd.Message.Link != nil && upd.Message.Link.Type == maxschemes.REPLY {
		return upd.Message.Link.Message.Mid
	}
	return ""
}

func (b *Bridge) resolveMaxNameTarget(upd *maxschemes.MessageCreatedUpdate) (MessageAuthor, bool) {
	if upd == nil {
		return MessageAuthor{}, false
	}
	chatID := upd.Message.Recipient.ChatId
	if mid := maxReplyMessageID(upd); mid != "" {
		if author, ok := b.repo.LookupMessageAuthor("max", chatID, mid); ok && author.UserID > 0 {
			return author, true
		}
		if tgChatID, tgMsgID, _, origin, ok := b.repo.LookupMessageRouteByMax(mid); ok && origin == "tg" {
			if author, found := b.repo.LookupMessageAuthor("tg", tgChatID, strconv.Itoa(tgMsgID)); found && author.UserID > 0 {
				return author, true
			}
		}
	}
	if upd.Message.Link == nil || upd.Message.Link.Type != maxschemes.REPLY {
		return MessageAuthor{}, false
	}
	sender := upd.Message.Link.Sender
	if sender.UserId <= 0 || sender.IsBot || b.isSelfMaxBot(sender.UserId) {
		return MessageAuthor{}, false
	}
	return MessageAuthor{Platform: "max", ChatID: chatID, UserID: sender.UserId}, true
}

func (b *Bridge) applyNameCommand(target MessageAuthor, mode nameCommandMode, alias string, updatedBy int64) string {
	if target.UserID <= 0 || (target.Platform != "tg" && target.Platform != "max") || target.ChatID == 0 {
		return "Не удалось определить участника. Ответьте на его сообщение или на сообщение, которое перенёс «Мост»."
	}
	switch mode {
	case nameCommandShow:
		if current, ok := b.repo.GetUserAlias(target.Platform, target.ChatID, target.UserID); ok {
			return "Сохранённое имя: «" + current + "»."
		}
		return "Для этого участника отдельное имя не задано."
	case nameCommandReset:
		if b.repo.DeleteUserAlias(target.Platform, target.ChatID, target.UserID) {
			return "Готово. Сохранённое имя удалено."
		}
		return "Для этого участника отдельное имя не было задано."
	case nameCommandSet:
		clean, err := validateUserAlias(alias)
		if err != nil {
			return fmt.Sprintf("Имя должно занимать одну строку и быть не длиннее %d символов.", maxUserAliasRunes)
		}
		if err := b.repo.SetUserAlias(target.Platform, target.ChatID, target.UserID, clean, updatedBy); err != nil {
			return "Не удалось сохранить имя. Попробуйте ещё раз."
		}
		return "Готово. Теперь сообщения участника будут подписываться: «" + clean + "»."
	default:
		return "Неизвестная команда."
	}
}

func (b *Bridge) tgRelayName(msg *TGMessage) string {
	if msg == nil {
		return ""
	}
	name := tgName(msg)
	if msg.From == nil || msg.From.ID <= 0 || isTgAnonymousAdmin(msg) {
		return name
	}
	if alias, ok := b.repo.GetUserAlias("tg", msg.Chat.ID, msg.From.ID); ok && strings.TrimSpace(alias) != "" {
		return alias
	}
	return name
}

func (b *Bridge) maxRelayName(upd *maxschemes.MessageCreatedUpdate) string {
	if upd == nil {
		return ""
	}
	name := maxName(upd)
	if upd.Message.Sender.UserId <= 0 {
		return name
	}
	return b.maxUserRelayName(upd.Message.Recipient.ChatId, upd.Message.Sender.UserId, name)
}

func (b *Bridge) maxUserRelayName(chatID, userID int64, fallback string) string {
	if userID <= 0 {
		return fallback
	}
	if alias, ok := b.repo.GetUserAlias("max", chatID, userID); ok && strings.TrimSpace(alias) != "" {
		return alias
	}
	return fallback
}
