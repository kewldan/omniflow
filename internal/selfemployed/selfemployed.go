// Package selfemployed issues «Мой налог» receipts for an operator registered
// as self-employed (НПД), the way Bedolaga's NaloGO integration does.
//
// Every ruble payment settled after the integration was switched on gets one
// receipt: queued in self_employed_receipts (unique per order, which is the
// dedupe), sent by the worker's pass with retries, checked against the tax
// service before any retry whose first attempt had an unknown outcome so a
// timeout never produces a second receipt, cancelled when the order is fully
// refunded, and announced in the operator group's own receipts topic.
package selfemployed

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/nalogo"
	"github.com/omniflow/omniflow/internal/panelpg"
)

const (
	// retryInterval and maxAttempts give a failing receipt twelve hours, as
	// Bedolaga's queue does (72 attempts ten minutes apart).
	retryInterval = 10 * time.Minute
	maxAttempts   = 72
	batch         = 20
)

// Errors the panel maps to answers.
var (
	ErrInvalid  = errors.New("the settings are not valid")
	ErrNotFound = errors.New("no such receipt")
	ErrState    = errors.New("the receipt cannot be retried in its current state")
)

var innPattern = regexp.MustCompile(`^[0-9]{12}$`)

// Settings is the integration's configuration as the panel shows it. The
// password is write-only.
type Settings struct {
	Enabled     bool       `json:"enabled"`
	EnabledAt   *time.Time `json:"enabledAt,omitempty"`
	INN         string     `json:"inn"`
	HasPassword bool       `json:"hasPassword"`
	ServiceName string     `json:"serviceName"`
	Providers   []string   `json:"providers"`
}

// SettingsInput changes the configuration. An empty Password keeps the
// stored one.
type SettingsInput struct {
	Enabled     bool     `json:"enabled"`
	INN         string   `json:"inn"`
	Password    string   `json:"password"`
	ServiceName string   `json:"serviceName"`
	Providers   []string `json:"providers"`
}

// Receipt is one queued or issued receipt.
type Receipt struct {
	ID            string     `json:"id"`
	OrderID       string     `json:"orderId"`
	AmountMinor   int64      `json:"amountMinor"`
	Status        string     `json:"status"`
	Attempts      int        `json:"attempts"`
	ReceiptURL    string     `json:"receiptUrl,omitempty"`
	LastErrorCode string     `json:"lastErrorCode,omitempty"`
	OperationTime time.Time  `json:"operationTime"`
	IssuedAt      *time.Time `json:"issuedAt,omitempty"`
}

// Options configures the service.
type Options struct {
	DataKey []byte
	// BaseURL and HTTP override the tax service, for tests.
	BaseURL string
	HTTP    *http.Client
	Logger  *slog.Logger
}

// Service is the integration.
type Service struct {
	pool    *pgxpool.Pool
	aead    cipher.AEAD
	options Options
}

// New builds the service.
func New(pool *pgxpool.Pool, options Options) (*Service, error) {
	if len(options.DataKey) != 32 {
		return nil, errors.New("self-employed receipts need APP_DATA_ENCRYPTION_KEY to seal the password")
	}
	block, err := aes.NewCipher(options.DataKey)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	if options.BaseURL == "" {
		options.BaseURL = nalogo.DefaultBaseURL
	}
	return &Service{pool: pool, aead: aead, options: options}, nil
}

// Settings reads the configuration.
func (service *Service) Settings(ctx context.Context) (Settings, error) {
	var settings Settings
	err := service.pool.QueryRow(ctx, `SELECT enabled, enabled_at, inn, password_ciphertext IS NOT NULL, service_name, providers
		FROM self_employed_settings`).Scan(&settings.Enabled, &settings.EnabledAt, &settings.INN, &settings.HasPassword,
		&settings.ServiceName, &settings.Providers)
	if settings.Providers == nil {
		settings.Providers = []string{}
	}
	return settings, err
}

