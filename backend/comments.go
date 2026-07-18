package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"commenter/billing"
)

type server struct {
	store   store
	billing *billing.Service // боевой биллинг (подписки). nil, если T-Bank не настроен
	// certBilling — отдельный клиент на DEMO-терминале для прохождения сертификации
	// (чеки/возвраты), чтобы не трогать боевой терминал. nil, если не настроен.
	certBilling *billing.Service
}

func (s *server) handleComments(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		postID := r.URL.Query().Get("post_id")
		if postID == "" {
			http.Error(w, "post_id required", http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"comments": s.store.List(postID)})

	case http.MethodPost:
		var in struct {
			PostID  string `json:"post_id"`
			Text    string `json:"text"`
			ReplyTo int64  `json:"reply_to"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		in.PostID = strings.TrimSpace(in.PostID)
		in.Text = strings.TrimSpace(in.Text)
		if in.PostID == "" || in.Text == "" {
			http.Error(w, "post_id and text required", http.StatusBadRequest)
			return
		}
		// Антиспам мини-апп-комментов (если включён для канала связки): быстрый regex.
		// Только enforce отклоняет комментарий; observe/debug лишь фиксируют вердикт.
		if chID := channelOfPostID(in.PostID); chID != 0 {
			if on, mode := s.store.GetAntispam(chID); on {
				if spam, why := commentLooksSpam(in.Text); spam {
					if mode != "enforce" {
						log.Printf("comment [%s] WOULD reject post=%s: %s", mode, in.PostID, why)
					} else {
						log.Printf("comment rejected (spam) post=%s: %s", in.PostID, why)
						writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Комментарий похож на спам и не опубликован."})
						return
					}
				}
			}
		}
		// Личность комментатора — из подписи мини-аппа (см. auth.go).
		u := authUser(r)
		c := s.store.Add(Comment{
			PostID:   in.PostID,
			Author:   u.Name,
			AuthorID: u.ID,
			Source:   "max",
			Text:     in.Text,
			ReplyTo:  in.ReplyTo,
		})
		// Синк в группу обсуждения TG (app→TG), асинхронно. Только корневые комменты
		// (не реплаи внутри мини-аппа), чтобы не плодить шум в TG.
		// Ответ — находим tg_msg_id родителя, чтобы в TG прицепить как reply на него.
		replyParentTg := 0
		if in.ReplyTo != 0 {
			for _, x := range s.store.List(in.PostID) {
				if x.ID == in.ReplyTo {
					replyParentTg = int(x.TgMsgID)
					break
				}
			}
		}
		log.Printf("comment add post=%s author=%s reply_to=%d parentTg=%d", in.PostID, u.Name, in.ReplyTo, replyParentTg)
		go s.postCommentToTG(c.ID, in.PostID, u.Name, in.Text, replyParentTg)
		writeJSON(w, http.StatusOK, c)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleWhoami — кто открыл мини-апп (по подписи initData). Для проверки авторизации
// и показа «вы вошли как…» в настройках.
func (s *server) handleWhoami(w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	writeJSON(w, http.StatusOK, map[string]any{"id": u.ID, "name": u.Name, "valid": u.Valid})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
