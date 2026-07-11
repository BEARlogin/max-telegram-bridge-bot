package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
)

// Настройки читаем прямо из БД бриджа (read-only, тот же сервер) + PRO через TG API.
var (
	bridgeDBPath = os.Getenv("BRIDGE_DB")    // /opt/bearlogin-bridge/bridge.db
	addonDBPath  = os.Getenv("ADDON_DB")     // /opt/bearlogin-bridge/addon.db
	proGroupID   = os.Getenv("PRO_GROUP_ID") // TG-группа PRO-подписчиков
	tgAPIURL     = os.Getenv("TG_API_URL")   // локальный Telegram Bot API
	tgBotToken   = os.Getenv("TG_BOT_TOKEN")
)

// replRule / replSet — формат замен в bridge.db (поля совпадают с bridge.Replacement).
type replRule struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Regex  bool   `json:"regex"`
	Target string `json:"target,omitempty"` // "" / "all" — весь текст; "links" — только ссылки
}
type replSet struct {
	TgToMax []replRule `json:"tg>max"`
	MaxToTg []replRule `json:"max>tg"`
}

type crosspost struct {
	TgChatID        int64   `json:"tg_chat_id"`
	MaxChatID       int64   `json:"max_chat_id"`
	Direction       string  `json:"direction"`
	Title           string  `json:"title"`
	HasReplacements bool    `json:"has_replacements"`
	SyncEdits       bool    `json:"sync_edits"`
	Paused          bool    `json:"paused"`
	CommentsEnabled bool    `json:"comments_enabled"`
	Antispam        bool    `json:"antispam"`
	AntispamMode    string  `json:"antispam_mode"`
	StrikeLimit     int     `json:"strike_limit"`
	BanAfter        int     `json:"ban_after"`
	Action          string  `json:"action"`
	MuteMinutes     int     `json:"mute_minutes"`
	Warn            bool    `json:"warn"`
	Notify          string  `json:"notify"`
	Captcha         bool    `json:"captcha"`
	AntiraidWords   string  `json:"-"`
	Antiraid        bool    `json:"antiraid"`
	BlockWords      string  `json:"block_words"`
	BlockCats       string  `json:"block_cats"`
	DelService      bool    `json:"del_service"`
	BotAdmin        bool    `json:"bot_admin"` // бот админ в группе обсуждения (для модерации)
	Replacements    replSet `json:"replacements"`
}

// titleCache — кэш названий каналов (getChat дорогой, каналы повторяются).
var (
	titleMu    sync.Mutex
	titleCache = map[int64]string{}
)

func chatTitle(id int64) string {
	titleMu.Lock()
	if t, ok := titleCache[id]; ok {
		titleMu.Unlock()
		return t
	}
	titleMu.Unlock()
	if tgAPIURL == "" || tgBotToken == "" {
		return ""
	}
	resp, err := http.Get(tgAPIURL + "/bot" + tgBotToken + "/getChat?chat_id=" + strconv.FormatInt(id, 10))
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var out struct {
		OK     bool `json:"ok"`
		Result struct {
			Title string `json:"title"`
		} `json:"result"`
	}
	title := ""
	if json.NewDecoder(resp.Body).Decode(&out) == nil && out.OK {
		title = out.Result.Title
	}
	titleMu.Lock()
	titleCache[id] = title
	titleMu.Unlock()
	return title
}