// SaveSettings validates and stores the configuration. Changing the taxpayer
// number or the password discards the cached token.
func (service *Service) SaveSettings(ctx context.Context, input SettingsInput, actor panelpg.Actor) (Settings, error) {
	input.INN = strings.TrimSpace(input.INN)
	input.ServiceName = strings.TrimSpace(input.ServiceName)
	if input.ServiceName == "" {
		input.ServiceName = "Услуги VPN"
	}
	if (input.INN != "" && !innPattern.MatchString(input.INN)) || utf8.RuneCountInString(input.ServiceName) > 128 {
		return Settings{}, ErrInvalid
	}
	providers := []string{}
	for _, provider := range input.Providers {
		if provider = strings.TrimSpace(provider); provider != "" {
			providers = append(providers, provider)
		}
	}
	var sealed []byte
	if input.Password != "" {
		var err error
		if sealed, err = service.seal([]byte(input.Password), "self_employed_password"); err != nil {
			return Settings{}, err
		}
	}
	tx, err := service.pool.Begin(ctx)
	if err != nil {
		return Settings{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `UPDATE self_employed_settings SET
			enabled = $1,
			enabled_at = CASE WHEN $1 AND NOT enabled THEN now() WHEN $1 THEN enabled_at ELSE NULL END,
			token_ciphertext = CASE WHEN inn <> $2 OR $3::bytea IS NOT NULL THEN NULL ELSE token_ciphertext END,
			inn = $2,
			password_ciphertext = COALESCE($3::bytea, password_ciphertext),
			device_id = CASE WHEN device_id = '' THEN $4 ELSE device_id END,
			service_name = $5, providers = $6, updated_at = now(), updated_by = NULLIF($7, '')::uuid`,
		input.Enabled, input.INN, sealed, nalogo.NewDeviceID(), input.ServiceName, providers, actor.AdminID)
	if err != nil {
		if strings.Contains(err.Error(), "self_employed_settings_enabled_complete") {
			return Settings{}, ErrInvalid
		}
		return Settings{}, err
	}
	if err = panelpg.AppendAudit(ctx, tx, actor, "panel.self_employed.settings_saved", "configuration",
		"self_employed", "settings", map[string]any{
			"enabled": input.Enabled, "passwordChanged": input.Password != "",
			"serviceName": input.ServiceName, "providers": providers,
		}); err != nil {
		return Settings{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Settings{}, err
	}
	return service.Settings(ctx)
}

// credentials is the unsealed configuration the pass needs.
type credentials struct {
	enabled  bool
	inn      string
	password string
	deviceID string
	token    nalogo.Token
}

func (service *Service) credentials(ctx context.Context) (credentials, error) {
	var (
		creds          credentials
		password, toke []byte
	)
	if err := service.pool.QueryRow(ctx, `SELECT enabled, inn, password_ciphertext, token_ciphertext, device_id
		FROM self_employed_settings`).Scan(&creds.enabled, &creds.inn, &password, &toke, &creds.deviceID); err != nil {
		return credentials{}, err
	}
	if len(password) > 0 {
		plain, err := service.open(password, "self_employed_password")
		if err != nil {
			return credentials{}, err
		}
		creds.password = string(plain)
	}
	if len(toke) > 0 {
		if plain, err := service.open(toke, "self_employed_token"); err == nil {
			_ = json.Unmarshal(plain, &creds.token)
		}
	}
	return creds, nil
}

// session returns a client and a valid access token, renewing or
// re-authenticating and caching the result.
func (service *Service) session(ctx context.Context, creds *credentials) (*nalogo.Client, error) {
	client := nalogo.New(service.options.BaseURL, creds.deviceID, service.options.HTTP)
	if creds.token.Valid(time.Now()) {
		return client, nil
	}
	var token nalogo.Token
	var err error
	if creds.token.Refresh != "" {
		token, err = client.Refresh(ctx, creds.token.Refresh)
	}
	if creds.token.Refresh == "" || err != nil {
		token, err = client.Authenticate(ctx, creds.inn, creds.password)
	}
	if err != nil {
		return nil, err
	}
	creds.token = token
	encoded, err := json.Marshal(token)
	if err != nil {
		return nil, err
	}
	sealed, err := service.seal(encoded, "self_employed_token")
	if err != nil {
		return nil, err
	}
	if _, err = service.pool.Exec(ctx, `UPDATE self_employed_settings SET token_ciphertext = $1`, sealed); err != nil {
		return nil, err
	}
	return client, nil
}

// CheckConnection signs in with the stored credentials, so an operator learns
// a wrong password now rather than from a failed receipt.
func (service *Service) CheckConnection(ctx context.Context, actor panelpg.Actor) error {
	creds, err := service.credentials(ctx)
	if err != nil {
		return err
	}
	if creds.inn == "" || creds.password == "" {
		return ErrInvalid
	}
	creds.token = nalogo.Token{}
	_, err = service.session(ctx, &creds)
	outcome := "success"
	if err != nil {
		outcome = "failure"
	}
	_ = panelpg.AppendAudit(ctx, service.pool, actor, "panel.self_employed.connection_checked", "configuration",
		"self_employed", "connection", map[string]any{"outcome": outcome})
	return err
}

// Pass queues receipts for newly settled payments, sends what is due, and
// cancels receipts for refunded orders.
func (service *Service) Pass(ctx context.Context) error {
	creds, err := service.credentials(ctx)
	if err != nil || !creds.enabled {
		return err
	}
	if err = service.enqueue(ctx); err != nil {
		return err
	}
	due, err := service.due(ctx)
	if err != nil {
		return err
	}
	toCancel, err := service.refunded(ctx)
	if err != nil {
		return err
	}
	if len(due) == 0 && len(toCancel) == 0 {
		return nil
	}
	client, err := service.session(ctx, &creds)
	if err != nil {
		// Every due receipt waits for the next interval; nothing is lost and
		// nothing was sent.
		for _, item := range due {
			service.retryLater(ctx, item, "auth_failed")
		}
		return fmt.Errorf("sign in to Moi Nalog: %w", err)
	}
	for _, item := range due {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		service.issue(ctx, client, &creds, item)
	}
	for _, item := range toCancel {
		if err := client.CancelIncome(ctx, creds.token.Access, item.uuid); err != nil {
			service.options.Logger.Warn("self-employed receipt cancellation failed", "error", err)
			continue
		}
		if _, err := service.pool.Exec(ctx, `UPDATE self_employed_receipts SET status = 'cancelled', cancelled_at = now()
			WHERE id = $1::uuid AND status = 'issued'`, item.id); err != nil {
			return err
		}
		service.announce(ctx, item.orderID, item.amount, "cancelled", "")
	}
	return nil
}

// enqueue writes a pending receipt for every eligible settled order.
func (service *Service) enqueue(ctx context.Context) error {
	_, err := service.pool.Exec(ctx, `INSERT INTO self_employed_receipts (order_id, amount_minor, service_name, operation_time)
		SELECT o.id, o.external_minor, s.service_name, o.paid_at
		FROM orders o CROSS JOIN self_employed_settings s
		WHERE s.enabled AND o.currency = 'RUB' AND o.external_minor > 0 AND o.paid_at >= s.enabled_at
		  AND o.state IN ('paid', 'fulfilled', 'partially_refunded')
		  AND EXISTS (SELECT 1 FROM payment_intents p WHERE p.order_id = o.id AND p.status = 'succeeded'
			AND (cardinality(s.providers) = 0 OR p.provider = ANY (s.providers)))
		ON CONFLICT (order_id) DO NOTHING`)
	return err
}

type dueReceipt struct {
	id, orderID, name, status string
	amount                    int64
	attempts                  int
	operation                 time.Time
}

func (service *Service) due(ctx context.Context) ([]dueReceipt, error) {
	rows, err := service.pool.Query(ctx, `SELECT id::text, order_id::text, service_name, status, amount_minor, attempts, operation_time
		FROM self_employed_receipts WHERE status IN ('pending', 'uncertain') AND next_attempt_at <= now()
		ORDER BY next_attempt_at LIMIT $1`, batch)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (dueReceipt, error) {
		var item dueReceipt
		err := row.Scan(&item.id, &item.orderID, &item.name, &item.status, &item.amount, &item.attempts, &item.operation)
		return item, err
	})
}

type issuedReceipt struct {
	id, orderID, uuid string
	amount            int64
}

func (service *Service) refunded(ctx context.Context) ([]issuedReceipt, error) {
	rows, err := service.pool.Query(ctx, `SELECT r.id::text, r.order_id::text, r.receipt_uuid, r.amount_minor
		FROM self_employed_receipts r JOIN orders o ON o.id = r.order_id
		WHERE r.status = 'issued' AND o.state = 'refunded' LIMIT $1`, batch)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (issuedReceipt, error) {
		var item issuedReceipt
		err := row.Scan(&item.id, &item.orderID, &item.uuid, &item.amount)
		return item, err
	})
}

