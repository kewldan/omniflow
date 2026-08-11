// Package panelpg is the PostgreSQL adapter behind the operator panel's
// day-to-day operations: the dashboard, customer and subscription
// administration, catalogue and promotions, finance, recurring charges, gifts,
// the digital-goods shop, risk review, fulfillment diagnostics, and bulk
// actions.
//
// It is separate from `internal/adminauthpg`, which owns who an operator is and
// what they may do. This package assumes that question is already answered: it
// receives an actor identifier from transport and never decides authorization
// for itself, because `internal/rbac` is the only place allowed to.
//
// Two rules hold throughout.
//
// A state change and the audit event describing it are written in one
// transaction. The trail therefore cannot disagree with what happened, and a
// rolled-back mutation leaves no record claiming it succeeded.
//
// Nothing here punishes a customer automatically. Risk signals and blocklist
// matches produce rows an operator reviews; the adverse action, when there is
// one, is an ordinary customer or finance mutation with its own permission,
// its own reason, and its own audit event.
package panelpg

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// Errors returned to transport.
var (
	ErrNotFound  = errors.New("record not found")
	ErrConflict  = errors.New("record already exists")
	ErrRejected  = errors.New("operation is not allowed in the current state")
	ErrValidaton = errors.New("input is not valid")
)

// DefaultPageSize is used when transport asks for no particular page size.
const DefaultPageSize int32 = 25

// MaxPageSize bounds what a caller may request, so a single panel request
// cannot ask the database for an unbounded result set.
const MaxPageSize int32 = 200

// Service is the operations adapter.
type Service struct {
	pool    *pgxpool.Pool
	secrets cipher.AEAD
	clock   func() time.Time
}

// Options configures the Service. A zero clock uses the wall clock.
type Options struct {
	Clock func() time.Time
}

// New builds the adapter.
//
// The encryption key is the same 32-byte APP_DATA_ENCRYPTION_KEY that protects
// customer contact values and operator TOTP secrets. Here it seals payment
// provider credentials, digital-goods provider tokens, and blocklist
// authorization headers, so a database dump alone yields none of them.
func New(pool *pgxpool.Pool, encryptionKey []byte, options Options) (*Service, error) {
	if pool == nil {
		return nil, errors.New("a database pool is required")
	}
	if len(encryptionKey) != 32 {
		return nil, errors.New("panel secret encryption key must be 32 bytes")
	}
	block, err := aes.NewCipher(encryptionKey)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	clock := options.Clock
	if clock == nil {
		clock = time.Now
	}
	return &Service{pool: pool, secrets: aead, clock: clock}, nil
}

// Actor identifies the operator making a change, for the audit trail.
//
// It is a value rather than a session, because this package must be usable by a
// worker acting on its own behalf as well as by a panel request. A worker
// passes an empty AdminID and the actor type "system".
type Actor struct {
	AdminID   string
	Type      string
	RequestID string
	Reason    string
}

// audit builds the entry for an actor, defaulting the actor type so a caller
// that forgets it still produces a valid, attributable row.
func (actor Actor) audit(action, category, targetType, targetID string, metadata map[string]any) auditEntry {
	actorType := actor.Type
	if actorType == "" {
		actorType = "admin"
	}
	if actor.AdminID == "" && actorType == "admin" {
		actorType = "system"
	}
	return auditEntry{
		ActorType: actorType, ActorID: actor.AdminID,
		Action: action, Category: category, Outcome: "success",
		TargetType: targetType, TargetID: targetID,
		Reason: actor.Reason, RequestID: actor.RequestID, Metadata: metadata,
	}
}

