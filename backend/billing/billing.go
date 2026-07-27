// Package billing — приём платежей через T-Bank (Tinkoff) с рекуррентом.
// Базируется на github.com/nikita-vanyasin/tinkoff (как в mediacrawlers/paycore),
// плюс автосписания: первый платёж регистрирует RebillId, дальше Charge по расписанию.
package billing

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"embed"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nikita-vanyasin/tinkoff"
)

// russianCAFS — корневой и промежуточный сертификаты Russian Trusted CA (Минцифры).
// T-Bank мигрирует API с HARICA на эти сертификаты; без них TLS к securepay.tinkoff.ru
// отвалится ("certificate verify failed"). См. developer.tbank.ru/eacq/.../migration-russian-trusted-ca.
//
//go:embed certs/russian_trusted_root.pem certs/russian_trusted_sub.pem
var russianCAFS embed.FS

// tbankHTTPClient — http-клиент ТОЛЬКО для T-Bank API: системные CA + Russian Trusted
// Root/Sub CA. Пул = система + русские корни, поэтому текущий HARICA продолжит работать,
// а переключение T-Bank на русский CA пройдёт бесшовно. Системный trust НЕ трогаем —
// доверие к русскому CA ограничено этим клиентом.
func tbankHTTPClient() *http.Client {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	for _, name := range []string{"certs/russian_trusted_root.pem", "certs/russian_trusted_sub.pem"} {
		pem, rerr := russianCAFS.ReadFile(name)
		if rerr != nil {
			log.Printf("tbank CA: read %s failed: %v", name, rerr)
			continue
		}
		if !pool.AppendCertsFromPEM(pem) {
			log.Printf("tbank CA: append %s failed", name)
		}
	}
	return &http.Client{
		Timeout:   30 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}},
	}
}

type Config struct {
	TerminalKey   string
	Password      string
	NotifyURL     string // куда T-Bank шлёт нотификации
	SuccessURL    string
	FailURL       string
	AmountKopecks uint64 // цена для новой ценовой когорты; старую сохраняем в subscriptions.amount
	PeriodDays    int    // 30 — период подписки
	ReceiptEmail  string
	Taxation      string // система налогообложения для чека (по умолчанию usn_income)
}

type Service struct {
	cfg Config
	cli *tinkoff.Client
	db  *sql.DB
}

func New(db *sql.DB, cfg Config) (*Service, error) {
	if cfg.PeriodDays == 0 {
		cfg.PeriodDays = 30
	}
	cli := tinkoff.NewClientWithOptions(
		tinkoff.WithTerminalKey(cfg.TerminalKey),
		tinkoff.WithPassword(cfg.Password),
		tinkoff.WithHTTPClient(tbankHTTPClient()),
	)
	s := &Service{cfg: cfg, cli: cli, db: db}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Service) Enabled() bool { return s.cfg.TerminalKey != "" && s.cfg.Password != "" }

func (s *Service) migrate() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS subscriptions (
		user_id     INTEGER PRIMARY KEY,
		order_id    TEXT NOT NULL DEFAULT '',  -- текущий OrderID (меняется на каждом списании)
		rebill_id   TEXT NOT NULL DEFAULT '',  -- для автосписаний
		payment_id  TEXT NOT NULL DEFAULT '',  -- PaymentId последней операции (для возврата)
		status      TEXT NOT NULL DEFAULT 'pending', -- pending|active|canceled|past_due
		amount      INTEGER NOT NULL DEFAULT 0,
		paid_until  INTEGER NOT NULL DEFAULT 0,
		next_charge INTEGER NOT NULL DEFAULT 0,
		created_at  INTEGER NOT NULL,
		updated_at  INTEGER NOT NULL
	)`); err != nil {
		return err
	}
	// История платежей — источник идемпотентности (один payment_id применяем один раз)
	// и аудит «кто/когда/сколько заплатил». Раньше идемпотентность шла по payment_id в
	// subscriptions, но Init писал туда payment_id заранее → notify считал оплату дублем
	// и НЕ активировал подписку. Теперь сверяемся с этой таблицей.
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS payments (
		payment_id TEXT PRIMARY KEY,
		user_id    INTEGER NOT NULL DEFAULT 0,
		order_id   TEXT NOT NULL DEFAULT '',
		amount     INTEGER NOT NULL DEFAULT 0,
		rebill_id  TEXT NOT NULL DEFAULT '',
		status     TEXT NOT NULL DEFAULT '',
		kind       TEXT NOT NULL DEFAULT '', -- sub | posts
		at         INTEGER NOT NULL DEFAULT 0
	)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS billing_attempts (
		payment_id TEXT PRIMARY KEY,
		user_id    INTEGER NOT NULL,
		order_id   TEXT NOT NULL,
		amount     INTEGER NOT NULL,
		status     TEXT NOT NULL,
		at         INTEGER NOT NULL
	)`); err != nil {
		return err
	}
	// Идемпотентные миграции.
	_, _ = s.db.Exec(`ALTER TABLE subscriptions ADD COLUMN payment_id TEXT NOT NULL DEFAULT ''`)
	_, _ = s.db.Exec(`ALTER TABLE subscriptions ADD COLUMN trial_used INTEGER NOT NULL DEFAULT 0`)
	_, _ = s.db.Exec(`ALTER TABLE subscriptions ADD COLUMN card_pan TEXT NOT NULL DEFAULT ''`)
	_, _ = s.db.Exec(`ALTER TABLE subscriptions ADD COLUMN mirror_slots INTEGER NOT NULL DEFAULT 0`)
	return nil
}

