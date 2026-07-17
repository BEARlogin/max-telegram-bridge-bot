package main

import (
	"strconv"
	"sync"
	"time"
)

// botChatSeen — троттлинг записи bot_chats (не писать в БД на каждое сообщение).
var botChatSeen sync.Map // "platform:chatID" → unix последней записи

// noteBotChat запоминает чат, где есть бот (для мастера линковки в кабинете).
// Троттлится: пишет не чаще раза в 10 минут на чат.
func (b *Bridge) noteBotChat(platform string, chatID int64, title, chatType string) {
	if chatID == 0 {
		return
	}
	key := platform + ":" + strconv.FormatInt(chatID, 10)
	now := time.Now().Unix()
	if v, ok := botChatSeen.Load(key); ok {
		if last, _ := v.(int64); now-last < 600 {
			return
		}
	}
	botChatSeen.Store(key, now)
	b.repo.RecordBotChat(platform, chatID, title, chatType)
}