func (service *Service) issue(ctx context.Context, client *nalogo.Client, creds *credentials, item dueReceipt) {
	income := nalogo.Income{Name: item.name, AmountMinor: item.amount, OperationTime: item.operation}
	if item.status == "uncertain" {
		// The last attempt may have created the receipt. Look before sending
		// again; if the lookup itself fails, wait rather than risk a duplicate.
		found, ok, err := client.FindIncome(ctx, creds.token.Access, income)
		if err != nil {
			service.retryLater(ctx, item, "lookup_failed")
			return
		}
		if ok {
			service.issued(ctx, item, creds.inn, found)
			return
		}
	}
	receiptUUID, err := client.CreateIncome(ctx, creds.token.Access, income)
	switch {
	case err == nil:
		service.issued(ctx, item, creds.inn, receiptUUID)
	case errors.Is(err, nalogo.ErrUncertain):
		service.markUncertain(ctx, item)
	case errors.Is(err, nalogo.ErrUnauthorized):
		// A refused token created nothing. Drop it so the next pass signs in.
		_, _ = service.pool.Exec(ctx, `UPDATE self_employed_settings SET token_ciphertext = NULL`)
		creds.token = nalogo.Token{}
		service.retryLater(ctx, item, "unauthorized")
	default:
		service.retryLater(ctx, item, "rejected")
	}
}