// CardPAN — маскированный номер привязанной карты (для показа в кабинете). "" если нет.
func (s *Service) CardPAN(userID int64) string {
	var pan string
	s.db.QueryRow(`SELECT COALESCE(card_pan,'') FROM subscriptions WHERE user_id=?`, userID).Scan(&pan)
	return pan
}

// HasRebill — есть ли сохранённая карта (rebill) для возобновления без новой оплаты.
func (s *Service) HasRebill(userID int64) bool {
	var r string
	s.db.QueryRow(`SELECT COALESCE(rebill_id,'') FROM subscriptions WHERE user_id=?`, userID).Scan(&r)
	return r != ""
}

// BaseAmount возвращает базовую цену пользователя. Legacy-цена сохраняется только
// за теми, у кого в истории есть фактически успешная оплата подписки. Наличие
// trial/pending-записи или ранее показанной платёжной формы скидку не фиксирует.
func (s *Service) BaseAmount(userID int64) uint64 {
	var amount uint64
	_ = s.db.QueryRow(`SELECT amount FROM payments
		WHERE user_id=? AND kind='sub' AND status IN ('AUTHORIZED','CONFIRMED') AND amount>0
		ORDER BY at ASC LIMIT 1`, userID).Scan(&amount)
	if amount > 0 {
		return amount
	}
	return s.cfg.AmountKopecks
}

// Resume возобновляет подписку без отправки на полную оплату, когда это возможно:
//
//	"resumed"   — в пределах оплаченного периода: просто включаем автопродление, без денег.
//	"charging"  — период истёк, но есть карта (rebill): списываем по ней (подтвердит нотификация).
//	"need_card" — карты нет: нужна полная оплата (фронт зовёт subscribe).
func (s *Service) Resume(ctx context.Context, userID int64) (string, error) {
	_, until := s.SubStatus(userID)
	now := time.Now().Unix()
	var rebill string
	s.db.QueryRow(`SELECT COALESCE(rebill_id,'') FROM subscriptions WHERE user_id=?`, userID).Scan(&rebill)

	// Ещё в оплаченном периоде (отменён или активен) → возобновляем автопродление без оплаты.
	if until > now {
		next := int64(0)
		if rebill != "" {
			next = until - int64(renewLeadDays)*86400
			if next < now {
				next = now
			}
		}
		_, err := s.db.Exec(`UPDATE subscriptions SET status='active', next_charge=?, updated_at=? WHERE user_id=?`,
			next, now, userID)
		return "resumed", err
	}
	// Период истёк, но карта привязана → списываем по rebill (без формы оплаты).
	if rebill != "" {
		if err := s.charge(ctx, userID, rebill); err != nil {
			return "", err
		}
		return "charging", nil
	}
	return "need_card", nil
}

// orderUID извлекает user_id из OrderID вида "sub-<uid>-…" / "posts-<uid>-…".
func orderUID(orderID string) int64 {
	parts := strings.Split(orderID, "-")
	if len(parts) >= 2 {
		uid, _ := strconv.ParseInt(parts[1], 10, 64)
		return uid
	}
	return 0
}

