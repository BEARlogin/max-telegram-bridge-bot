package main

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"

	"commenter/spamfilter"
)

// channelOfPostID — TG-канал из post_id "<channelChat>_<channelMsg>" (0 если не распарсилось).
func channelOfPostID(postID string) int64 {
	parts := strings.SplitN(postID, "_", 2)
	if len(parts) != 2 {
		return 0
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0
	}
	return id
}

// Антиспам из кабинета. Одна связка → один тумблер + настройки. Включение:
//   - пишем antispam_config (addon.db) для ГРУППЫ ОБСУЖДЕНИЯ канала (бридж её модерит);
//   - ставим флаг в comments.db по TG-каналу — им фильтруются мини-апп-комменты.
// Бридж читает antispam_config на каждое сообщение, поэтому рестарт не нужен.

// tgChannelOfCrosspost — TG-канал связки по max_chat_id.
func tgChannelOfCrosspost(maxChatID int64) (int64, bool) {
	if bridgeDBPath == "" {
		return 0, false
	}
	db, err := sql.Open("sqlite", "file:"+bridgeDBPath+"?mode=ro&_pragma=busy_timeout(3000)")
	if err != nil {
		return 0, false
	}
	defer db.Close()
	var tg int64
	err = db.QueryRow(`SELECT tg_chat_id FROM crossposts WHERE max_chat_id=? AND deleted_at=0`, maxChatID).Scan(&tg)
	return tg, err == nil && tg != 0
}

// writeAntispamConfig пишет antispam_config для чата (addon.db) на указанной платформе.
// asPolicy — политика наказания антиспама (значения уже санитайзнуты sanitizeAntispamPolicy).
type asPolicy struct {
	StrikeLimit int    // мут после N нарушений (1 = сразу)
	BanAfter    int    // для mute_then_ban: бан после M нарушений (M > StrikeLimit)
	Action      string // mute | ban | mute_then_ban
	MuteMinutes int    // длительность мута, минут
	Warn        bool   // предупреждать нарушителя в чате до наказания
	Notify      string // off | ban | all
	Captcha     bool   // капча на входе
	Antiraid    bool   // анти-рейд (авто-кик новичков при наплыве)
	BlockWords  string // запрещённые слова/фразы владельца (csv)
	BlockCats   string // включённые пресет-категории запрета (csv ключей)
	DelService  bool   // удалять служебные сообщения (вошёл/вышел/смена названия/закреп)
	Tone        string // strict | friendly — тон уведомлений в чате о наказании
}

func writeAntispamConfig(platform string, chatID, ownerID int64, on bool, mode string, linkDelayH, trustMsgs int, p asPolicy) error {
	if addonDBPath == "" || chatID == 0 {
		return nil
	}
	db, err := openRW(addonDBPath)
	if err != nil {
		return err
	}
	defer db.Close()
	v := 0
	if on {
		v = 1
	}
	warn := 0
	if p.Warn {
		warn = 1
	}
	captcha := 0
	if p.Captcha {
		captcha = 1
	}
	antiraid := 0
	if p.Antiraid {
		antiraid = 1
	}
	delService := 0
	if p.DelService {
		delService = 1
	}
	tone := p.Tone
	if tone != "friendly" {
		tone = "strict"
	}
	_, err = db.Exec(`INSERT INTO antispam_config (platform, chat_id, enabled, enabled_by, mode, link_delay_h, trust_msgs, strike_limit, ban_after, action, mute_minutes, warn, notify, captcha, antiraid, block_words, block_cats, del_service, tone, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, strftime('%s','now'))
		ON CONFLICT(platform, chat_id) DO UPDATE SET enabled=excluded.enabled, enabled_by=excluded.enabled_by,
			mode=excluded.mode, link_delay_h=excluded.link_delay_h, trust_msgs=excluded.trust_msgs,
			strike_limit=excluded.strike_limit, ban_after=excluded.ban_after, action=excluded.action,
			mute_minutes=excluded.mute_minutes, warn=excluded.warn, notify=excluded.notify, captcha=excluded.captcha, antiraid=excluded.antiraid,
			block_words=excluded.block_words, block_cats=excluded.block_cats, del_service=excluded.del_service, tone=excluded.tone, updated_at=excluded.updated_at`,
		platform, chatID, v, ownerID, mode, linkDelayH, trustMsgs, p.StrikeLimit, p.BanAfter, p.Action, p.MuteMinutes, warn, p.Notify, captcha, antiraid, p.BlockWords, p.BlockCats, delService, tone)
	return err
}

