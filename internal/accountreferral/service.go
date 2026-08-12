// Package accountreferral is the customer web panel's referral, loyalty, and
// personal-data surface.
//
// Referral and loyalty are read models over records the reward and evaluation
// workers already wrote. Nothing here grants a reward or promotes a tier: doing
// that on a page load would make a customer's balance depend on who happened to
// open which screen.
//
// The personal-data half carries the two requests a customer may make about
// their own account. An export is produced synchronously from the records this
// installation already holds, because a customer's own data is small and a
// queued export would need a delivery channel, a retention window, and a link
// that authenticates on its own — three new disclosure decisions to answer a
// question that fits in one response. A deletion request is recorded as a
// lifecycle event with the customer as its actor, and nothing here executes it:
// deletion runs through the retention workflow an operator already governs, so
// an irreversible action never happens on the strength of one browser session.
package accountreferral

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	// ErrNotFound reports a record that does not belong to the calling customer.
	// It is the same error for "does not exist" and "is not yours", so an
	// identifier cannot be probed for existence.
	ErrNotFound = errors.New("not found")
	// ErrInvalidInput reports a value the customer supplied that this package
	// refused. It wraps its own message so the panel can explain why without
	// restating the rule.
	ErrInvalidInput = errors.New("invalid input")
	// ErrContactConflict reports a contact channel that is already registered
	// somewhere this customer cannot see. It is deliberately the only thing the
	// caller learns: telling somebody that an address belongs to another account
	// turns the panel into an account-existence oracle, so the remedy is a
	// support handoff rather than a resolution here.
	ErrContactConflict = errors.New("contact channel is not available")
	// ErrDeletionPending reports a second deletion request while one is open.
	// Repeating the request changes nothing and would only add noise to the
	// record an operator reads before acting on it.
	ErrDeletionPending = errors.New("a deletion request is already pending")
	// ErrNoDeletionPending reports a cancellation with nothing to cancel.
	ErrNoDeletionPending = errors.New("no deletion request is pending")
	// ErrContactsUnavailable reports that no contact encryption key was supplied,
	// so a contact value could be neither sealed on write nor read back. The
	// panel says the section is unavailable rather than storing an address it
	// could never show the customer again.
	ErrContactsUnavailable = errors.New("contact storage is not configured")
)

// Service is the customer referral, loyalty, and privacy adapter.
type Service struct {
	pool   *pgxpool.Pool
	public string
	logger *slog.Logger
	clock  func() time.Time

	// contactAEAD and fingerprintKey are the same construction customerpg uses,
	// and deliberately so: a contact added from the panel has to collide with one
	// added from the operator API under `UNIQUE (kind, value_fingerprint)`, which
	// only happens when both surfaces derive the fingerprint the same way. A
	// second scheme would silently let one address exist twice.
	contactAEAD    cipher.AEAD
	fingerprintKey []byte
}

// Options configures a Service.
type Options struct {
	// PublicURL is the base a referral link is built from. Without one the panel
	// still shows the code and reports that no link can be built, which is
	// better than handing out a link to nowhere.
	PublicURL string
	// EncryptionKey is the installation's 32-byte data key. It is optional here
	// and only because the contact section can honestly report itself
	// unavailable: every other route works without it.
	EncryptionKey []byte
	Logger        *slog.Logger
	Clock         func() time.Time
}

// New builds the adapter.
func New(pool *pgxpool.Pool, options Options) (*Service, error) {
	if pool == nil {
		return nil, errors.New("a database pool is required")
	}
	service := &Service{
		pool:   pool,
		public: strings.TrimRight(strings.TrimSpace(options.PublicURL), "/"),
		logger: options.Logger, clock: options.Clock,
	}
	if service.logger == nil {
		service.logger = slog.Default()
	}
	if service.clock == nil {
		service.clock = time.Now
	}
	// A key of the wrong length is a configuration error rather than a reason to
	// degrade: silently running without contact storage because somebody typed a
	// 31-byte key would hide the mistake until a customer noticed.
	if length := len(options.EncryptionKey); length > 0 {
		if length != 32 {
			return nil, errors.New("the contact encryption key must be 32 bytes")
		}
		block, err := aes.NewCipher(options.EncryptionKey)
		if err != nil {
			return nil, err
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, err
		}
		service.contactAEAD = aead
		service.fingerprintKey = append([]byte(nil), options.EncryptionKey...)
	}
	return service, nil
}

func (service *Service) now() time.Time { return service.clock().UTC() }

// ContactsAvailable reports whether contact values can be sealed and read back,
// so the panel can hide a section it cannot use rather than offering a control
// that always fails.
func (service *Service) ContactsAvailable() bool { return service.contactAEAD != nil }

// ---------------------------------------------------------------------------
// Pagination
// ---------------------------------------------------------------------------

const (
	// defaultPageSize is what a list returns when the caller asks for nothing.
	defaultPageSize = 25
	// maxPageSize bounds what one request can cost.
	maxPageSize = 100
)

// cursor is a keyset position, encoded as "<rfc3339nano>|<uuid>".
//
// It carries no offset, so a page boundary stays correct while rows are written
// underneath it, and both halves are encoded because the (timestamp, id)
// comparison in the query needs both to be resolvable. The identifier half is
// always a row the calling customer already owns — a cursor that carried
// somebody else's identifier would leak one just by being handed back.
type cursor struct {
	At time.Time
	ID string
}