// recordPayment пишет платёж в историю. Возвращает false, если payment_id уже был
// учтён (идемпотентность) — тогда применять повторно не нужно.
func (s *Service) recordPayment(paymentID string, orderID, status, rebillID, kind string, amount uint64) (fresh bool) {
	res, err := s.db.Exec(`INSERT OR IGNORE INTO payments (payment_id, user_id, order_id, amount, rebill_id, status, kind, at)
		VALUES (?, ?, ?, ?, ?, ?, ?, strftime('%s','now'))`,
		paymentID, orderUID(orderID), orderID, amount, rebillID, status, kind)
	if err != nil {
		return true // не блокируем активацию из-за сбоя записи истории
	}
	n, _ := res.RowsAffected()
	return n > 0
}

// TrialUsed — использовал ли юзер бесплатный триал.
func (s *Service) TrialUsed(userID int64) bool {
	var used int
	s.db.QueryRow(`SELECT trial_used FROM subscriptions WHERE user_id=?`, userID).Scan(&used)
	return used == 1
}

// StartTrial выдаёт бесплатный PRO на days дней (без карты, один раз на юзера).
func (s *Service) StartTrial(userID int64, days int) error {
	status, until := s.SubStatus(userID)
	now := time.Now().Unix()
	if (status == "active" || status == "trial" || status == "canceled") && until > now {
		return fmt.Errorf("PRO уже активен")
	}
	if s.TrialUsed(userID) {
		return fmt.Errorf("триал уже использован")
	}
	end := time.Now().AddDate(0, 0, days).Unix()
	_, err := s.db.Exec(`INSERT INTO subscriptions (user_id, status, paid_until, trial_used, created_at, updated_at)
		VALUES (?, 'trial', ?, 1, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET status='trial', paid_until=?, trial_used=1, updated_at=?`,
		userID, end, now, now, end, now)
	return err
}

// PayPosts создаёт разовый платёж за пакет постов импорта (не подписка). posts —
// сколько постов в пакете (попадает в описание платежа и в позицию чека, чтобы
// покупателю было видно «N постов»). OrderID с префиксом "posts-" — по нему
// нотификация отличает покупку постов от подписки. Возвращает URL оплаты.
func (s *Service) PayPosts(userID int64, amount uint64, posts int, orderSuffix string) (url, paymentID string, err error) {
	if amount == 0 {
		amount = s.cfg.AmountKopecks
	}
	itemName := fmt.Sprintf("%d постов для импорта канала в MAX", posts)
	req := &tinkoff.InitRequest{
		Amount:          amount,
		OrderID:         fmt.Sprintf("posts-%d-%d-%s", userID, posts, orderSuffix),
		Description:     itemName,
		NotificationURL: s.cfg.NotifyURL,
		SuccessURL:      s.cfg.SuccessURL,
		FailURL:         s.cfg.FailURL,
		Data:            map[string]string{"user_id": strconv.FormatInt(userID, 10), "kind": "posts", "posts": strconv.Itoa(posts)},
	}
	if s.cfg.ReceiptEmail != "" {
		req.Receipt = s.receiptForItem(amount, itemName)
	}
	resp, err := s.cli.Init(req)
	if err != nil {
		return "", "", err
	}
	return resp.PaymentURL, resp.PaymentID, nil
}

// Parse разбирает и валидирует нотификацию T-Bank (подпись/терминал).
func (s *Service) Parse(body []byte) (*tinkoff.Notification, error) {
	return s.cli.ParseNotification(bytes.NewBuffer(body))
}

// NotifyOK — успешный ответ, который ждёт T-Bank на нотификацию.
func (s *Service) NotifyOK() string { return s.cli.GetNotificationSuccessResponse() }

// PayOnce создаёт обычный (не рекуррентный) платёж на заданную сумму. Для
// прохождения тестов сертификации терминала T-Bank (успешная/неуспешная оплата),
// где нужен «ванильный» платёж, а не подписка. amount=0 — берём сумму из конфига.
func (s *Service) PayOnce(amount uint64, orderSuffix string) (url, paymentID string, err error) {
	if amount == 0 {
		amount = s.cfg.AmountKopecks
	}
	req := &tinkoff.InitRequest{
		Amount:          amount,
		OrderID:         "cert-" + orderSuffix,
		Description:     "Тестовый платёж (сертификация)",
		NotificationURL: s.cfg.NotifyURL,
		SuccessURL:      s.cfg.SuccessURL,
		FailURL:         s.cfg.FailURL,
	}
	if s.cfg.ReceiptEmail != "" {
		req.Receipt = s.receiptFor(amount)
	}
	resp, err := s.cli.Init(req)
	if err != nil {
		return "", "", err
	}
	return resp.PaymentURL, resp.PaymentID, nil
}

