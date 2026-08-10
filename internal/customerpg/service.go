package customerpg

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
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/customer"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

type Service struct {
	pool           *pgxpool.Pool
	contactAEAD    cipher.AEAD
	fingerprintKey []byte
	clock          func() time.Time
	retention      time.Duration
}

func New(pool *pgxpool.Pool, encryptionKey []byte, retention time.Duration) (*Service, error) {
	if len(encryptionKey) != 32 {
		return nil, errors.New("contact encryption key must be 32 bytes")
	}
	block, err := aes.NewCipher(encryptionKey)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Service{pool: pool, contactAEAD: aead, fingerprintKey: append([]byte(nil), encryptionKey...), clock: time.Now, retention: retention}, nil
}

func (service *Service) UpdateProfile(ctx context.Context, customerID, locale, timezone string) (dbgen.User, error) {
	if err := customer.ValidateLocaleTimezone(locale, timezone); err != nil {
		return dbgen.User{}, err
	}
	id, err := parseUUID(customerID)
	if err != nil {
		return dbgen.User{}, err
	}
	return dbgen.New(service.pool).UpdateCustomerPreferences(ctx, dbgen.UpdateCustomerPreferencesParams{UserID: id, Locale: locale, Timezone: timezone})
}

func (service *Service) LinkIdentity(ctx context.Context, customerID, provider, subject string, verifiedAt time.Time, metadata map[string]any) (dbgen.Identity, error) {
	id, err := parseUUID(customerID)
	if err != nil {
		return dbgen.Identity{}, err
	}
	if strings.TrimSpace(provider) == "" || strings.TrimSpace(subject) == "" || verifiedAt.IsZero() {
		return dbgen.Identity{}, errors.New("verified identity is required")
	}
	encoded, _ := json.Marshal(metadata)
	identity, err := dbgen.New(service.pool).LinkCustomerIdentity(ctx, dbgen.LinkCustomerIdentityParams{UserID: id, Provider: provider, ProviderSubject: subject, VerifiedAt: pgtype.Timestamptz{Time: verifiedAt, Valid: true}, Metadata: encoded})
	if errors.Is(err, pgx.ErrNoRows) {
		return dbgen.Identity{}, customer.ErrIdentityConflict
	}
	return identity, err
}

func (service *Service) UnlinkIdentity(ctx context.Context, customerID, identityID string) (dbgen.Identity, error) {
	userID, err := parseUUID(customerID)
	if err != nil {
		return dbgen.Identity{}, err
	}
	targetID, err := parseUUID(identityID)
	if err != nil {
		return dbgen.Identity{}, err
	}
	queries := dbgen.New(service.pool)
	rows, err := queries.ListCustomerIdentities(ctx, userID)
	if err != nil {
		return dbgen.Identity{}, err
	}
	identities := make([]customer.Identity, 0, len(rows))
	for _, row := range rows {
		identities = append(identities, customer.Identity{ID: uuidString(row.ID), Active: row.Status == "active", Verified: row.VerifiedAt.Valid})
	}
	if err := customer.CanUnlink(identities, identityID); err != nil {
		return dbgen.Identity{}, err
	}
	return queries.RevokeCustomerIdentity(ctx, dbgen.RevokeCustomerIdentityParams{IdentityID: targetID, UserID: userID})
}

func (service *Service) SetContact(ctx context.Context, customerID, kind, value string, verifiedAt *time.Time, transactional, marketing bool) (dbgen.ContactChannel, error) {
	userID, err := parseUUID(customerID)
	if err != nil {
		return dbgen.ContactChannel{}, err
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return dbgen.ContactChannel{}, errors.New("contact value is required")
	}
	fingerprintMAC := hmac.New(sha256.New, service.fingerprintKey)
	_, _ = fingerprintMAC.Write([]byte(strings.ToLower(value)))
	fingerprint := fingerprintMAC.Sum(nil)
	nonce := make([]byte, service.contactAEAD.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return dbgen.ContactChannel{}, err
	}
	ciphertext := service.contactAEAD.Seal(nonce, nonce, []byte(value), []byte(kind))
	verified := pgtype.Timestamptz{}
	if verifiedAt != nil {
		verified = pgtype.Timestamptz{Time: *verifiedAt, Valid: true}
	}
	contact, err := dbgen.New(service.pool).CreateContactChannel(ctx, dbgen.CreateContactChannelParams{UserID: userID, Kind: kind, ValueCiphertext: ciphertext, ValueFingerprint: fingerprint, VerifiedAt: verified, TransactionalEnabled: transactional, MarketingEnabled: marketing})
	if errors.Is(err, pgx.ErrNoRows) {
		return dbgen.ContactChannel{}, customer.ErrIdentityConflict
	}
	return contact, err
}

