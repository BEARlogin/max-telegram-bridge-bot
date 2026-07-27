package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

func bridgeInternalBase() string {
	base := strings.TrimRight(os.Getenv("BRIDGE_INTERNAL_URL"), "/")
	if base == "" {
		base = "http://127.0.0.1:8443"
	}
	return base
}

func (s *server) vkCabinetIdentity(u user) (ownerID int64, platform string, chatID int64) {
	platform, chatID = u.Platform, u.ID
	if tgID := s.effectiveTgID(u); tgID > 0 {
		platform, chatID = "tg", tgID
	}
	ownerID = chatID
	if s.billing != nil {
		if billingID := s.billing.BillingID(chatID); billingID > 0 {
			ownerID = billingID
		}
	}
	return ownerID, platform, chatID
}

func notifyVKReload() {
	secret := commentSyncSecret()
	if secret == "" {
		return
	}
	base := bridgeInternalBase()
	payload, _ := json.Marshal(map[string]string{"secret": secret})
	req, err := http.NewRequest(http.MethodPost, base+"/internal/vk-reload", bytes.NewReader(payload))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 2 * time.Second}
	if resp, err := client.Do(req); err == nil {
		resp.Body.Close()
	}
}

func (s *server) handleVKConnect(w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	if !u.Valid {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	ownerID, platform, chatID := s.vkCabinetIdentity(u)
	secret := commentSyncSecret()
	if secret == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "Подключение VK временно недоступно"})
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"secret": secret, "owner_id": ownerID, "platform": platform, "chat_id": chatID,
	})
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
		bridgeInternalBase()+"/internal/vk-connect", bytes.NewReader(payload))
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "Не удалось начать подключение VK"})
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "Не удалось связаться с мостом"})
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	if resp.StatusCode != http.StatusOK {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "Не удалось начать подключение VK"})
		return
	}
	var out struct {
		URL string `json:"url"`
	}
	if json.Unmarshal(body, &out) != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "Мост вернул некорректный ответ"})
		return
	}
	parsed, err := url.Parse(out.URL)
	if err != nil || parsed.Scheme != "https" ||
		(parsed.Hostname() != "vk.com" && parsed.Hostname() != "vk.ru") {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "Мост вернул небезопасную ссылку"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"url": out.URL})
}

type vkSourceCandidate struct {
	Platform string `json:"platform"`
	ChatID   int64  `json:"chat_id"`
	Title    string `json:"title"`
	ActorID  int64  `json:"-"`
}

func (s *server) vkSourceCandidates(u user, groups []groupInfo) []vkSourceCandidate {
	tgActor := s.effectiveTgID(u)
	if tgActor == 0 && u.Platform == "tg" {
		tgActor = u.ID
	}
	maxActor := int64(0)
	if u.Platform == "max" {
		maxActor = u.ID
	} else if tgActor != 0 {
		maxActor = s.store.LinkedMax(tgActor)
	}
	out := make([]vkSourceCandidate, 0, len(groups)*2)
	seen := map[string]bool{}
	add := func(platform string, chatID int64, title string, actorID int64) {
		if chatID == 0 || actorID == 0 {
			return
		}
		key := platform + ":" + strconv.FormatInt(chatID, 10)
		if seen[key] {
			return
		}
		seen[key] = true
		if strings.TrimSpace(title) == "" {
			title = strings.ToUpper(platform) + " " + strconv.FormatInt(chatID, 10)
		}
		out = append(out, vkSourceCandidate{
			Platform: platform, ChatID: chatID, Title: title, ActorID: actorID,
		})
	}
	for _, group := range groups {
		add("tg", group.TgChatID, group.TgTitle, tgActor)
		add("max", group.MaxChatID, group.MaxTitle, maxActor)
	}
	return out
}