// writeDiscussionAntispam — antispam для группы обсуждения канала (TG-сторона).
func writeDiscussionAntispam(discGroupID, ownerID int64, on bool, mode string, linkDelayH, trustMsgs int, p asPolicy) error {
	return writeAntispamConfig("tg", discGroupID, ownerID, on, mode, linkDelayH, trustMsgs, p)
}

// sanitizeAntispamPolicy нормализует политику: strike_limit (1..5), action
// (mute|ban|mute_then_ban), mute_minutes (1..43200), notify (off|ban|all).
func sanitizeAntispamPolicy(p asPolicy) asPolicy {
	if p.StrikeLimit < 1 || p.StrikeLimit > 10 {
		p.StrikeLimit = 2
	}
	if p.Action != "ban" && p.Action != "mute_then_ban" {
		p.Action = "mute"
	}
	if p.BanAfter < 1 || p.BanAfter > 20 {
		p.BanAfter = 3
	}
	// Для эскалации бан должен наступать строго позже мута.
	if p.Action == "mute_then_ban" && p.BanAfter <= p.StrikeLimit {
		p.BanAfter = p.StrikeLimit + 1
	}
	if p.MuteMinutes < 1 || p.MuteMinutes > 43200 {
		p.MuteMinutes = 60
	}
	if p.Notify != "off" && p.Notify != "all" {
		p.Notify = "ban"
	}
	return p
}

// antispamStatus читает текущее состояние антиспама чата из addon.db.
func antispamStatus(platform string, chatID int64) (on bool, mode string) {
	mode = "enforce"
	if addonDBPath == "" {
		return
	}
	db, err := sql.Open("sqlite", "file:"+addonDBPath+"?mode=ro&_pragma=busy_timeout(3000)")
	if err != nil {
		return
	}
	defer db.Close()
	var en int
	var m string
	if db.QueryRow(`SELECT enabled, mode FROM antispam_config WHERE platform=? AND chat_id=?`, platform, chatID).Scan(&en, &m) == nil {
		on = en != 0
		if m != "" {
			mode = m
		}
	}
	return
}

// antispamPolicy читает политику наказания чата из addon.db (для префилла кабинета).
func antispamPolicy(platform string, chatID int64) asPolicy {
	p := asPolicy{StrikeLimit: 2, BanAfter: 3, Action: "mute", MuteMinutes: 60, Notify: "ban", Tone: "strict"}
	if addonDBPath == "" || chatID == 0 {
		return p
	}
	db, err := sql.Open("sqlite", "file:"+addonDBPath+"?mode=ro&_pragma=busy_timeout(3000)")
	if err != nil {
		return p
	}
	defer db.Close()
	var sl, ba, mm, wn, cap, ar, ds int
	var act, ntf, tn string
	if db.QueryRow(`SELECT COALESCE(strike_limit,2), COALESCE(ban_after,3), COALESCE(action,'mute'), COALESCE(mute_minutes,60), COALESCE(warn,0), COALESCE(notify,'ban'), COALESCE(captcha,0), COALESCE(antiraid,0), COALESCE(block_words,''), COALESCE(block_cats,''), COALESCE(del_service,0), COALESCE(tone,'strict')
		FROM antispam_config WHERE platform=? AND chat_id=?`, platform, chatID).Scan(&sl, &ba, &act, &mm, &wn, &ntf, &cap, &ar, &p.BlockWords, &p.BlockCats, &ds, &tn) == nil {
		if tn == "friendly" || tn == "strict" {
			p.Tone = tn
		}
		if sl >= 1 {
			p.StrikeLimit = sl
		}
		if ba >= 1 {
			p.BanAfter = ba
		}
		if act == "ban" || act == "mute" || act == "mute_then_ban" {
			p.Action = act
		}
		if mm >= 1 {
			p.MuteMinutes = mm
		}
		p.Warn = wn != 0
		if ntf == "off" || ntf == "ban" || ntf == "all" {
			p.Notify = ntf
		}
		p.Captcha = cap != 0
		p.Antiraid = ar != 0
		p.DelService = ds != 0
	}
	return p
}

