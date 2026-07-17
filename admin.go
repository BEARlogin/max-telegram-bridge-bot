package main

import (
	"strings"

	maxschemes "github.com/max-messenger/max-bot-api-client-go/schemes"
)

// isTgGroup returns true if the TG chat type indicates a group.
func isTgGroup(chatType string) bool {
	return chatType == "group" || chatType == "supergroup"
}

// isTgChannel returns true if the TG chat type is a channel.
func isTgChannel(chatType string) bool {
	return chatType == "channel"
}

// isTgAdmin returns true if the TG ChatMember status indicates admin rights.
func isTgAdmin(memberStatus string) bool {
	return memberStatus == "creator" || memberStatus == "administrator"
}

// isTgAnonymousAdmin returns true if the message was sent by an anonymous admin
// (owner/admin with "Remain anonymous" enabled). In this case msg.From is
// @GroupAnonymousBot and msg.SenderChat is the group itself.
func isTgAnonymousAdmin(msg *TGMessage) bool {
	if msg == nil || msg.SenderChat == nil {
		return false
	}
	return msg.SenderChat.ID == msg.Chat.ID
}

// parseModCommand распознаёт быструю модер-команду (/ban /mute /unban /unmute),
// с опциональным @botname и аргументами. ok=false — не модер-команда.
func parseModCommand(text string) (cmd, arg string, ok bool) {
	if !strings.HasPrefix(text, "/") {
		return "", "", false
	}
	head, rest, _ := strings.Cut(text, " ")
	if i := strings.IndexByte(head, '@'); i > 0 {
		head = head[:i]
	}
	switch head {
	case "/ban", "/mute", "/unban", "/unmute":
		return strings.TrimPrefix(head, "/"), strings.TrimSpace(rest), true
	}
	return "", "", false
}

// isMaxGroup returns true if the MAX chat type indicates a group.
func isMaxGroup(chatType maxschemes.ChatType) bool {
	return chatType == maxschemes.CHAT || chatType == maxschemes.CHANNEL
}

// isMaxUserAdmin returns true if userID is found in the admin members list.
func isMaxUserAdmin(members []maxschemes.ChatMember, userID int64) bool {
	for _, m := range members {
		if m.UserId == userID {
			return true
		}
	}
	return false
}