type auditEntry struct {
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

// appendAudit writes one audit event inside the caller's transaction, so the
// trail commits or rolls back with the change it describes.
func appendAudit(ctx context.Context, queries *dbgen.Queries, entry auditEntry) error {
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

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

func (service *Service) now() time.Time { return service.clock().UTC() }

// queries returns a non-transactional handle for read paths.
func (service *Service) queries() *dbgen.Queries { return dbgen.New(service.pool) }

// inTx runs body inside a transaction, rolling back on any error.
//
// Read Committed throughout: every operation that must be exclusive takes its
// own row lock or leans on a unique constraint, so there is no read-skew window
// to protect against.
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
//
// The associated data binds a ciphertext to the field it belongs to, so a
// sealed provider credential cannot be moved into the blocklist authorization
// column and decrypted there.
func (service *Service) sealSecret(plaintext, associated string) ([]byte, error) {
	if strings.TrimSpace(plaintext) == "" {
		return nil, nil
	}
	nonce := make([]byte, service.secrets.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return service.secrets.Seal(nonce, nonce, []byte(plaintext), []byte(associated)), nil
}

// OpenSecret reverses sealSecret. It is exported because the adapters that
// actually call a provider live outside this package and need the credential
// the operator configured.
func (service *Service) OpenSecret(ciphertext []byte, associated string) (string, error) {
	if len(ciphertext) == 0 {
		return "", nil
	}
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

// Associated-data labels for the sealed columns this package writes.
const (
	SecretPaymentProvider = "panel.payment_provider"
	SecretPaymentWebhook  = "panel.payment_webhook"
	SecretGoodsProvider   = "panel.goods_provider"
	SecretBlocklistAuth   = "panel.blocklist_auth"
)

// pageSize clamps what transport asked for into the supported range.
func pageSize(requested int32) int32 {
	switch {
	case requested <= 0:
		return DefaultPageSize
	case requested > MaxPageSize:
		return MaxPageSize
	default:
		return requested
	}
}

// Cursor encodes a keyset position as "<rfc3339nano>|<uuid>".
//
// It is opaque to the panel and carries no offset, so a page boundary stays
// correct while rows are inserted underneath it. Encoding both halves is what
// makes the (timestamp, id) comparison in every keyset query resolvable.
type Cursor struct {
	At time.Time
	ID string
}

// EncodeCursor renders the position after the last row of a page.
func EncodeCursor(at time.Time, id string) string {
	if id == "" {
		return ""
	}
	return at.UTC().Format(time.RFC3339Nano) + "|" + id
}

// DecodeCursor parses a cursor. A malformed value yields the zero cursor rather
// than an error: the correct response to an unreadable cursor is the first
// page, not a failed request.
func DecodeCursor(value string) Cursor {
	parts := strings.SplitN(strings.TrimSpace(value), "|", 2)
	if len(parts) != 2 {
		return Cursor{}
	}
	at, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return Cursor{}
	}
	id, err := parseUUID(parts[1])
	if err != nil || !id.Valid {
		return Cursor{}
	}
	return Cursor{At: at.UTC(), ID: parts[1]}
}

// timestampOf converts a cursor's instant into the nullable parameter a keyset
// query takes, where null means "start at the beginning".
func (cursor Cursor) timestamp() pgtype.Timestamptz {
	if cursor.ID == "" {
		return pgtype.Timestamptz{}
	}
	return timestamp(cursor.At)
}

func (cursor Cursor) uuid() pgtype.UUID {
	if cursor.ID == "" {
		return pgtype.UUID{}
	}
	id, err := parseUUID(cursor.ID)
	if err != nil {
		return pgtype.UUID{}
	}
	return id
}

// The pgtype conversion helpers below are deliberately duplicated from
// `internal/adminauthpg` rather than shared. They are four lines each, and a
// shared "pg helpers" package between two adapters is the kind of dependency
// that grows until it owns behaviour neither adapter meant to share.

func parseUUID(value string) (pgtype.UUID, error) {
	var id pgtype.UUID
	if err := id.Scan(strings.TrimSpace(value)); err != nil {
		return pgtype.UUID{}, fmt.Errorf("%w: malformed identifier", ErrNotFound)
	}
	return id, nil
}

// optionalUUID parses an identifier used as a filter, where an empty string
// means "no filter" rather than an error.
func optionalUUID(value string) pgtype.UUID {
	if strings.TrimSpace(value) == "" {
		return pgtype.UUID{}
	}
	id, err := parseUUID(value)
	if err != nil {
		return pgtype.UUID{}
	}
	return id
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

func uuidStrings(ids []pgtype.UUID) []string {
	values := make([]string, 0, len(ids))
	for _, id := range ids {
		if text := uuidString(id); text != "" {
			values = append(values, text)
		}
	}
	return values
}

func optionalText(value string) pgtype.Text {
	if strings.TrimSpace(value) == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: value, Valid: true}
}

func textValue(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return value.String
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

func timePointer(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	at := value.Time.UTC()
	return &at
}

func timeValue(value pgtype.Timestamptz) time.Time {
	if !value.Valid {
		return time.Time{}
	}
	return value.Time.UTC()
}

func optionalInt8(value *int64) pgtype.Int8 {
	if value == nil {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: *value, Valid: true}
}

func int8Pointer(value pgtype.Int8) *int64 {
	if !value.Valid {
		return nil
	}
	amount := value.Int64
	return &amount
}

func optionalInt4(value *int32) pgtype.Int4 {
	if value == nil {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: *value, Valid: true}
}

func int4Pointer(value pgtype.Int4) *int32 {
	if !value.Valid {
		return nil
	}
	amount := value.Int32
	return &amount
}

// optionalInt2 narrows an int32 the API speaks into the smallint the column
// holds. The range is checked rather than truncated: a duration of 70000 months
// is a mistake, and silently storing 4464 would be a worse answer than
// refusing it.
func optionalInt2(value *int32) pgtype.Int2 {
	if value == nil || *value < 0 || *value > 32767 {
		return pgtype.Int2{}
	}
	return pgtype.Int2{Int16: int16(*value), Valid: true}
}

func optionalBool(value *bool) pgtype.Bool {
	if value == nil {
		return pgtype.Bool{}
	}
	return pgtype.Bool{Bool: *value, Valid: true}
}

// interval converts a Go duration into the pgtype the database takes, so a
// window is measured against the database's clock rather than this process's.
func interval(duration time.Duration) pgtype.Interval {
	return pgtype.Interval{Microseconds: duration.Microseconds(), Valid: true}
}

// formatInt renders an integer minor-unit amount for a CSV cell. Amounts are
// never formatted with a decimal separator in an export: the column name says
// `_minor`, and inserting a separator would make the file's meaning depend on
// the reader's locale.
func formatInt(value int64) string { return strconv.FormatInt(value, 10) }

// notFound maps pgx's no-rows sentinel onto this package's error, leaving every
// other failure to surface as itself.
func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// conflicted reports an insert that lost to an existing row.
//
// Several inserts here are `ON CONFLICT DO NOTHING`, which is how they are made
// idempotent: a re-run finds the row it already created. Those return no rows,
// and "somebody already did this" is the accurate answer rather than an error.
func conflicted(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrConflict
	}
	return err
}

// rejected distinguishes "the row does not exist" from "the row exists but is
// not in a state this operation accepts".
//
// Every guarded mutation in this package restricts its UPDATE to the states it
// accepts, so both cases arrive as no-rows. Callers that can tell them apart do
// so with an explicit read; the rest report the conservative answer, which is
// that the operation was refused rather than that the record is missing.
func rejected(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrRejected
	}
	return err
}
