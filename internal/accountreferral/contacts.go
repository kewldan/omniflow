package accountreferral

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// Contact is one channel the installation may reach the customer on.
type Contact struct {
	ID   string
	Kind string
	// Value is the address itself, decrypted for its owner. It is empty when the
	// stored ciphertext cannot be opened — under a rotated key, say — so one
	// unreadable row does not fail the whole list.
	Value string
	// Verified reports whether the address was ever proved to belong to this
	// customer. The panel cannot prove it: adding a channel here records an
	// intention, and the communication pipeline decides what an unverified
	// channel is allowed to receive.
	Verified bool
	// Transactional and Marketing are what the customer agreed to receive on
	// this channel. Neither grants permission on its own — suppression, quiet
	// hours, and frequency caps belong to the pipeline — but a false here is a
	// refusal nothing downstream may override.
	Transactional bool
	Marketing     bool
	CreatedAt     time.Time
}

// ContactInput is a request to add a channel.
type ContactInput struct {
	Kind          string
	Value         string
	Transactional bool
	Marketing     bool
}

// Contact channel kinds the schema admits.
const (
	ContactEmail    = "email"
	ContactPhone    = "phone"
	ContactTelegram = "telegram"
)

// The consent trail written from this surface. `policyVersion` names the terms
// in force when the customer chose, so a later change to those terms cannot
// retroactively reinterpret an old choice, and `consentSource` distinguishes a
// web decision from the bot's.
const (
	policyVersion = "account-web-v0.10"
	consentSource = "account_web"
)

