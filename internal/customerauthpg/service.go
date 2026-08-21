// Package customerauthpg is the PostgreSQL adapter for customer sign-in,
// browser sessions, linked identities, and the customer-visible security log.
//
// The rules it applies live in internal/customerauth and internal/customer.
// This package owns persistence, transaction boundaries, and the ordering that
// makes each operation idempotent: a session and the security event announcing
// it are written in one transaction, so the log a customer reads can never
// disagree with what actually happened to their account.
//
// It is deliberately separate from internal/adminauthpg. The two surfaces share
// the session-token construction (internal/websession) and nothing else: an
// operator account and a customer are different tables, different lifetimes,
// and different consequences when one is taken over.
package customerauthpg

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/customerauth"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// Errors returned to transport.
//
// They are coarse where being specific would help an attacker: a sign-in that
// fails never reveals whether the identity existed, and an unusable magic link
// never reveals whether it was unknown, spent, or expired.
var (
	ErrSignInRejected  = errors.New("sign-in could not be completed")
	ErrSessionInvalid  = errors.New("session is not valid")
	ErrAccountInactive = errors.New("account is not active")
	ErrNotFound        = errors.New("record not found")
	ErrOIDCDisabled    = errors.New("no such sign-in provider is enabled")
	ErrTelegramUnset   = errors.New("telegram sign-in is not configured")
)

const (
	// secretAssociatedData binds a sealed OIDC client secret to its purpose, so
	// a ciphertext lifted from this column cannot be opened as any other field
	// that shares the encryption key.
	secretAssociatedData = "customer.oidc.client_secret"
	// flowAssociatedData does the same for the short-lived sign-in flow cookie.
	flowAssociatedData = "customer.flow"

	// SecurityEventRetention is how long the customer-visible log is kept.
	SecurityEventRetention = 180 * 24 * time.Hour
	// SessionRetention is how long a dead session row survives past its absolute
	// expiry, so a recent sign-out can still be explained in that log.
	SessionRetention = 30 * 24 * time.Hour
)

// Service is the customer identity adapter.
type Service struct {
	pool     *pgxpool.Pool
	secrets  cipher.AEAD
	sessions customerauth.SessionPolicy
	clock    func() time.Time

	// botToken verifies Telegram login payloads. An installation that has not
	// configured a bot simply cannot offer Telegram sign-in, which is reported
	// as the route being unavailable rather than as a failed attempt.
	botToken string

	// magicLinkEnabled is the operator switch for the fallback route. It is read
	// at construction from installation settings and refreshed by the settings
	// writer, so a request never pays for a settings lookup.
	magicLinkEnabled bool

	// publicURL is the installation's customer-facing origin. It is needed to
	// build a magic link the bot can send, which is the one place this package
	// has to know where it lives.
	publicURL string

	httpClient *http.Client

	// telegramAPI overrides the bot API origin. Empty means Telegram's own; it
	// exists so a test can stand in for getMe.
	telegramAPI string

	// botName caches the bot's own @name for the login widget. See telegram.go.
	botName telegramBotUsername
}

// Options configures a Service. Zero values fall back to the domain defaults.
type Options struct {
	SessionPolicy    customerauth.SessionPolicy
	Clock            func() time.Time
	TelegramBotToken string
	MagicLinkEnabled bool
	PublicURL        string
	HTTPClient       *http.Client
	// TelegramAPIURL replaces https://api.telegram.org. Tests only.
	TelegramAPIURL string
}

