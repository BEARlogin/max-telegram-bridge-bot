package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type adCampaignStats struct {
	ID               int64   `json:"id"`
	Name             string  `json:"name"`
	Source           string  `json:"source"`
	Note             string  `json:"note"`
	Active           bool    `json:"active"`
	CreatedAt        int64   `json:"created_at"`
	Link             string  `json:"link"`
	Starts           int64   `json:"starts"`
	UniqueVisitors   int64   `json:"unique_visitors"`
	AttributedUsers  int64   `json:"attributed_users"`
	NewUsers         int64   `json:"new_users"`
	ActivatedUsers   int64   `json:"activated_users"`
	TrialUsers       int64   `json:"trial_users"`
	PaidUsers        int64   `json:"paid_users"`
	ProUsers         int64   `json:"pro_users"`
	RevenueKopecks   int64   `json:"revenue_kopecks"`
	RevenueRub       float64 `json:"revenue_rub"`
	LastStartAt      int64   `json:"last_start_at"`
	ConversionToPaid float64 `json:"conversion_to_paid"`
}

type adAttribution struct {
	CampaignID int64
	UserID     int64
	Attributed int64
	IsNew      bool
}

const adCampaignSchema = `
CREATE TABLE IF NOT EXISTS ad_campaigns (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL,
	source TEXT NOT NULL DEFAULT '',
	note TEXT NOT NULL DEFAULT '',
	active INTEGER NOT NULL DEFAULT 1,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS ad_campaign_starts (
	campaign_id INTEGER NOT NULL,
	user_id INTEGER NOT NULL,
	tg_message_id INTEGER NOT NULL,
	started_at INTEGER NOT NULL,
	is_new INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (campaign_id,user_id,tg_message_id)
);
CREATE INDEX IF NOT EXISTS idx_ad_campaign_starts_campaign
	ON ad_campaign_starts(campaign_id,started_at);
CREATE INDEX IF NOT EXISTS idx_ad_campaign_starts_user
	ON ad_campaign_starts(user_id,started_at);
CREATE TABLE IF NOT EXISTS ad_attributions (
	user_id INTEGER PRIMARY KEY,
	campaign_id INTEGER NOT NULL,
	attributed_at INTEGER NOT NULL,
	is_new INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_ad_attributions_campaign
	ON ad_attributions(campaign_id,attributed_at);
`

func ensureAdCampaignSchema(db *sql.DB) error {
	_, err := db.Exec(adCampaignSchema)
	return err
}

func adCampaignBotURL() string {
	return strings.TrimRight(envOr("TG_BOT_URL", "https://t.me/MaxTelegramBridgeBot"), "/")
}