func encodeCursor(at time.Time, id string) string {
	if id == "" {
		return ""
	}
	return at.UTC().Format(time.RFC3339Nano) + "|" + id
}

// decodeCursor parses a cursor. A malformed value yields the zero cursor rather
// than an error: the right response to an unreadable cursor is the first page,
// not a failed request.
func decodeCursor(value string) cursor {
	parts := strings.SplitN(strings.TrimSpace(value), "|", 2)
	if len(parts) != 2 {
		return cursor{}
	}
	at, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return cursor{}
	}
	if _, err := parseUUID(parts[1]); err != nil {
		return cursor{}
	}
	return cursor{At: at.UTC(), ID: parts[1]}
}

func (position cursor) timestamp() pgtype.Timestamptz {
	if position.ID == "" {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: position.At, Valid: true}
}

func (position cursor) uuid() pgtype.UUID {
	if position.ID == "" {
		return pgtype.UUID{}
	}
	id, err := parseUUID(position.ID)
	if err != nil {
		return pgtype.UUID{}
	}
	return id
}

func pageSize(requested int) int {
	switch {
	case requested <= 0:
		return defaultPageSize
	case requested > maxPageSize:
		return maxPageSize
	default:
		return requested
	}
}

// ---------------------------------------------------------------------------
// Value helpers
// ---------------------------------------------------------------------------

func parseUUID(value string) (pgtype.UUID, error) {
	var id pgtype.UUID
	if err := id.Scan(value); err != nil || !id.Valid {
		return pgtype.UUID{}, fmt.Errorf("%w: not an identifier", ErrInvalidInput)
	}
	return id, nil
}

func uuidString(value pgtype.UUID) string {
	if !value.Valid {
		return ""
	}
	bytes := value.Bytes
	return fmt.Sprintf(
		"%08x-%04x-%04x-%04x-%012x",
		bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:16],
	)
}

func optionalText(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: value != ""}
}

// timePointer converts a nullable timestamp into a pointer, normalized to UTC so
// nothing downstream has to guess which zone a value arrived in.
func timePointer(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	moment := value.Time.UTC()
	return &moment
}

// invalid wraps a refusal so the caller's error mapping stays a single switch.
func invalid(format string, arguments ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidInput, fmt.Sprintf(format, arguments...))
}

// ---------------------------------------------------------------------------
// Contact sealing
// ---------------------------------------------------------------------------

// seal encrypts a contact value and derives its fingerprint.
//
// The fingerprint is a keyed MAC rather than a plain digest so a stolen database
// cannot be tested for a known address by hashing candidates, and the `kind` is
// bound in as additional data so a value sealed as an email cannot be replayed
// as a phone number.
func (service *Service) seal(kind, value string) (ciphertext, fingerprint []byte, err error) {
	if service.contactAEAD == nil {
		return nil, nil, ErrContactsUnavailable
	}
	mac := hmac.New(sha256.New, service.fingerprintKey)
	_, _ = mac.Write([]byte(strings.ToLower(value)))
	fingerprint = mac.Sum(nil)

	nonce := make([]byte, service.contactAEAD.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, err
	}
	return service.contactAEAD.Seal(nonce, nonce, []byte(value), []byte(kind)), fingerprint, nil
}

// open reverses seal. A value that cannot be opened returns empty rather than an
// error: one unreadable row — written under a rotated key, say — must not make
// the whole list fail.
func (service *Service) open(kind string, ciphertext []byte) string {
	if service.contactAEAD == nil || len(ciphertext) <= service.contactAEAD.NonceSize() {
		return ""
	}
	size := service.contactAEAD.NonceSize()
	plaintext, err := service.contactAEAD.Open(
		nil, ciphertext[:size], ciphertext[size:], []byte(kind),
	)
	if err != nil {
		return ""
	}
	return string(plaintext)
}

// ---------------------------------------------------------------------------
// Audit
// ---------------------------------------------------------------------------

// recordAudit appends one row an operator can be held to.
//
// It writes `audit_events` rather than `customer_security_events` for a reason
// worth stating: the customer-facing log has a closed `event` vocabulary that a
// disclosure action is not part of, and widening it would be a migration. The
// audit record is the one that matters here anyway — an export is a disclosure
// the installation must be able to account for, and `audit_events` already
// admits `actor_type = 'customer'`.
//
// Metadata carries counts and identifiers of the customer's own records only.
// Never a contact value, never an export payload.
func (service *Service) recordAudit(
	ctx context.Context, executor executor,
	customerID, action, requestID string, metadata map[string]any,
) error {
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	_, err = executor.Exec(ctx, `INSERT INTO audit_events
		(actor_type, actor_id, action, target_type, target_id, request_id, metadata)
		VALUES ('customer', $1, $2, 'customer', $1, $3, $4::jsonb)`,
		customerID, action, optionalText(requestID), encoded)
	return err
}

// executor is the narrow slice of pgx both a pool and a transaction satisfy, so
// a helper can be reused inside and outside a transaction without either caller
// having to know which it holds.
type executor interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, arguments ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, arguments ...any) pgx.Row
}

// RequestContext is the transport detail a recorded action carries.
//
// It exists so an audit or lifecycle row can name the request that produced it,
// which is what turns "somebody asked for deletion" into something an operator
// can trace. It never carries a contact value or a payload.
type RequestContext struct {
	IP        string
	UserAgent string
	RequestID string
}