func (service *Service) issued(ctx context.Context, item dueReceipt, inn, receiptUUID string) {
	url := nalogo.PrintURL(service.options.BaseURL, inn, receiptUUID)
	if _, err := service.pool.Exec(ctx, `UPDATE self_employed_receipts SET status = 'issued', receipt_uuid = $2,
			receipt_url = $3, issued_at = now(), attempts = attempts + 1, last_error_code = NULL
		WHERE id = $1::uuid AND status IN ('pending', 'uncertain')`, item.id, receiptUUID, url); err != nil {
		service.options.Logger.Error("self-employed receipt bookkeeping failed", "error", err)
		return
	}
	service.announce(ctx, item.orderID, item.amount, "issued", url)
}

func (service *Service) markUncertain(ctx context.Context, item dueReceipt) {
	service.reschedule(ctx, item, "uncertain", "uncertain")
}

func (service *Service) retryLater(ctx context.Context, item dueReceipt, code string) {
	status := item.status
	service.reschedule(ctx, item, status, code)
}

func (service *Service) reschedule(ctx context.Context, item dueReceipt, status, code string) {
	if item.attempts+1 >= maxAttempts {
		status = "failed"
	}
	if _, err := service.pool.Exec(ctx, `UPDATE self_employed_receipts SET status = $2, attempts = attempts + 1,
			last_error_code = $3, next_attempt_at = now() + $4::interval
		WHERE id = $1::uuid AND status IN ('pending', 'uncertain')`, item.id, status, code, retryInterval); err != nil {
		service.options.Logger.Error("self-employed receipt bookkeeping failed", "error", err)
		return
	}
	if status == "failed" {
		service.announce(ctx, item.orderID, item.amount, "failed", "")
	}
}

// announce queues a notice for the operator group's receipts topic. Only the
// order, amount, outcome, and the receipt link are sent: no customer data.
func (service *Service) announce(ctx context.Context, orderID string, amount int64, status, url string) {
	payload, _ := json.Marshal(map[string]any{
		"orderId": orderID, "amountMinor": amount, "currency": "RUB", "status": status, "receiptUrl": url,
	})
	if _, err := service.pool.Exec(ctx, `INSERT INTO operator_notifications (kind, dedupe_key, payload)
		VALUES ('receipt', $1, $2) ON CONFLICT DO NOTHING`, "receipt:"+orderID+":"+status, payload); err != nil {
		service.options.Logger.Warn("self-employed receipt notice was not queued", "error", err)
	}
}

// List reads receipts for the panel, newest first.
func (service *Service) List(ctx context.Context, status string, limit int) ([]Receipt, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := service.pool.Query(ctx, `SELECT id::text, order_id::text, amount_minor, status, attempts,
			COALESCE(receipt_url, ''), COALESCE(last_error_code, ''), operation_time, issued_at
		FROM self_employed_receipts WHERE ($1 = '' OR status = $1) ORDER BY created_at DESC LIMIT $2`, status, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Receipt, error) {
		var receipt Receipt
		err := row.Scan(&receipt.ID, &receipt.OrderID, &receipt.AmountMinor, &receipt.Status, &receipt.Attempts,
			&receipt.ReceiptURL, &receipt.LastErrorCode, &receipt.OperationTime, &receipt.IssuedAt)
		return receipt, err
	})
}

// Retry puts a failed receipt back in the queue.
func (service *Service) Retry(ctx context.Context, receiptID string, actor panelpg.Actor) error {
	tx, err := service.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `UPDATE self_employed_receipts SET status = 'pending', attempts = 0, next_attempt_at = now()
		WHERE id = $1::uuid AND status = 'failed'`, receiptID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		var exists bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM self_employed_receipts WHERE id = $1::uuid)`, receiptID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		return ErrState
	}
	if err = panelpg.AppendAudit(ctx, tx, actor, "panel.self_employed.receipt_retried", "financial",
		"self_employed_receipt", receiptID, nil); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// CustomerReceiptURL is the receipt link for a customer's own order, or ""
// when there is none.
func (service *Service) CustomerReceiptURL(ctx context.Context, customerID, orderID string) (string, error) {
	var url string
	err := service.pool.QueryRow(ctx, `SELECT COALESCE(r.receipt_url, '') FROM self_employed_receipts r
		JOIN orders o ON o.id = r.order_id
		WHERE r.order_id = $1::uuid AND o.user_id = $2::uuid AND r.status = 'issued'`, orderID, customerID).Scan(&url)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return url, err
}

func (service *Service) seal(plaintext []byte, label string) ([]byte, error) {
	nonce := make([]byte, service.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return service.aead.Seal(nonce, nonce, plaintext, []byte(label)), nil
}

func (service *Service) open(sealed []byte, label string) ([]byte, error) {
	size := service.aead.NonceSize()
	if len(sealed) <= size {
		return nil, errors.New("sealed value is truncated")
	}
	return service.aead.Open(nil, sealed[:size], sealed[size:], []byte(label))
}
