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
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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

// LinkIdentity attaches a sign-in identity to a customer on an operator's word.
//
// The same rules as the customer's own link: another customer's identity is
// refused, never moved; the customer's own unlinked row comes back; a row that
// is freed — unlinked by an operator, or replaced on its customer — is taken
// over. An account that is not active gains nothing.
func (service *Service) LinkIdentity(ctx context.Context, customerID, provider, subject string, verifiedAt time.Time, metadata map[string]any) (dbgen.Identity, error) {
	id, err := parseUUID(customerID)
	if err != nil {
		return dbgen.Identity{}, err
	}
	if strings.TrimSpace(provider) == "" || strings.TrimSpace(subject) == "" || verifiedAt.IsZero() {
		return dbgen.Identity{}, errors.New("verified identity is required")
	}
	encoded, _ := json.Marshal(metadata)
	verified := pgtype.Timestamptz{Time: verifiedAt, Valid: true}
	var identity dbgen.Identity
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		if provider == "telegram" {
			// The lock the bot and the web sign-in take, so a first /start
			// cannot create a second customer for this account meanwhile.
			if err := queries.LockTelegramSubject(ctx, subject); err != nil {
				return err
			}
		}
		if err := queries.LockCustomerSignInMethods(ctx, id); err != nil {
			return err
		}
		active, err := queries.CustomerIsActive(ctx, id)
		if err != nil {
			return err
		}
		if !active {
			return customer.ErrIdentityConflict
		}
		existing, err := queries.GetIdentityBySubjectAnyStatus(ctx, dbgen.GetIdentityBySubjectAnyStatusParams{
			Provider: provider, ProviderSubject: subject,
		})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			identity, err = queries.LinkCustomerIdentity(ctx, dbgen.LinkCustomerIdentityParams{
				UserID: id, Provider: provider, ProviderSubject: subject, VerifiedAt: verified, Metadata: encoded,
			})
		case err != nil:
			return err
		case existing.UserID == id && existing.Status == "active":
			identity = identityFromLookup(existing)
		case existing.UserID == id:
			identity, err = queries.ReactivateCustomerIdentity(ctx, dbgen.ReactivateCustomerIdentityParams{
				IdentityID: existing.ID, UserID: id, VerifiedAt: verified,
			})
		case existing.Freed:
			identity, err = queries.ClaimFreedIdentity(ctx, dbgen.ClaimFreedIdentityParams{
				UserID: id, IdentityID: existing.ID, VerifiedAt: verified, OidcIssuer: existing.OidcIssuer,
			})
		default:
			return customer.ErrIdentityConflict
		}
		if errors.Is(err, pgx.ErrNoRows) || isUniqueViolation(err) {
			// Another customer's row, or a second active identity of one
			// provider on this customer, which the schema allows only once.
			return customer.ErrIdentityConflict
		}
		if err != nil {
			return err
		}
		if provider == "telegram" {
			if telegramID, parseErr := strconv.ParseInt(subject, 10, 64); parseErr == nil {
				mapping := pgtype.Int8{Int64: telegramID, Valid: true}
				if err := queries.BackfillTelegramMapping(ctx, dbgen.BackfillTelegramMappingParams{
					TelegramID: mapping, UserID: id,
				}); err != nil {
					return err
				}
				return queries.ReplaceTelegramMapping(ctx, dbgen.ReplaceTelegramMappingParams{
					TelegramID: mapping, UserID: id,
				})
			}
		}
		return nil
	})
	return identity, err
}

// UnlinkIdentity removes a sign-in identity on an operator's word.
//
// Unlike the customer's own unlink, an operator's frees the identity: the
// next sign-in with that account does not relink it here. That is what an
// operator unlinking a Telegram account attached to the wrong person needs;
// before, the next widget sign-in or bot tap put it straight back.
func (service *Service) UnlinkIdentity(ctx context.Context, customerID, identityID string) (dbgen.Identity, error) {
	userID, err := parseUUID(customerID)
	if err != nil {
		return dbgen.Identity{}, err
	}
	targetID, err := parseUUID(identityID)
	if err != nil {
		return dbgen.Identity{}, err
	}
	var removed dbgen.Identity
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		// The count and the removal under one lock, so two operators removing
		// two different identities at once cannot both see one left over.
		if err := queries.LockCustomerSignInMethods(ctx, userID); err != nil {
			return err
		}
		rows, err := queries.ListCustomerIdentities(ctx, userID)
		if err != nil {
			return err
		}
		identities := make([]customer.Identity, 0, len(rows))
		for _, row := range rows {
			identities = append(identities, customer.Identity{ID: uuidString(row.ID), Active: row.Status == "active", Verified: row.VerifiedAt.Valid})
		}
		if err := customer.CanUnlink(identities, identityID); err != nil {
			return err
		}
		removed, err = queries.RevokeCustomerIdentityByOperator(ctx, dbgen.RevokeCustomerIdentityByOperatorParams{IdentityID: targetID, UserID: userID})
		if err != nil {
			return err
		}
		if removed.Provider == "telegram" {
			if telegramID, parseErr := strconv.ParseInt(removed.ProviderSubject, 10, 64); parseErr == nil {
				return queries.ClearTelegramMapping(ctx, dbgen.ClearTelegramMappingParams{
					UserID: userID, TelegramID: pgtype.Int8{Int64: telegramID, Valid: true},
				})
			}
		}
		return nil
	})
	return removed, err
}

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

func identityFromLookup(row dbgen.GetIdentityBySubjectAnyStatusRow) dbgen.Identity {
	return dbgen.Identity{
		ID: row.ID, UserID: row.UserID, Provider: row.Provider, ProviderSubject: row.ProviderSubject,
		VerifiedAt: row.VerifiedAt, CreatedAt: row.CreatedAt, Status: row.Status, RevokedAt: row.RevokedAt,
		Metadata: row.Metadata, OidcIssuer: row.OidcIssuer,
	}
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func (service *Service) SetContact(ctx context.Context, customerID, kind, value string, verifiedAt *time.Time, transactional, marketing bool) (dbgen.ContactChannel, error) {
	userID, err := parseUUID(customerID)
	if err != nil {
		return dbgen.ContactChannel{}, err
	}
	ciphertext, fingerprint, err := service.SealContact(kind, value)
	if err != nil {
		return dbgen.ContactChannel{}, err
	}
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

// SealContact encrypts a contact value and derives the keyed fingerprint the
// uniqueness constraint is checked against. It is the one place that decides
// how a contact is stored, so a caller writing contacts inside its own
// transaction — the Bedolaga importer — stores them exactly as SetContact does.
func (service *Service) SealContact(kind, value string) (ciphertext, fingerprint []byte, err error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil, errors.New("contact value is required")
	}
	fingerprintMAC := hmac.New(sha256.New, service.fingerprintKey)
	_, _ = fingerprintMAC.Write([]byte(strings.ToLower(value)))
	fingerprint = fingerprintMAC.Sum(nil)
	nonce := make([]byte, service.contactAEAD.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, err
	}
	return service.contactAEAD.Seal(nonce, nonce, []byte(value), []byte(kind)), fingerprint, nil
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
