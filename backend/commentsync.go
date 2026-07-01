package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
)

func commentSyncSecret() string { return os.Getenv("COMMENT_SYNC_SECRET") }

// handleInternalComment — приём коммента из группы обсуждения TG (бридж постит сюда).
// Защищён общим секретом. Сохраняет в стор как source="tg" и НЕ шлёт обратно в TG.
func (s *server) handleInternalComment(w http.ResponseWriter, r *http.Request) {
	secret := commentSyncSecret()
	var in struct {
		PostID       string `json:"post_id"`
		Author       string `json:"author"`
		AuthorID     int64  `json:"author_id"`
		Text         string `json:"text"`
		Secret       string `json:"secret"`
		TgMsgID      int64  `json:"tg_msg_id"`       // id этого сообщения в обсуждении
		ReplyToTgMsg int64  `json:"reply_to_tg_msg"` // id сообщения, на которое это ответ
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if secret == "" || in.Secret != secret {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	in.PostID = strings.TrimSpace(in.PostID)
	in.Text = strings.TrimSpace(in.Text)
	if in.PostID == "" || in.Text == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	// Дедуп: это сообщение TG уже заносили? (нотификации/ретраи). Иначе — reply-резолв.
	if in.TgMsgID != 0 {
		if _, ok := s.store.FindByTgMsg(in.PostID, in.TgMsgID); ok {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "dup": true})
			return
		}
	}
	replyTo := int64(0)
	if in.ReplyToTgMsg != 0 {
		if cid, ok := s.store.FindByTgMsg(in.PostID, in.ReplyToTgMsg); ok {
			replyTo = cid
		}
	}
	s.store.Add(Comment{
		PostID:   in.PostID,
		Author:   in.Author,
		AuthorID: in.AuthorID,
		Source:   "tg",
		Text:     in.Text,
		ReplyTo:  replyTo,
		TgMsgID:  in.TgMsgID,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// postCommentToTG отправляет коммент из мини-аппа в группу обсуждения TG (app→TG).
// post_id = "<channelChat>_<channelMsg>" → ищем маппинг в bridge.db → шлём реплаем
// на авто-форвард (Telegram прицепит как комментарий к посту). Бот-сообщение бридж
// при ингесте пропускает (sender == bot), эхо нет.
// postCommentToTG шлёт коммент из мини-аппа в группу обсуждения TG. replyToTgMsg —
// id сообщения-родителя в TG (для reply на конкретный коммент); 0 — корневой, тогда
// отвечаем на авто-форвард поста (прицепится под пост). Сохраняет id отправленного
// сообщения на коммент, чтобы ответы из TG на него мапились обратно.
func (s *server) postCommentToTG(commentID int64, postID, author, text string, replyToTgMsg int) {
	if tgAPIURL == "" || tgBotToken == "" {
		return
	}
	parts := strings.SplitN(postID, "_", 2)
	if len(parts) != 2 {
		return
	}
	channelChat, err1 := strconv.ParseInt(parts[0], 10, 64)
	channelMsg, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return
	}

	discChat := tgLinkedChat(channelChat)
	if discChat == 0 {
		log.Printf("app→TG: у канала %d нет группы обсуждения", channelChat)
		return
	}

	// Цель reply: на конкретный коммент (если это ответ) либо на авто-форвард поста.
	replyTo := replyToTgMsg
	if replyTo == 0 && bridgeDBPath != "" {
		if db, err := sql.Open("sqlite", "file:"+bridgeDBPath+"?mode=ro&_pragma=busy_timeout(3000)"); err == nil {
			db.QueryRow(`SELECT disc_msg_id FROM discussion_map WHERE channel_chat_id=? AND channel_msg_id=?`,
				channelChat, channelMsg).Scan(&replyTo)
			db.Close()
		}
	}

	form := url.Values{
		"chat_id": {strconv.FormatInt(discChat, 10)},
		"text":    {fmt.Sprintf("%s: %s", author, text)},
	}
	if replyTo > 0 {
		form.Set("reply_to_message_id", strconv.Itoa(replyTo))
	}
	resp, err := http.PostForm(tgAPIURL+"/bot"+tgBotToken+"/sendMessage", form)
	if err != nil {
		log.Printf("postCommentToTG send failed post=%s: %v", postID, err)
		return
	}
	defer resp.Body.Close()
	// Запоминаем id отправленного сообщения — чтобы ответы из TG на него легли как reply.
	var out struct {
		OK     bool `json:"ok"`
		Result struct {
			MessageID int64 `json:"message_id"`
		} `json:"result"`
	}
	if json.NewDecoder(resp.Body).Decode(&out) == nil && out.OK && out.Result.MessageID != 0 {
		s.store.SetTgMsg(commentID, out.Result.MessageID)
	}
	log.Printf("comment app→TG post=%s disc=%d replyTo=%d sentMsg=%d", postID, discChat, replyTo, out.Result.MessageID)
}

// tgLinkedChat возвращает linked_chat_id канала (его группу обсуждения) или 0.
func tgLinkedChat(channelID int64) int64 {
	resp, err := http.Get(tgAPIURL + "/bot" + tgBotToken + "/getChat?chat_id=" + strconv.FormatInt(channelID, 10))
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	var out struct {
		OK     bool `json:"ok"`
		Result struct {
			LinkedChatID int64 `json:"linked_chat_id"`
		} `json:"result"`
	}
	if json.NewDecoder(resp.Body).Decode(&out) != nil || !out.OK {
		return 0
	}
	return out.Result.LinkedChatID
}