func (s *server) vkSourceCandidate(u user, platform string, chatID int64) (vkSourceCandidate, bool) {
	tgID := s.effectiveTgID(u)
	if tgID == 0 && u.Platform == "tg" {
		tgID = u.ID
	}
	for _, candidate := range s.vkSourceCandidates(u, userGroups(tgID, tgID)) {
		if candidate.Platform == platform && candidate.ChatID == chatID {
			return candidate, true
		}
	}
	return vkSourceCandidate{}, false
}

func callBridgeVK(ctx context.Context, path string, payload any, timeout time.Duration) ([]byte, int, error) {
	secret := commentSyncSecret()
	if secret == "" {
		return nil, 0, errors.New("internal secret unavailable")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		bridgeInternalBase()+path, bytes.NewReader(raw))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	return body, resp.StatusCode, err
}

func (s *server) handleVKChats(w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	if !u.Valid {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	ownerID, _, _ := s.vkCabinetIdentity(u)
	body, status, err := callBridgeVK(r.Context(), "/internal/vk-chats",
		map[string]any{"secret": commentSyncSecret(), "owner_id": ownerID}, 20*time.Second)
	if err != nil || status != http.StatusOK {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "Не удалось получить беседы VK"})
		return
	}
	var out struct {
		Chats             []map[string]any `json:"chats"`
		FailedCommunities []int64          `json:"failed_communities"`
	}
	if json.Unmarshal(body, &out) != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "Мост вернул некорректный ответ"})
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) handleVKChatBind(w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	if !u.Valid {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var in struct {
		Platform     string `json:"platform"`
		SourceChatID int64  `json:"source_chat_id"`
		AccountID    int64  `json:"account_id"`
		PeerID       int64  `json:"peer_id"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.SourceChatID == 0 || in.AccountID <= 0 ||
		in.PeerID < 2000000000 || (in.Platform != "tg" && in.Platform != "max") {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Некорректные параметры связки"})
		return
	}
	source, ok := s.vkSourceCandidate(u, in.Platform, in.SourceChatID)
	if !ok {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "Выбранная группа вам недоступна"})
		return
	}
	body, status, err := callBridgeVK(r.Context(), "/internal/vk-chat-bind", map[string]any{
		"secret": commentSyncSecret(), "actor_id": source.ActorID,
		"platform": in.Platform, "source_chat_id": in.SourceChatID,
		"account_id": in.AccountID, "peer_id": in.PeerID,
	}, 25*time.Second)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "Не удалось связаться с мостом"})
		return
	}
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &out) != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "Мост вернул некорректный ответ"})
		return
	}
	if status != http.StatusOK || !out.OK {
		if strings.TrimSpace(out.Error) == "" {
			out.Error = "Не удалось создать связку"
		}
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": out.Error})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type vkCabinetBinding struct {
	ID             int64  `json:"id"`
	SourcePlatform string `json:"source_platform"`
	SourceChatID   int64  `json:"source_chat_id"`
	SourceTitle    string `json:"source_title"`
	CommunityID    int64  `json:"community_id"`
	Kind           string `json:"kind"`
	Title          string `json:"title"`
	Direction      string `json:"direction"`
	Paused         bool   `json:"paused"`
}

// cabinetOwnerIDs повторяет канонизацию владельца из VK-addon: учитываем ID,
// связанный аккаунт второй платформы и billing ID. Фиксированный массив упрощает
// безопасные параметризованные WHERE без сборки SQL из пользовательских данных.
func (s *server) cabinetOwnerIDs(u user) [4]int64 {
	ids := [4]int64{u.ID, s.effectiveTgID(u)}
	if ids[1] != 0 {
		ids[2] = s.store.LinkedMax(ids[1])
	}
	if s.billing != nil {
		ids[3] = s.billing.BillingID(u.ID)
	}
	return ids
}

func (s *server) vkCabinetInfo(u user) (bindings []vkCabinetBinding, communities []int64) {
	if addonDBPath == "" {
		return nil, nil
	}
	db, err := sql.Open("sqlite", "file:"+addonDBPath+"?mode=ro&_pragma=busy_timeout(3000)")
	if err != nil {
		return nil, nil
	}
	defer db.Close()
	ids := s.cabinetOwnerIDs(u)
	rows, err := db.Query(`SELECT b.id,b.source_platform,b.source_chat_id,a.community_id,
		e.kind,e.title,b.direction,b.paused
		FROM vk_bindings b
		JOIN vk_endpoints e ON e.id=b.endpoint_id
		JOIN vk_accounts a ON a.id=e.account_id
		WHERE b.owner_id IN (?,?,?,?)
		ORDER BY b.id`, ids[0], ids[1], ids[2], ids[3])
	if err == nil {
		for rows.Next() {
			var item vkCabinetBinding
			if rows.Scan(&item.ID, &item.SourcePlatform, &item.SourceChatID,
				&item.CommunityID, &item.Kind, &item.Title,
				&item.Direction, &item.Paused) == nil {
				bindings = append(bindings, item)
			}
		}
		rows.Close()
	}
	rows, err = db.Query(`SELECT DISTINCT community_id FROM vk_accounts
		WHERE owner_id IN (?,?,?,?) AND enabled=1 ORDER BY community_id`,
		ids[0], ids[1], ids[2], ids[3])
	if err == nil {
		for rows.Next() {
			var id int64
			if rows.Scan(&id) == nil {
				communities = append(communities, id)
			}
		}
		rows.Close()
	}
	for i := range bindings {
		if bindings[i].SourcePlatform == "tg" {
			bindings[i].SourceTitle = chatTitle(bindings[i].SourceChatID)
		}
	}
	return bindings, communities
}

func (s *server) vkBindingOwned(db *sql.DB, u user, id int64) bool {
	ids := s.cabinetOwnerIDs(u)
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM vk_bindings
		WHERE id=? AND owner_id IN (?,?,?,?)`, id, ids[0], ids[1], ids[2], ids[3]).Scan(&n)
	return n == 1
}

func (s *server) handleVKDirection(w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	if !u.Valid {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	var in struct {
		ID        int64  `json:"id"`
		Direction string `json:"direction"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil ||
		(in.Direction != "both" && in.Direction != "source>vk" && in.Direction != "vk>source") {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid direction"})
		return
	}
	db, err := openRW(addonDBPath)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "vk unavailable"})
		return
	}
	defer db.Close()
	if !s.vkBindingOwned(db, u, in.ID) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}
	_, err = db.Exec(`UPDATE vk_bindings SET direction=?,updated_at=? WHERE id=?`,
		in.Direction, time.Now().Unix(), in.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "save failed"})
		return
	}
	notifyVKReload()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "direction": in.Direction})
}

func (s *server) handleVKPause(w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	if !u.Valid {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	var in struct {
		ID     int64 `json:"id"`
		Paused bool  `json:"paused"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.ID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid binding"})
		return
	}
	db, err := openRW(addonDBPath)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "vk unavailable"})
		return
	}
	defer db.Close()
	if !s.vkBindingOwned(db, u, in.ID) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}
	paused := 0
	if in.Paused {
		paused = 1
	}
	_, err = db.Exec(`UPDATE vk_bindings SET paused=?,updated_at=? WHERE id=?`, paused, time.Now().Unix(), in.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "save failed"})
		return
	}
	notifyVKReload()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "paused": in.Paused})
}

func (s *server) handleVKDelete(w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	if !u.Valid {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	var in struct {
		ID int64 `json:"id"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.ID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid binding"})
		return
	}
	db, err := openRW(addonDBPath)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "vk unavailable"})
		return
	}
	defer db.Close()
	if !s.vkBindingOwned(db, u, in.ID) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}
	if _, err = db.Exec(`DELETE FROM vk_bindings WHERE id=?`, in.ID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "delete failed"})
		return
	}
	notifyVKReload()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