// asRuleInfo — кастомное правило антиспама для кабинета.
type asRuleInfo struct {
	Rid      int    `json:"rid"`
	Descr    string `json:"descr"`
	Keywords string `json:"keywords"`
	Action   string `json:"action"`
	Warns    int    `json:"warns"`
}

// readAntispamRules читает кастомные правила чата из addon.db.
func readAntispamRules(platform string, chatID int64) []asRuleInfo {
	if addonDBPath == "" || chatID == 0 {
		return nil
	}
	db, err := sql.Open("sqlite", "file:"+addonDBPath+"?mode=ro&_pragma=busy_timeout(3000)")
	if err != nil {
		return nil
	}
	defer db.Close()
	rows, err := db.Query(`SELECT rid, descr, keywords, action, warns FROM antispam_rules WHERE platform=? AND chat_id=? ORDER BY rid`, platform, chatID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []asRuleInfo
	for rows.Next() {
		var r asRuleInfo
		if rows.Scan(&r.Rid, &r.Descr, &r.Keywords, &r.Action, &r.Warns) == nil {
			out = append(out, r)
		}
	}
	return out
}

// addAntispamRule добавляет правило (rid=max+1) в addon.db.
func addAntispamRule(platform string, chatID int64, descr, keywords, action string, warns int) error {
	if addonDBPath == "" || chatID == 0 {
		return nil
	}
	if action != "mute" && action != "ban" && action != "delete" {
		action = "mute"
	}
	if warns < 0 {
		warns = 0
	}
	db, err := openRW(addonDBPath)
	if err != nil {
		return err
	}
	defer db.Close()
	var maxRid int
	_ = db.QueryRow(`SELECT COALESCE(MAX(rid),0) FROM antispam_rules WHERE platform=? AND chat_id=?`, platform, chatID).Scan(&maxRid)
	_, err = db.Exec(`INSERT INTO antispam_rules (platform, chat_id, rid, descr, keywords, action, mute_min, warns, created_at)
		VALUES (?, ?, ?, ?, ?, ?, 60, ?, strftime('%s','now'))`, platform, chatID, maxRid+1, descr, keywords, action, warns)
	return err
}

func delAntispamRule(platform string, chatID int64, rid int) error {
	if addonDBPath == "" {
		return nil
	}
	db, err := openRW(addonDBPath)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(`DELETE FROM antispam_rules WHERE platform=? AND chat_id=? AND rid=?`, platform, chatID, rid)
	return err
}

// handleAddGroupRule — добавить кастомное правило антиспама группы (обе стороны связки).
func (s *server) handleAddGroupRule(w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	if !u.Valid {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	var in struct {
		TgChatID int64  `json:"tg_chat_id"`
		Descr    string `json:"descr"`
		Keywords string `json:"keywords"`
		Action   string `json:"action"`
		Warns    int    `json:"warns"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.TgChatID == 0 || strings.TrimSpace(in.Descr) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "descr required"})
		return
	}
	if !ownsGroup(u.ID, in.TgChatID) || !s.userIsPro(u.ID) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "нет доступа"})
		return
	}
	_ = addAntispamRule("tg", in.TgChatID, strings.TrimSpace(in.Descr), strings.TrimSpace(in.Keywords), in.Action, in.Warns)
	if maxID := groupMaxChat(in.TgChatID); maxID != 0 {
		_ = addAntispamRule("max", maxID, strings.TrimSpace(in.Descr), strings.TrimSpace(in.Keywords), in.Action, in.Warns)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "rules": readAntispamRules("tg", in.TgChatID)})
}

// handleDelGroupRule — удалить правило (обе стороны связки).
func (s *server) handleDelGroupRule(w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	if !u.Valid {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	var in struct {
		TgChatID int64 `json:"tg_chat_id"`
		Rid      int   `json:"rid"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.TgChatID == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "tg_chat_id required"})
		return
	}
	if !ownsGroup(u.ID, in.TgChatID) || !s.userIsPro(u.ID) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "нет доступа"})
		return
	}
	_ = delAntispamRule("tg", in.TgChatID, in.Rid)
	if maxID := groupMaxChat(in.TgChatID); maxID != 0 {
		_ = delAntispamRule("max", maxID, in.Rid)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "rules": readAntispamRules("tg", in.TgChatID)})
}

