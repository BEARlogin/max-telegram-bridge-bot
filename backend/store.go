package main

import (
	"sort"
	"sync"
	"time"
)

// Comment — один комментарий к посту MAX-канала.
type Comment struct {
	ID        int64  `json:"id"`
	PostID    string `json:"post_id"`   // ключ поста: "<maxChatID>:<maxMid>" (или общий cross-id)
	Author    string `json:"author"`    // отображаемое имя
	AuthorID  int64  `json:"author_id"` // id юзера (из подписи мини-аппа)
	Source    string `json:"source"`    // "max" | "tg" (для кросс-комментов)
	Text      string `json:"text"`
	ReplyTo   int64  `json:"reply_to"` // id комментария-родителя (0 = корневой)
	TgMsgID   int64  `json:"-"`        // id сообщения в группе обсуждения TG (для синка reply)
	CreatedAt int64  `json:"created_at"`
}

// store — абстракция хранилища (MVP: in-memory; далее SQLite/Postgres).
type store interface {
	List(postID string) []Comment
	Add(c Comment) Comment
	// FindByTgMsg — id коммента по id его сообщения в группе обсуждения TG (для reply).
	FindByTgMsg(postID string, tgMsgID int64) (int64, bool)
	// SetTgMsg — проставить tg_msg_id комменту (после отправки app→TG).
	SetTgMsg(commentID, tgMsgID int64)
	// SetAntispam / GetAntispam — флаг+режим антиспама мини-апп-комментов по TG-каналу связки.
	SetAntispam(tgChatID int64, on bool, mode string)
	GetAntispam(tgChatID int64) (on bool, mode string)
	// Связка MAX↔TG по одноразовому коду.
	LinkNewCode(maxID int64, code string)
	LinkRedeem(code string, tgID, ttlSec int64) (maxID int64, ok bool)
	LinkedTg(maxID int64) int64
	LinkedMax(tgID int64) int64
}

type asState struct {
	on   bool
	mode string
}

type memStore struct {
	mu       sync.Mutex
	seq      int64
	data     map[string][]Comment
	antispam map[int64]asState
}

func newMemStore() *memStore {
	return &memStore{data: map[string][]Comment{}, antispam: map[int64]asState{}}
}

func (m *memStore) SetAntispam(tgChatID int64, on bool, mode string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.antispam[tgChatID] = asState{on: on, mode: mode}
}

func (m *memStore) GetAntispam(tgChatID int64) (bool, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.antispam[tgChatID]
	return s.on, s.mode
}

// memStore-связки (не персистентны — фолбэк-режим).
func (m *memStore) LinkNewCode(maxID int64, code string)                     {}
func (m *memStore) LinkRedeem(code string, tgID, ttlSec int64) (int64, bool) { return 0, false }
func (m *memStore) LinkedTg(maxID int64) int64                               { return 0 }
func (m *memStore) LinkedMax(tgID int64) int64                               { return 0 }

func (m *memStore) List(postID string) []Comment {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := append([]Comment(nil), m.data[postID]...)
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out
}

func (m *memStore) Add(c Comment) Comment {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	c.ID = m.seq
	if c.CreatedAt == 0 {
		c.CreatedAt = time.Now().Unix()
	}
	m.data[c.PostID] = append(m.data[c.PostID], c)
	return c
}

func (m *memStore) FindByTgMsg(postID string, tgMsgID int64) (int64, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.data[postID] {
		if c.TgMsgID == tgMsgID && tgMsgID != 0 {
			return c.ID, true
		}
	}
	return 0, false
}

func (m *memStore) SetTgMsg(commentID, tgMsgID int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for pid := range m.data {
		for i := range m.data[pid] {
			if m.data[pid][i].ID == commentID {
				m.data[pid][i].TgMsgID = tgMsgID
				return
			}
		}
	}
}