// Refund возвращает (отменяет) платёж по PaymentId. amount=0 — полный возврат.
// Если задан ReceiptEmail и amount>0 — прикладывает чек возврата (тест «Чек возврата»).
func (s *Service) Refund(paymentID string, amount uint64) (*tinkoff.CancelResponse, error) {
	req := &tinkoff.CancelRequest{PaymentID: paymentID, Amount: amount}
	if s.cfg.ReceiptEmail != "" && amount > 0 {
		req.Receipt = s.receiptFor(amount)
	}
	return s.cli.Cancel(req)
}

// LastPaymentID возвращает PaymentId последней операции пользователя (для возврата).
func (s *Service) LastPaymentID(userID int64) string {
	var pid string
	_ = s.db.QueryRow(`SELECT payment_id FROM subscriptions WHERE user_id=?`, userID).Scan(&pid)
	return pid
}

// Subscribe создаёт первый платёж с регистрацией автоплатежа. Возвращает URL оплаты.
func (s *Service) Subscribe(ctx context.Context, userID int64, name string) (string, error) {
	orderID := fmt.Sprintf("sub-%d-%d", userID, time.Now().UnixNano())
	amount := s.BaseAmount(userID)
	req := &tinkoff.InitRequest{
		Amount:          amount,
		OrderID:         orderID,
		CustomerKey:     strconv.FormatInt(userID, 10),
		Description:     "PRO-подписка MaxTelegramBridgeBot",
		NotificationURL: s.cfg.NotifyURL,
		SuccessURL:      s.cfg.SuccessURL,
		FailURL:         s.cfg.FailURL,
		Recurrent:       "Y", // регистрируем автоплатёж
		Data:            map[string]string{"user_id": strconv.FormatInt(userID, 10)},
	}
	if s.cfg.ReceiptEmail != "" {
		req.Receipt = s.receiptFor(amount)
	}
	resp, err := s.cli.InitWithContext(ctx, req)
	if err != nil {
		return "", err
	}
	now := time.Now().Unix()
	// На конфликте НЕ понижаем статус: если у юзера ещё валиден триал/подписка
	// (active|canceled|trial и paid_until в будущем), он сохраняет доступ, пока идёт
	// оплата. status='active' проставит только успешная нотификация (notify.go).
	// Иначе старт оплаты при живом триале гасил бы PRO в pending.
	_, err = s.db.Exec(`INSERT INTO subscriptions (user_id, order_id, payment_id, status, amount, created_at, updated_at)
		VALUES (?, ?, ?, 'pending', ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET order_id=excluded.order_id, payment_id=excluded.payment_id,
			status=CASE WHEN subscriptions.status IN ('active','canceled','trial') AND subscriptions.paid_until > excluded.updated_at
				THEN subscriptions.status ELSE 'pending' END,
			amount=excluded.amount, updated_at=excluded.updated_at`,
		userID, orderID, resp.PaymentID, amount, now, now)
	if err != nil {
		return "", err
	}
	return resp.PaymentURL, nil
}

// receiptFor — чек подписки (название позиции по умолчанию).
func (s *Service) receiptFor(amount uint64) *tinkoff.Receipt {
	return s.receiptForItem(amount, "PRO-подписка MaxTelegramBridgeBot")
}

// receiptForItem собирает чек на сумму (в копейках) с заданным названием позиции.
// Сумма позиции совпадает с суммой платежа — иначе T-Bank отклонит чек.
func (s *Service) receiptForItem(amount uint64, name string) *tinkoff.Receipt {
	tax := s.cfg.Taxation
	if tax == "" {
		tax = tinkoff.TaxationUSNIncome
	}
	return &tinkoff.Receipt{
		Email:    s.cfg.ReceiptEmail,
		Taxation: tax,
		Items: []*tinkoff.ReceiptItem{{
			Name:          name,
			Price:         amount,
			Quantity:      "1",
			Amount:        amount,
			Tax:           tinkoff.VATNone,
			PaymentMethod: tinkoff.PaymentMethodFullPayment,
			PaymentObject: tinkoff.PaymentObjectService,
		}},
		Payments: &tinkoff.ReceiptPayments{Electronic: amount},
	}
}