var (
	// Deliberately loose. A strict address grammar rejects valid addresses more
	// often than it catches invalid ones, and the only thing that actually
	// proves an address is delivery to it.
	emailPattern    = regexp.MustCompile(`^[^@\s]+@[^@\s.]+\.[^@\s]+$`)
	phonePattern    = regexp.MustCompile(`^\+?[1-9][0-9]{6,14}$`)
	telegramPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{4,31}$`)
)

// Contacts lists the customer's active channels.
func (service *Service) Contacts(ctx context.Context, customerID string) ([]Contact, error) {
	if !service.ContactsAvailable() {
		return nil, ErrContactsUnavailable
	}
	userID, err := parseUUID(customerID)
	if err != nil {
		return nil, err
	}
	rows, err := service.queries.ListAccountContacts(ctx, userID)
	if err != nil {
		return nil, err
	}

	contacts := make([]Contact, 0, max(len(rows), 4))
	for _, row := range rows {
		contacts = append(contacts, Contact{
			ID:            uuidString(row.ID),
			Kind:          row.Kind,
			Value:         service.open(row.Kind, row.ValueCiphertext),
			Verified:      row.VerifiedAt.Valid,
			Transactional: row.TransactionalEnabled,
			Marketing:     row.MarketingEnabled,
			CreatedAt:     row.CreatedAt.Time.UTC(),
		})
	}
	return contacts, nil
}

// AddContact registers a channel for the calling customer.
//
// A value already registered under `UNIQUE (kind, value_fingerprint)` produces
// ErrContactConflict and nothing else. The caller is not told whether the row
// belongs to another customer, to a suspended account, or to a channel this
// customer revoked long ago, because each of those answers turns the panel into
// a way of testing whether a given address has an account here. The remedy is a
// support handoff, where a person can establish who is asking.
//
// One exception, and it reveals nothing: a row this customer already owns is
// theirs to see in the list anyway, so re-adding it un-revokes and re-flags it.
func (service *Service) AddContact(
	ctx context.Context, customerID string, input ContactInput, request RequestContext,
) (Contact, error) {
	if !service.ContactsAvailable() {
		return Contact{}, ErrContactsUnavailable
	}
	userID, err := parseUUID(customerID)
	if err != nil {
		return Contact{}, err
	}
	kind, value, err := NormalizeContact(input.Kind, input.Value)
	if err != nil {
		return Contact{}, err
	}
	if !input.Transactional && !input.Marketing {
		// A channel that may receive nothing is an address stored for no reason.
		// Refusing it keeps the retention story simple: every row here exists
		// because the customer asked for something to be sent to it.
		return Contact{}, invalid("a contact channel must accept at least one kind of message")
	}

	ciphertext, fingerprint, err := service.seal(kind, value)
	if err != nil {
		return Contact{}, err
	}

	transaction, err := service.pool.Begin(ctx)
	if err != nil {
		return Contact{}, err
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	queries := service.queries.WithTx(transaction)

	added, err := queries.AddAccountContact(ctx, dbgen.AddAccountContactParams{
		UserID:               userID,
		Kind:                 kind,
		ValueCiphertext:      ciphertext,
		ValueFingerprint:     fingerprint,
		TransactionalEnabled: input.Transactional,
		MarketingEnabled:     input.Marketing,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Contact{}, ErrContactConflict
	}
	if err != nil {
		return Contact{}, err
	}

	// Marketing is a consent decision and belongs in the consent trail, not only
	// on the channel row. An operator asked to prove somebody opted in needs a
	// dated record of the choice, and the flag alone only shows the latest state.
	if input.Marketing {
		if err = recordConsent(ctx, queries, userID, "marketing", true, request.RequestID); err != nil {
			return Contact{}, err
		}
	}
	if err = service.recordAudit(ctx, queries, customerID, "account.contact.added", request.RequestID,
		map[string]any{"kind": kind, "marketing": input.Marketing},
	); err != nil {
		return Contact{}, err
	}
	if err = transaction.Commit(ctx); err != nil {
		return Contact{}, err
	}
	return Contact{
		ID: uuidString(added.ID), Kind: kind, Value: value,
		Transactional: input.Transactional, Marketing: input.Marketing,
		CreatedAt: added.CreatedAt.Time.UTC(),
	}, nil
}

// RemoveContact revokes a channel.
//
// The row is marked revoked rather than deleted. The fingerprint stays unique,
// so an address cannot be silently moved between accounts by removing it from
// one, and the consent history keeps pointing at something.
//
// Ownership is in the WHERE clause: a channel belonging to somebody else is
// ErrNotFound, the same answer as one that never existed.
func (service *Service) RemoveContact(
	ctx context.Context, customerID, contactID string, request RequestContext,
) error {
	userID, err := parseUUID(customerID)
	if err != nil {
		return err
	}
	channelID, err := parseUUID(contactID)
	if err != nil {
		return ErrNotFound
	}

	transaction, err := service.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	queries := service.queries.WithTx(transaction)

	revoked, err := queries.RevokeAccountContact(ctx, dbgen.RevokeAccountContactParams{
		ChannelID: channelID, UserID: userID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}

	// Withdrawing marketing consent is recorded only when the last channel that
	// carried it goes. Writing a withdrawal while another opted-in channel
	// remains would misstate what the customer actually chose.
	if revoked.MarketingEnabled {
		remaining, err := queries.CountAccountMarketingContacts(ctx, userID)
		if err != nil {
			return err
		}
		if remaining == 0 {
			if err = recordConsent(ctx, queries, userID, "marketing", false, request.RequestID); err != nil {
				return err
			}
		}
	}
	if err = service.recordAudit(ctx, queries, customerID, "account.contact.removed", request.RequestID,
		map[string]any{"kind": revoked.Kind},
	); err != nil {
		return err
	}
	return transaction.Commit(ctx)
}

// NormalizeContact validates a channel and returns it in the form it is stored.
//
// Normalization is not cosmetic. The uniqueness constraint is on a MAC of the
// value, so "+7 900 000 00 00" and "+79000000000" would otherwise be two
// different channels for one phone, and an address that differs only in case
// would be two different mailboxes as far as the database is concerned.
func NormalizeContact(kind, value string) (string, string, error) {
	kind = strings.ToLower(strings.TrimSpace(kind))
	value = strings.TrimSpace(value)
	if value == "" {
		return "", "", invalid("a contact value is required")
	}
	if len(value) > 254 {
		return "", "", invalid("that contact value is too long")
	}

	switch kind {
	case ContactEmail:
		value = strings.ToLower(value)
		if !emailPattern.MatchString(value) {
			return "", "", invalid("that does not look like an email address")
		}
	case ContactPhone:
		value = strings.Map(func(character rune) rune {
			if strings.ContainsRune(" -() ", character) {
				return -1
			}
			return character
		}, value)
		if !phonePattern.MatchString(value) {
			return "", "", invalid("that does not look like a phone number in international format")
		}
	case ContactTelegram:
		value = strings.ToLower(strings.TrimPrefix(value, "@"))
		if !telegramPattern.MatchString(value) {
			return "", "", invalid("that does not look like a Telegram username")
		}
	default:
		return "", "", invalid("unsupported contact kind")
	}
	return kind, value, nil
}

// recordConsent appends one dated decision inside the caller's transaction, so
// the record commits with the change it describes and can never claim a consent
// that was rolled back.
func recordConsent(
	ctx context.Context, queries *dbgen.Queries,
	userID pgtype.UUID, purpose string, granted bool, requestID string,
) error {
	_, err := queries.InsertConsentRecord(ctx, dbgen.InsertConsentRecordParams{
		UserID:        userID,
		Purpose:       purpose,
		Granted:       granted,
		PolicyVersion: policyVersion,
		Source:        consentSource,
		RequestID:     optionalText(requestID),
	})
	return err
}