func (service *Service) RecordConsent(ctx context.Context, customerID, purpose string, granted bool, policyVersion, source, requestID string) (dbgen.ConsentRecord, error) {
	id, err := parseUUID(customerID)
	if err != nil {
		return dbgen.ConsentRecord{}, err
	}
	return dbgen.New(service.pool).InsertConsentRecord(ctx, dbgen.InsertConsentRecordParams{UserID: id, Purpose: purpose, Granted: granted, PolicyVersion: policyVersion, Source: source, RequestID: optionalText(requestID)})
}

func (service *Service) Lifecycle(ctx context.Context, customerID, action, reason, actorType, actorID, requestID string) (dbgen.User, error) {
	id, err := parseUUID(customerID)
	if err != nil {
		return dbgen.User{}, err
	}
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return dbgen.User{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := dbgen.New(tx)
	row, err := queries.GetCustomer(ctx, id)
	if err != nil {
		return dbgen.User{}, err
	}
	profile := customer.Profile{ID: customerID, Status: customer.Status(row.Status), Locale: row.Locale, Timezone: row.Timezone}
	if row.SuspendedAt.Valid {
		profile.SuspendedAt = &row.SuspendedAt.Time
	}
	if row.DeletedAt.Valid {
		profile.DeletedAt = &row.DeletedAt.Time
	}
	if row.AnonymizedAt.Valid {
		profile.AnonymizedAt = &row.AnonymizedAt.Time
	}
	if row.RetentionUntil.Valid {
		profile.RetentionUntil = &row.RetentionUntil.Time
	}
	next, err := profile.Transition(action, reason, service.clock(), service.retention)
	if err != nil {
		return dbgen.User{}, err
	}
	updated, err := queries.ApplyCustomerLifecycle(ctx, dbgen.ApplyCustomerLifecycleParams{UserID: id, Status: string(next.Status), SuspendedAt: optionalTime(next.SuspendedAt), DeletedAt: optionalTime(next.DeletedAt), AnonymizedAt: optionalTime(next.AnonymizedAt), RetentionUntil: optionalTime(next.RetentionUntil)})
	if err != nil {
		return dbgen.User{}, err
	}
	if action == "anonymize" {
		if err = queries.AnonymizeCustomerData(ctx, id); err != nil {
			return dbgen.User{}, err
		}
	}
	eventAction := map[string]string{"suspend": "suspended", "restore": "restored", "delete": "deleted", "anonymize": "anonymized"}[action]
	if _, err = queries.InsertCustomerLifecycleEvent(ctx, dbgen.InsertCustomerLifecycleEventParams{UserID: id, Action: eventAction, Reason: reason, ActorType: actorType, ActorID: optionalText(actorID), RequestID: optionalText(requestID)}); err != nil {
		return dbgen.User{}, err
	}
	return updated, tx.Commit(ctx)
}

func parseUUID(value string) (pgtype.UUID, error) {
	var id pgtype.UUID
	if err := id.Scan(value); err != nil || !id.Valid {
		return pgtype.UUID{}, fmt.Errorf("invalid UUID")
	}
	return id, nil
}
func optionalText(value string) pgtype.Text { return pgtype.Text{String: value, Valid: value != ""} }
func optionalTime(value *time.Time) pgtype.Timestamptz {
	if value == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *value, Valid: true}
}
func uuidString(value pgtype.UUID) string {
	b := value.Bytes
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