// New builds the adapter. The encryption key is the same 32-byte
// APP_DATA_ENCRYPTION_KEY that protects customer contact values and operator
// TOTP secrets; here it seals OIDC client secrets and the sign-in flow cookie.
func New(pool *pgxpool.Pool, encryptionKey []byte, options Options) (*Service, error) {
	if pool == nil {
		return nil, errors.New("a database pool is required")
	}
	if len(encryptionKey) != 32 {
		return nil, errors.New("customer secret encryption key must be 32 bytes")
	}
	block, err := aes.NewCipher(encryptionKey)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	service := &Service{
		pool:             pool,
		secrets:          aead,
		sessions:         options.SessionPolicy,
		clock:            options.Clock,
		botToken:         strings.TrimSpace(options.TelegramBotToken),
		magicLinkEnabled: options.MagicLinkEnabled,
		publicURL:        strings.TrimRight(strings.TrimSpace(options.PublicURL), "/"),
		httpClient:       options.HTTPClient,
		telegramAPI:      strings.TrimSpace(options.TelegramAPIURL),
	}
	if service.sessions == (customerauth.SessionPolicy{}) {
		service.sessions = customerauth.DefaultSessionPolicy
	}
	if service.clock == nil {
		service.clock = time.Now
	}
	if service.httpClient == nil {
		service.httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	return service, nil
}

// SessionPolicy exposes the configured lifetimes so transport can set cookie
// expiries that match the server's own view of the session.
func (service *Service) SessionPolicy() customerauth.SessionPolicy { return service.sessions }

// TelegramConfigured reports whether the Telegram route can be offered.
func (service *Service) TelegramConfigured() bool { return service.botToken != "" }

// MagicLinkEnabled reports whether the fallback route is switched on.
func (service *Service) MagicLinkEnabled() bool { return service.magicLinkEnabled }

// SetMagicLinkEnabled applies an operator's change to the switch without a
// restart. The settings writer calls it after a successful save.
func (service *Service) SetMagicLinkEnabled(enabled bool) { service.magicLinkEnabled = enabled }

// Customer is the account behind a session, with nothing secret in it.
type Customer struct {
	ID       string
	Status   string
	Locale   string
	Timezone string
}

// Active reports whether the account may use the panel at all.
func (customer Customer) Active() bool { return customer.Status == "active" }

// Principal is a resolved, authenticated request identity.
type Principal struct {
	Customer     Customer
	SessionID    string
	AuthMethod   string
	AuthProvider string
	// CSRFToken is the value the browser must echo on every unsafe request.
	CSRFToken string
	// RotatedToken is non-empty when the session token was rotated while
	// resolving this request; transport must reissue the cookie.
	RotatedToken string
	ExpiresAt    time.Time
	// ReauthenticationRequired reports that this session is too old to perform a
	// sensitive action. It is computed on every request rather than stored, so
	// the answer never goes stale.
	ReauthenticationRequired bool
}

// RequestContext carries the transport details recorded against a session or a
// security event.
type RequestContext struct {
	IP        *netip.Addr
	UserAgent string
	RequestID string
}

// SignInResult is a freshly established session.
type SignInResult struct {
	Token     string
	ExpiresAt time.Time
	Customer  Customer
	SessionID string
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

func (service *Service) now() time.Time { return service.clock().UTC() }

// inTx runs body inside a transaction, rolling back on any error. Read
// Committed is sufficient throughout: every operation that must be exclusive
// either takes its own row lock or relies on a unique constraint.
func (service *Service) inTx(ctx context.Context, body func(*dbgen.Queries) error) error {
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = body(dbgen.New(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// sealSecret encrypts a secret for storage, prefixing the nonce.
func (service *Service) sealSecret(plaintext, associated string) ([]byte, error) {
	nonce := make([]byte, service.secrets.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return service.secrets.Seal(nonce, nonce, []byte(plaintext), []byte(associated)), nil
}

func (service *Service) openSecret(ciphertext []byte, associated string) (string, error) {
	size := service.secrets.NonceSize()
	if len(ciphertext) < size {
		return "", errors.New("stored secret is truncated")
	}
	plaintext, err := service.secrets.Open(nil, ciphertext[:size], ciphertext[size:], []byte(associated))
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

// SealFlowState encrypts the sign-in flow state — the anti-forgery value, the
// PKCE verifier, and the nonce — for a cookie the browser holds but can neither
// read nor forge.
func (service *Service) SealFlowState(plaintext []byte) ([]byte, error) {
	return service.sealSecret(string(plaintext), flowAssociatedData)
}

// OpenFlowState reverses SealFlowState.
func (service *Service) OpenFlowState(ciphertext []byte) ([]byte, error) {
	opened, err := service.openSecret(ciphertext, flowAssociatedData)
	if err != nil {
		return nil, err
	}
	return []byte(opened), nil
}

func customerFromUser(row dbgen.User) Customer {
	return Customer{
		ID: uuidString(row.ID), Status: row.Status, Locale: row.Locale, Timezone: row.Timezone,
	}
}

func parseUUID(value string) (pgtype.UUID, error) {
	var id pgtype.UUID
	if err := id.Scan(strings.TrimSpace(value)); err != nil {
		return pgtype.UUID{}, fmt.Errorf("%w: malformed identifier", ErrNotFound)
	}
	return id, nil
}

func uuidString(id pgtype.UUID) string {
	if !id.Valid {
		return ""
	}
	value, err := id.Value()
	if err != nil {
		return ""
	}
	text, _ := value.(string)
	return text
}

func optionalText(value string) pgtype.Text {
	if strings.TrimSpace(value) == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: value, Valid: true}
}

// interval converts a Go duration into the pgtype the queries take, so an
// expiry is computed from the database's clock rather than this process's.
func interval(duration time.Duration) pgtype.Interval {
	return pgtype.Interval{Microseconds: duration.Microseconds(), Valid: true}
}

func timePointer(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	at := value.Time.UTC()
	return &at
}

func encodeMetadata(metadata map[string]any) ([]byte, error) {
	if len(metadata) == 0 {
		return []byte("{}"), nil
	}
	return json.Marshal(metadata)
}

// decodeMetadata reads a stored metadata document back. A row that cannot be
// decoded yields an empty map rather than an error: the metadata is context on
// an event, and losing it must not make the event itself unreadable.
func decodeMetadata(raw []byte) map[string]any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	decoded := map[string]any{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return map[string]any{}
	}
	return decoded
}
