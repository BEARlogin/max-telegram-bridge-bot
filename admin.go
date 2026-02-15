package main

import maxschemes "github.com/max-messenger/max-bot-api-client-go/schemes"

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
