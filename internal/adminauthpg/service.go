// Package adminauthpg is the PostgreSQL adapter for operator authentication,
// role grants, sessions, and the audit trail.
//
// The domain rules it applies live in internal/adminauth and internal/rbac.
// This package owns only persistence, transaction boundaries, and the ordering
// guarantees that make each operation idempotent and auditable: every state
// change and the audit event describing it are written in one transaction, so
// the trail can never disagree with what actually happened.
package adminauthpg

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"strings"
	"time"

	"crypto/rand"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/adminauth"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/rbac"
)

// Errors returned to transport. They are deliberately coarse: an authentication
// failure never reveals whether the account existed, whether the password was
// wrong, or whether the account was locked, because each distinction is a probe
// an attacker can use to enumerate operators.
var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrAccountLocked      = errors.New("account is temporarily locked")
	ErrAccountDisabled    = errors.New("account is not active")
	ErrSessionInvalid     = errors.New("session is not valid")
	ErrChallengeRequired  = errors.New("second factor is required")
	ErrNotFound           = errors.New("record not found")
	ErrConflict           = errors.New("record already exists")
	ErrForbidden          = errors.New("operation is not permitted")
	ErrLastOwner          = errors.New("the installation must keep at least one active owner")
	ErrBootstrapClosed    = errors.New("bootstrap is no longer available")
)

// Service is the operator identity adapter.
type Service struct {
	pool     *pgxpool.Pool
	secrets  cipher.AEAD
	sessions adminauth.SessionPolicy
	lockout  adminauth.LockoutPolicy
	password adminauth.PasswordParams
	clock    func() time.Time

	// decoyHash is verified against when a sign-in names an address that does
	// not exist. Without it, a missing account would answer measurably faster
	// than a wrong password and turn the login endpoint into an operator
	// enumeration oracle. It is produced with the live parameters so the work
	// performed matches a real verification.
	decoyHash string
}

// Options configures a Service. Zero values fall back to the domain defaults.
type Options struct {
	SessionPolicy  adminauth.SessionPolicy
	LockoutPolicy  adminauth.LockoutPolicy
	PasswordParams adminauth.PasswordParams
	Clock          func() time.Time
}

// New builds the adapter. The encryption key is the same 32-byte
// APP_DATA_ENCRYPTION_KEY that protects customer contact values; it seals TOTP
// secrets and OIDC client secrets so a database dump alone yields neither.
func New(pool *pgxpool.Pool, encryptionKey []byte, options Options) (*Service, error) {
	if pool == nil {
		return nil, errors.New("a database pool is required")
	}
	if len(encryptionKey) != 32 {
		return nil, errors.New("admin secret encryption key must be 32 bytes")
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
		pool:     pool,
		secrets:  aead,
		sessions: options.SessionPolicy,
		lockout:  options.LockoutPolicy,
		password: options.PasswordParams,
		clock:    options.Clock,
	}
	if service.sessions == (adminauth.SessionPolicy{}) {
		service.sessions = adminauth.DefaultSessionPolicy
	}
	if service.lockout == (adminauth.LockoutPolicy{}) {
		service.lockout = adminauth.DefaultLockoutPolicy
	}
	if service.password == (adminauth.PasswordParams{}) {
		service.password = adminauth.DefaultPasswordParams
	}
	if service.clock == nil {
		service.clock = time.Now
	}

	// Built once at construction so the cost is paid at startup rather than on
	// the first unauthenticated request.
	decoy := make([]byte, 32)
	if _, err = rand.Read(decoy); err != nil {
		return nil, err
	}
	service.decoyHash, err = adminauth.HashPassword(
		base64.RawURLEncoding.EncodeToString(decoy), service.password,
	)
	if err != nil {
		return nil, err
	}
	return service, nil
}

// SessionPolicy exposes the configured lifetimes so transport can set cookie
// expiries that match the server's own view of the session.
func (service *Service) SessionPolicy() adminauth.SessionPolicy { return service.sessions }

// Account is an operator account as the API presents it, with secrets removed.
type Account struct {
	ID           string
	Email        string
	DisplayName  string
	Status       string
	Locale       string
	Timezone     string
	Roles        []rbac.Role
	TOTPEnabled  bool
	LastLoginAt  *time.Time
	CreatedAt    time.Time
	PasswordSet  bool
	LockedUntil  *time.Time
	FailedLogins int
}