func adCampaignLink(id int64) string {
	base := adCampaignBotURL()
	parsed, err := url.Parse(base)
	if err != nil {
		return base + "?start=" + strconv.FormatInt(id, 10)
	}
	query := parsed.Query()
	query.Set("start", strconv.FormatInt(id, 10))
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func (s *server) handleAdminCampaigns(w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	if !u.Valid {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	if !s.isAdminUser(u) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "forbidden"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		billingDB := (*sql.DB)(nil)
		if st, ok := s.store.(*sqliteStore); ok {
			billingDB = st.db
		}
		campaigns, err := loadAdCampaignStats(addonDBPath, bridgeDBPath, billingDB)
		if err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "no such table") {
				writeJSON(w, http.StatusOK, map[string]any{
					"campaigns": []adCampaignStats{}, "bot_url": adCampaignBotURL(),
				})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "Не удалось загрузить кампании"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"campaigns": campaigns, "bot_url": adCampaignBotURL()})
	case http.MethodPost:
		if strings.TrimSpace(addonDBPath) == "" {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "Хранилище рекламных кампаний не настроено"})
			return
		}
		var in struct {
			Name   string `json:"name"`
			Source string `json:"source"`
			Note   string `json:"note"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&in) != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Некорректные данные"})
			return
		}
		in.Name = strings.TrimSpace(in.Name)
		in.Source = strings.TrimSpace(in.Source)
		in.Note = strings.TrimSpace(in.Note)
		if in.Name == "" || utf8.RuneCountInString(in.Name) > 120 ||
			utf8.RuneCountInString(in.Source) > 80 || utf8.RuneCountInString(in.Note) > 500 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Проверьте название и описание"})
			return
		}
		db, err := openRW(addonDBPath)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "База кампаний недоступна"})
			return
		}
		defer db.Close()
		if err = ensureAdCampaignSchema(db); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "Не удалось подготовить базу кампаний"})
			return
		}
		now := time.Now().Unix()
		result, err := db.Exec(`INSERT INTO ad_campaigns(name,source,note,active,created_at,updated_at)
			VALUES(?,?,?,1,?,?)`, in.Name, in.Source, in.Note, now, now)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "Не удалось создать кампанию"})
			return
		}
		id, _ := result.LastInsertId()
		writeJSON(w, http.StatusCreated, map[string]any{
			"ok": true, "id": id, "link": adCampaignLink(id),
		})
	default:
		w.Header().Set("Allow", "GET, POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
	}
}

func (s *server) handleAdminCampaignActive(w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	if !u.Valid {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	if !s.isAdminUser(u) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "forbidden"})
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	if strings.TrimSpace(addonDBPath) == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "Хранилище рекламных кампаний не настроено"})
		return
	}
	var in struct {
		ID     int64 `json:"id"`
		Active bool  `json:"active"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&in) != nil || in.ID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Некорректная кампания"})
		return
	}
	db, err := openRW(addonDBPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "База кампаний недоступна"})
		return
	}
	defer db.Close()
	value := 0
	if in.Active {
		value = 1
	}
	result, err := db.Exec(`UPDATE ad_campaigns SET active=?,updated_at=? WHERE id=?`,
		value, time.Now().Unix(), in.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "Не удалось изменить кампанию"})
		return
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "Кампания не найдена"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func loadAdCampaignStats(addonPath, bridgePath string, billingDB *sql.DB) ([]adCampaignStats, error) {
	if strings.TrimSpace(addonPath) == "" {
		return nil, errors.New("addon database is not configured")
	}
	addonDB, err := sql.Open("sqlite", "file:"+addonPath+"?mode=ro&_pragma=busy_timeout(3000)")
	if err != nil {
		return nil, err
	}
	defer addonDB.Close()

	rows, err := addonDB.Query(`SELECT c.id,c.name,c.source,c.note,c.active,c.created_at,
		(SELECT COUNT(*) FROM ad_campaign_starts s WHERE s.campaign_id=c.id),
		(SELECT COUNT(DISTINCT s.user_id) FROM ad_campaign_starts s WHERE s.campaign_id=c.id),
		(SELECT COALESCE(MAX(s.started_at),0) FROM ad_campaign_starts s WHERE s.campaign_id=c.id)
		FROM ad_campaigns c ORDER BY c.created_at DESC,c.id DESC`)
	if err != nil {
		return nil, err
	}
	campaigns := make([]adCampaignStats, 0)
	byID := map[int64]int{}
	for rows.Next() {
		var campaign adCampaignStats
		var active int
		if err = rows.Scan(&campaign.ID, &campaign.Name, &campaign.Source, &campaign.Note,
			&active, &campaign.CreatedAt, &campaign.Starts, &campaign.UniqueVisitors,
			&campaign.LastStartAt); err != nil {
			_ = rows.Close()
			return nil, err
		}
		campaign.Active = active != 0
		campaign.Link = adCampaignLink(campaign.ID)
		campaigns = append(campaigns, campaign)
		byID[campaign.ID] = len(campaigns) - 1
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}

	attributions := map[int64]adAttribution{}
	attrRows, err := addonDB.Query(`SELECT user_id,campaign_id,attributed_at,is_new FROM ad_attributions`)
	if err != nil {
		return nil, err
	}
	for attrRows.Next() {
		var item adAttribution
		var isNew int
		if attrRows.Scan(&item.UserID, &item.CampaignID, &item.Attributed, &isNew) == nil {
			item.IsNew = isNew != 0
			attributions[item.UserID] = item
			if index, ok := byID[item.CampaignID]; ok {
				campaign := &campaigns[index]
				campaign.AttributedUsers++
				if item.IsNew {
					campaign.NewUsers++
				}
			}
		}
	}
	_ = attrRows.Close()

	firstActivation := map[int64]int64{}
	mergeActivation := func(db *sql.DB, query string) {
		if db == nil {
			return
		}
		result, queryErr := db.Query(query)
		if queryErr != nil {
			return
		}
		defer result.Close()
		for result.Next() {
			var userID, createdAt int64
			if result.Scan(&userID, &createdAt) == nil && userID > 0 && createdAt > 0 &&
				(firstActivation[userID] == 0 || createdAt < firstActivation[userID]) {
				firstActivation[userID] = createdAt
			}
		}
	}
	if bridgePath != "" {
		bridgeDB, openErr := sql.Open("sqlite", "file:"+bridgePath+"?mode=ro&_pragma=busy_timeout(3000)")
		if openErr == nil {
			mergeActivation(bridgeDB, `SELECT tg_owner_id,MIN(created_at) FROM pairs
				WHERE tg_owner_id!=0 GROUP BY tg_owner_id`)
			mergeActivation(bridgeDB, `SELECT tg_owner_id,MIN(created_at) FROM crossposts
				WHERE tg_owner_id!=0 AND deleted_at=0 GROUP BY tg_owner_id`)
			_ = bridgeDB.Close()
		}
	}
	mergeActivation(addonDB, `SELECT owner_id,MIN(created_at) FROM vk_bindings
		WHERE owner_id!=0 GROUP BY owner_id`)
	mergeActivation(addonDB, `SELECT owner_id,MIN(created_at) FROM tg_mirror
		WHERE owner_id!=0 GROUP BY owner_id`)
	for userID, attribution := range attributions {
		if activatedAt := firstActivation[userID]; activatedAt >= attribution.Attributed {
			if index, ok := byID[attribution.CampaignID]; ok {
				campaign := &campaigns[index]
				campaign.ActivatedUsers++
			}
		}
	}

	if billingDB != nil && len(attributions) > 0 {
		trialRows, trialErr := billingDB.Query(`SELECT user_id,trial_used,updated_at FROM subscriptions`)
		if trialErr == nil {
			for trialRows.Next() {
				var userID, createdAt int64
				var trialUsed int
				if trialRows.Scan(&userID, &trialUsed, &createdAt) == nil && trialUsed != 0 {
					if attribution, ok := attributions[userID]; ok && createdAt >= attribution.Attributed {
						if index, exists := byID[attribution.CampaignID]; exists {
							campaign := &campaigns[index]
							campaign.TrialUsers++
						}
					}
				}
			}
			_ = trialRows.Close()
		}
		paidUsers := map[int64]bool{}
		proUsers := map[int64]bool{}
		paymentRows, paymentErr := billingDB.Query(`SELECT user_id,amount,kind,at FROM payments
			WHERE status IN ('AUTHORIZED','CONFIRMED')`)
		if paymentErr == nil {
			for paymentRows.Next() {
				var userID, amount, paidAt int64
				var kind string
				if paymentRows.Scan(&userID, &amount, &kind, &paidAt) != nil {
					continue
				}
				attribution, ok := attributions[userID]
				if !ok || paidAt < attribution.Attributed {
					continue
				}
				index, exists := byID[attribution.CampaignID]
				if !exists {
					continue
				}
				campaign := &campaigns[index]
				campaign.RevenueKopecks += amount
				if !paidUsers[userID] {
					campaign.PaidUsers++
					paidUsers[userID] = true
				}
				if kind == "sub" && !proUsers[userID] {
					campaign.ProUsers++
					proUsers[userID] = true
				}
			}
			_ = paymentRows.Close()
		}
	}
	for i := range campaigns {
		campaigns[i].RevenueRub = float64(campaigns[i].RevenueKopecks) / 100
		if campaigns[i].AttributedUsers > 0 {
			campaigns[i].ConversionToPaid =
				float64(campaigns[i].PaidUsers) * 100 / float64(campaigns[i].AttributedUsers)
		}
	}
	return campaigns, nil
}