// handleSettings возвращает данные кабинета по подписи мини-аппа: кросспосты,
// баланс импорта, статус PRO. Ключ — id юзера (TG из tg_owner_id, MAX из owner_id).
func (s *server) handleSettings(w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	if !u.Valid {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	// Связка MAX↔TG: баланс постов/связки/PRO частично по TG-id. Для MAX-юзера резолвим
	// привязанный TG-id; tgKey — ключ для TG-данных (свой id, если уже TG).
	effTg := s.effectiveTgID(u)
	tgKey := effTg
	if tgKey == 0 {
		tgKey = u.ID
	}
	// PRO = активная подписка T-Bank. BillingID резолвит связку в ОБЕ стороны (подписка может
	// быть под MAX- или TG-id) — иначе обратная сторона (купили в MAX, зашли из TG) не видела бы PRO.
	pro := s.billing != nil && s.billing.IsActive(s.billing.BillingID(u.ID))
	resp := map[string]any{
		"user":     map[string]any{"id": u.ID, "name": u.Name},
		"pro":      pro,
		"admin":    s.isAdminUser(u), // показывать админ-панель
		"platform": u.Platform,
		"linked":   u.Platform != "max" || effTg != 0, // для MAX: есть ли привязка TG
	}
	// Статус T-Bank подписки (для кнопки «Отменить»/«Оформить» в кабинете). Резолвим id к тому,
	// под которым лежит активная подписка (у MAX-юзера она под связанным TG-id) — иначе статус/
	// карта показывались бы по пустой pending-строке MAX-id.
	if s.billing != nil {
		bid := s.billing.BillingID(u.ID)
		st, until := s.billing.SubStatus(bid)
		resp["sub_status"] = st
		resp["sub_until"] = until
		resp["trial_used"] = s.billing.TrialUsed(bid)
		resp["card_pan"] = s.billing.CardPAN(bid)   // маскированная карта ("" если нет)
		resp["has_rebill"] = s.billing.HasRebill(bid) // можно возобновить без новой оплаты
		resp["mirror_slots"] = s.billing.MirrorSlots(bid)
	}
	// Слоты тарифа (мосты + зеркала + каналы) и зеркальные связки юзера.
	resp["slots"] = s.slotsInfo(u)
	resp["mirrors"] = s.listMirrors(u)

	var cps []crosspost
	if bridgeDBPath != "" {
		if db, err := sql.Open("sqlite", "file:"+bridgeDBPath+"?mode=ro&_pragma=busy_timeout(3000)"); err == nil {
			defer db.Close()
			rows, err := db.Query(`SELECT tg_chat_id, max_chat_id, direction, replacements, sync_edits, COALESCE(paused,0) FROM crossposts
				WHERE deleted_at=0 AND (owner_id=? OR tg_owner_id=?)`, u.ID, tgKey)
			if err == nil {
				for rows.Next() {
					var c crosspost
					var repl string
					var sync, paused int
					if rows.Scan(&c.TgChatID, &c.MaxChatID, &c.Direction, &repl, &sync, &paused) == nil {
						c.HasReplacements = strings.TrimSpace(repl) != "" && repl != "{}"
						c.SyncEdits = sync != 0
						c.Paused = paused != 0
						if repl != "" {
							_ = json.Unmarshal([]byte(repl), &c.Replacements)
						}
						cps = append(cps, c)
					}
				}
				rows.Close()
			}
			// названия каналов (после закрытия rows — внешние HTTP-вызовы)
			for i := range cps {
				cps[i].Title = chatTitle(cps[i].TgChatID)
				cps[i].Antispam, cps[i].AntispamMode = s.store.GetAntispam(cps[i].TgChatID)
				if disc := tgLinkedChat(cps[i].TgChatID); disc != 0 {
					pol := antispamPolicy("tg", disc)
					cps[i].StrikeLimit, cps[i].BanAfter, cps[i].Action, cps[i].MuteMinutes, cps[i].Warn, cps[i].Notify, cps[i].Captcha, cps[i].Antiraid, cps[i].BlockWords, cps[i].BlockCats, cps[i].DelService = pol.StrikeLimit, pol.BanAfter, pol.Action, pol.MuteMinutes, pol.Warn, pol.Notify, pol.Captcha, pol.Antiraid, pol.BlockWords, pol.BlockCats, pol.DelService
					if cps[i].Antispam { // проверяем права бота только если антиспам включён
						cps[i].BotAdmin = tgBotIsAdmin(disc)
					}
				}
			}
		}
	}

	if addonDBPath != "" {
		if db, err := sql.Open("sqlite", "file:"+addonDBPath+"?mode=ro&_pragma=busy_timeout(3000)"); err == nil {
			defer db.Close()
			var bal int
			_ = db.QueryRow(`SELECT credits FROM entitlements WHERE user_id=?`, tgKey).Scan(&bal)
			resp["import_balance"] = bal
			// статус комментариев по каждой связке
			for i := range cps {
				var en int
				_ = db.QueryRow(`SELECT enabled FROM channel_comments WHERE max_chat_id=?`, cps[i].MaxChatID).Scan(&en)
				cps[i].CommentsEnabled = en != 0
			}
		}
	}
	resp["crossposts"] = cps
	resp["groups"] = userGroups(u.ID, s.effectiveTgID(u))

	writeJSON(w, http.StatusOK, resp)
}

// proExcluded — id, которым принудительно не выдаём PRO по группе (для тестов
// сквозного пути «бесплатный → оплата → PRO»). billing.IsActive это не отменяет.
func proExcluded(uid int64) bool {
	raw := os.Getenv("PRO_EXCLUDE_USERS")
	if raw == "" {
		return false
	}
	want := strconv.FormatInt(uid, 10)
	for _, s := range strings.Split(raw, ",") {
		if strings.TrimSpace(s) == want {
			return true
		}
	}
	return false
}

// isProTG — состоит ли юзер в PRO-группе Telegram (getChatMember).
func isProTG(uid int64) bool {
	if proExcluded(uid) {
		return false
	}
	if proGroupID == "" || tgAPIURL == "" || tgBotToken == "" {
		return false
	}
	u := tgAPIURL + "/bot" + tgBotToken + "/getChatMember?chat_id=" + url.QueryEscape(proGroupID) +
		"&user_id=" + strconv.FormatInt(uid, 10)
	resp, err := http.Get(u)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var out struct {
		OK     bool `json:"ok"`
		Result struct {
			Status string `json:"status"`
		} `json:"result"`
	}
	if json.NewDecoder(resp.Body).Decode(&out) != nil || !out.OK {
		return false
	}
	switch strings.ToLower(out.Result.Status) {
	case "member", "administrator", "creator":
		return true
	}
	return false
}