// Principal is a resolved, fully authenticated request identity.
type Principal struct {
	Account   Account
	Grant     rbac.Grant
	SessionID string
	// CSRFToken is the value the browser must echo on every unsafe request.
	CSRFToken string
	// RotatedToken is non-empty when the session token was rotated while
	// resolving this request; transport must reissue the cookie.
	RotatedToken string
	ExpiresAt    time.Time
}

// AuditEntry describes one audit event to append.
type AuditEntry struct {
	ActorType  string
	ActorID    string
	Action     string
	Category   string
	Outcome    string
	TargetType string
	TargetID   string
	Reason     string
	RequestID  string
	Metadata   map[string]any
}

// RequestContext carries the transport details recorded against a session or an
// audit event.
type RequestContext struct {
	IP        *netip.Addr
	UserAgent string
	RequestID string
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

func (service *Service) now() time.Time { return service.clock().UTC() }

// inTx runs body inside a transaction, rolling back on any error. Read
// Committed is sufficient throughout this package: every operation that must be
// exclusive takes its own row lock or relies on a unique constraint, so there
// is no read-skew window to protect against.
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

// appendAudit writes one audit event. It always runs inside the caller's
// transaction so the trail commits or rolls back with the change it describes.
func appendAudit(ctx context.Context, queries *dbgen.Queries, entry AuditEntry) error {
	metadata := []byte("{}")
	if len(entry.Metadata) > 0 {
		encoded, err := json.Marshal(entry.Metadata)
		if err != nil {
			return err
		}
		metadata = encoded
	}
	if entry.Outcome == "" {
		entry.Outcome = "success"
	}
	_, err := queries.InsertAuditEvent(ctx, dbgen.InsertAuditEventParams{
		ActorType:  entry.ActorType,
		ActorID:    optionalText(entry.ActorID),
		Action:     entry.Action,
		Category:   entry.Category,
		Outcome:    entry.Outcome,
		TargetType: entry.TargetType,
		TargetID:   entry.TargetID,
		Reason:     optionalText(entry.Reason),
		RequestID:  optionalText(entry.RequestID),
		Metadata:   metadata,
	})
	return err
}

// NormalizeEmail produces the lookup key for an address. Only case and
// surrounding whitespace are folded: stripping dots or plus-tags would merge
// addresses that some providers treat as genuinely distinct mailboxes.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// sealSecret encrypts a secret for storage, prefixing the nonce.
func (service *Service) sealSecret(plaintext, associated string) ([]byte, error) {
	nonce := make([]byte, service.secrets.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return service.secrets.Seal(nonce, nonce, []byte(plaintext), []byte(associated)), nil
}

// openSecret reverses sealSecret.
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

func (service *Service) accountFrom(row dbgen.AdminUser, roles []rbac.Role) Account {
	account := Account{
		ID:           uuidString(row.ID),
		Email:        row.Email,
		DisplayName:  row.DisplayName,
		Status:       row.Status,
		Locale:       row.Locale,
		Timezone:     row.Timezone,
		Roles:        roles,
		TOTPEnabled:  row.TotpConfirmedAt.Valid,
		CreatedAt:    row.CreatedAt.Time,
		PasswordSet:  row.PasswordHash.Valid,
		FailedLogins: int(row.FailedLoginCount),
	}
	if row.LastLoginAt.Valid {
		at := row.LastLoginAt.Time
		account.LastLoginAt = &at
	}
	if row.LockedUntil.Valid {
		at := row.LockedUntil.Time
		account.LockedUntil = &at
	}
	return account
}

// loadRoles reads an operator's role grants, skipping any value the compiled-in
// catalogue no longer knows.
func loadRoles(ctx context.Context, queries *dbgen.Queries, adminUserID pgtype.UUID) ([]rbac.Role, error) {
	values, err := queries.ListAdminRoles(ctx, adminUserID)
	if err != nil {
		return nil, err
	}
	roles := make([]rbac.Role, 0, len(values))
	for _, value := range values {
		role, parseErr := rbac.ParseRole(value)
		if parseErr != nil {
			continue
		}
		roles = append(roles, role)
	}
	return roles, nil
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

func timestamp(at time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: at.UTC(), Valid: true}
}

func optionalTimestamp(at *time.Time) pgtype.Timestamptz {
	if at == nil {
		return pgtype.Timestamptz{}
	}
	return timestamp(*at)
}

// timePointer converts a nullable column into a pointer the domain policies
// take, where nil means "no deadline" rather than the zero time.
func timePointer(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	at := value.Time.UTC()
	return &at
}
