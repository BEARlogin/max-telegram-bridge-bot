package main

import (
	"context"
	"strings"

	maxbot "github.com/max-messenger/max-bot-api-client-go"
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

// isMaxUserAdminOrOwner also accepts the authoritative owner_id returned by
// GetChat. Some private MAX groups omit the owner from GetChatAdmins.
func isMaxUserAdminOrOwner(members []maxschemes.ChatMember, ownerID, userID int64) bool {
	return userID != 0 && (isMaxUserAdmin(members, userID) || ownerID == userID)
}

// maxUserCanManageChat verifies that the person selecting a forwarded MAX
// channel is its administrator or owner. Both bot clients are checked because
// production can receive updates through either member of the dual-bot setup.
func (b *Bridge) maxUserCanManageChat(ctx context.Context, chatID, userID int64) bool {
	check := func(api *maxbot.Api) bool {
		if api == nil || userID == 0 {
			return false
		}
		admins, adminsErr := api.Chats.GetChatAdmins(ctx, chatID)
		if adminsErr == nil && isMaxUserAdmin(admins.Members, userID) {
			return true
		}
		chat, chatErr := api.Chats.GetChat(ctx, chatID)
		return chatErr == nil && chat != nil && chat.OwnerId == userID
	}

	selected := b.maxClientFor(ctx, chatID)
	if check(selected) {
		return true
	}
	if !b.dualEnabled() {
		return false
	}
	if selected != b.maxApi && check(b.maxApi) {
		return true
	}
	return selected != b.maxApiOld && check(b.maxApiOld)
}

func (b *Bridge) maxChatType(ctx context.Context, chatID int64) (maxschemes.ChatType, bool) {
	check := func(api *maxbot.Api) (maxschemes.ChatType, bool) {
		if api == nil {
			return "", false
		}
		chat, err := api.Chats.GetChat(ctx, chatID)
		if err != nil || chat == nil {
			return "", false
		}
		return chat.Type, true
	}

	selected := b.maxClientFor(ctx, chatID)
	if typ, ok := check(selected); ok {
		return typ, true
	}
	if b.dualEnabled() {
		if selected != b.maxApi {
			if typ, ok := check(b.maxApi); ok {
				return typ, true
			}
		}
		if selected != b.maxApiOld {
			return check(b.maxApiOld)
		}
	}
	return "", false
}