// handleSetGroupAntispam — антиспам bridge-группы (обе стороны связки), PRO + админ.
func (s *server) handleSetGroupAntispam(w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	if !u.Valid {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	var in struct {
		TgChatID    int64  `json:"tg_chat_id"`
		Enabled     bool   `json:"enabled"`
		Mode        string `json:"mode"`
		LinkDelayH  int    `json:"link_delay_h"`
		TrustMsgs   int    `json:"trust_msgs"`
		StrikeLimit int    `json:"strike_limit"`
		BanAfter    int    `json:"ban_after"`
		Action      string `json:"action"`
		MuteMinutes int    `json:"mute_minutes"`
		Warn        bool   `json:"warn"`
		Notify      string `json:"notify"`
		Captcha     bool   `json:"captcha"`
		Antiraid    bool   `json:"antiraid"`
		BlockWords  string `json:"block_words"`
		BlockCats   string `json:"block_cats"`
		DelService  bool   `json:"del_service"`
		Tone        string `json:"tone"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.TgChatID == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "tg_chat_id required"})
		return
	}
	if !ownsGroup(u.ID, in.TgChatID) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "вы не админ этой группы"})
		return
	}
	if !s.userIsPro(u.ID) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "антиспам — PRO-функция"})
		return
	}
	if in.Mode != "observe" && in.Mode != "debug" {
		in.Mode = "enforce"
	}
	if in.LinkDelayH < 0 || in.LinkDelayH > 720 {
		in.LinkDelayH = 24
	}
	if in.TrustMsgs < 0 || in.TrustMsgs > 100 {
		in.TrustMsgs = 3
	}
	pol := sanitizeAntispamPolicy(asPolicy{StrikeLimit: in.StrikeLimit, BanAfter: in.BanAfter, Action: in.Action, MuteMinutes: in.MuteMinutes, Warn: in.Warn, Notify: in.Notify, Captcha: in.Captcha, Antiraid: in.Antiraid, BlockWords: in.BlockWords, BlockCats: in.BlockCats, DelService: in.DelService, Tone: in.Tone})
	// TG-сторона + MAX-сторона связки (бридж модерит обе).
	_ = writeAntispamConfig("tg", in.TgChatID, u.ID, in.Enabled, in.Mode, in.LinkDelayH, in.TrustMsgs, pol)
	if maxID := groupMaxChat(in.TgChatID); maxID != 0 {
		_ = writeAntispamConfig("max", maxID, u.ID, in.Enabled, in.Mode, in.LinkDelayH, in.TrustMsgs, pol)
	}
	botAdmin := tgBotIsAdmin(in.TgChatID)
	log.Printf("group antispam uid=%d tg=%d on=%v mode=%s botAdmin=%v", u.ID, in.TgChatID, in.Enabled, in.Mode, botAdmin)
	writeJSON(w, http.StatusOK, map[string]any{"ok": in.Enabled, "bot_admin": botAdmin})
}

// groupMaxChat — MAX-сторона пары по tg_chat_id.
func groupMaxChat(tgChatID int64) int64 {
	if bridgeDBPath == "" {
		return 0
	}
	db, err := sql.Open("sqlite", "file:"+bridgeDBPath+"?mode=ro&_pragma=busy_timeout(3000)")
	if err != nil {
		return 0
	}
	defer db.Close()
	var max int64
	db.QueryRow(`SELECT max_chat_id FROM pairs WHERE tg_chat_id=?`, tgChatID).Scan(&max)
	return max
}

// handleSetAntispam — вкл/выкл + настройки антиспама связки (владелец, PRO).
func (s *server) handleSetAntispam(w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	if !u.Valid {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	var in struct {
		MaxChatID   int64  `json:"max_chat_id"`
		Enabled     bool   `json:"enabled"`
		Mode        string `json:"mode"`         // enforce | observe | debug
		LinkDelayH  int    `json:"link_delay_h"` // часов «новичок не постит ссылки»
		TrustMsgs   int    `json:"trust_msgs"`   // сообщений до «доверенного»
		StrikeLimit int    `json:"strike_limit"` // мут после N нарушений (1 = сразу)
		BanAfter    int    `json:"ban_after"`    // mute_then_ban: бан после M нарушений
		Action      string `json:"action"`       // mute | ban | mute_then_ban
		MuteMinutes int    `json:"mute_minutes"` // длительность мута, минут
		Warn        bool   `json:"warn"`         // предупреждать нарушителя в чате
		Notify      string `json:"notify"`       // off | ban | all
		Captcha     bool   `json:"captcha"`      // капча на входе + анти-рейд
		Antiraid    bool   `json:"antiraid"`     // анти-рейд
		BlockWords  string `json:"block_words"`
		BlockCats   string `json:"block_cats"`
		DelService  bool   `json:"del_service"`
		Tone        string `json:"tone"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.MaxChatID == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "max_chat_id required"})
		return
	}
	if !ownsCrosspost(u.ID, in.MaxChatID) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "не ваша связка"})
		return
	}
	if !s.userIsPro(u.ID) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "антиспам — PRO-функция"})
		return
	}
	// Санитайз настроек.
	if in.Mode != "observe" && in.Mode != "debug" {
		in.Mode = "enforce"
	}
	if in.LinkDelayH < 0 || in.LinkDelayH > 720 {
		in.LinkDelayH = 24
	}
	if in.TrustMsgs < 0 || in.TrustMsgs > 100 {
		in.TrustMsgs = 3
	}
	pol := sanitizeAntispamPolicy(asPolicy{StrikeLimit: in.StrikeLimit, BanAfter: in.BanAfter, Action: in.Action, MuteMinutes: in.MuteMinutes, Warn: in.Warn, Notify: in.Notify, Captcha: in.Captcha, Antiraid: in.Antiraid, BlockWords: in.BlockWords, BlockCats: in.BlockCats, DelService: in.DelService, Tone: in.Tone})

	tgChan, ok := tgChannelOfCrosspost(in.MaxChatID)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "канал связки не найден"})
		return
	}

	// Группа обсуждения канала (если привязана) — её модерит бридж.
	discGroup := tgLinkedChat(tgChan)
	if discGroup != 0 {
		if err := writeDiscussionAntispam(discGroup, u.ID, in.Enabled, in.Mode, in.LinkDelayH, in.TrustMsgs, pol); err != nil {
			log.Printf("antispam discussion write err uid=%d disc=%d: %v", u.ID, discGroup, err)
		}
	}
	// Флаг+режим для мини-апп-комментов (по TG-каналу).
	s.store.SetAntispam(tgChan, in.Enabled, in.Mode)

	botAdmin := discGroup != 0 && tgBotIsAdmin(discGroup)
	log.Printf("antispam uid=%d max=%d tg=%d disc=%d on=%v mode=%s botAdmin=%v", u.ID, in.MaxChatID, tgChan, discGroup, in.Enabled, in.Mode, botAdmin)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                in.Enabled,
		"discussion_linked": discGroup != 0,
		"discussion_id":     discGroup,
		"bot_admin":         botAdmin,
	})
}

// handleBotAdminCheck — перепроверка прав бота (после добавления его в админы).
// kind: "group" (id=tg_chat_id) | иначе crosspost (id=max_chat_id → группа обсуждения).
func (s *server) handleBotAdminCheck(w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	if !u.Valid {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	var in struct {
		Kind string `json:"kind"`
		ID   int64  `json:"id"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.ID == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad request"})
		return
	}
	var tgGroup int64
	linked := false
	if in.Kind == "group" {
		if !ownsGroup(u.ID, in.ID) {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "не ваша группа"})
			return
		}
		tgGroup, linked = in.ID, true
	} else {
		if !ownsCrosspost(u.ID, in.ID) {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "не ваша связка"})
			return
		}
		if tgChan, ok := tgChannelOfCrosspost(in.ID); ok {
			tgGroup = tgLinkedChat(tgChan)
		}
		linked = tgGroup != 0
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"bot_admin":         tgGroup != 0 && tgBotIsAdmin(tgGroup),
		"discussion_linked": linked,
	})
}

// commentLooksSpam — быстрый regex-детектор для мини-апп-коммента (без LLM, чтобы не тормозить).
// Analyze сам находит ссылки/шортенеры/упоминания по внутренним регуляркам.
func commentLooksSpam(text string) (bool, string) {
	res := spamfilter.Analyze(text, spamfilter.Config{})
	if res.Spam {
		return true, spamfilter.DescribeSignals(res)
	}
	return false, ""
}
