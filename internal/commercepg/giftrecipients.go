package commercepg

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

// Gifts addressed to somebody.
//
// A sender may name who a gift is for: a Telegram @username or an e-mail
// address. Once the gift is paid it is delivered: claimed for the customer
// the address belongs to and announced in the bot, or, for an e-mail address
// no Telegram customer holds, mailed a claim code of its own. The sender's
// code keeps working either way, so an addressed gift is never harder to give
// than one handed over by hand.

var (
	// ErrGiftRecipientInvalid refuses a recipient that is neither a Telegram
	// username nor an e-mail address.
	ErrGiftRecipientInvalid = errors.New("gift_recipient_invalid")
	// ErrGiftRecipientSelf refuses a gift addressed to its own sender.
	ErrGiftRecipientSelf = errors.New("gift_recipient_self")
)

var (
	usernamePattern = regexp.MustCompile(`^[a-z0-9_]{5,32}$`)
	emailPattern    = regexp.MustCompile(`^[^@\s<>]+@[^@\s<>]+\.[^@\s<>]+$`)
)

// GiftRecipient is who a gift is addressed to. Exactly one of Username and
// the sealed e-mail is set; Locale is the language the recipient is written
// to in when nothing better is known.
type GiftRecipient struct {
	Username         string
	EmailCiphertext  []byte
	EmailFingerprint []byte
	Locale           string
}

func (recipient GiftRecipient) set() bool {
	return recipient.Username != "" || len(recipient.EmailCiphertext) > 0
}

// ParseGiftRecipient reads what a sender typed as the recipient. It returns
// a normalised Telegram username (without the @, lower-cased) or a trimmed
// e-mail address, and refuses anything else.
func ParseGiftRecipient(input string) (username, email string, err error) {
	value := strings.TrimSpace(input)
	if value == "" {
		return "", "", nil
	}
	if strings.HasPrefix(value, "@") || !strings.Contains(value, "@") {
		handle := NormalizeTelegramUsername(value)
		if !usernamePattern.MatchString(handle) {
			return "", "", ErrGiftRecipientInvalid
		}
		return handle, "", nil
	}
	if len(value) > 254 || !emailPattern.MatchString(value) {
		return "", "", ErrGiftRecipientInvalid
	}
	return "", value, nil
}

// NormalizeTelegramUsername lower-cases a username and drops a leading @.
// Telegram usernames are case-insensitive.
func NormalizeTelegramUsername(value string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(value), "@"))
}

// RecordTelegramUsername remembers the username a customer writes to the bot
// with, taking it from any other customer who held it before: Telegram gives
// a username to one account at a time. An empty username forgets the
// customer's. It writes only when something changed.
func (store *Store) RecordTelegramUsername(ctx context.Context, customerID, username string) error {
	userID, err := parseUUID(customerID)
	if err != nil {
		return err
	}
	username = NormalizeTelegramUsername(username)
	if username == "" || !usernamePattern.MatchString(username) {
		_, err = store.pool.Exec(ctx, `DELETE FROM telegram_usernames WHERE user_id = $1`, userID)
		return err
	}
	var current string
	err = store.pool.QueryRow(ctx, `SELECT username FROM telegram_usernames WHERE user_id = $1`, userID).Scan(&current)
	if err == nil && current == username {
		// The common case, on every message: nothing changed, nothing is written.
		return nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `DELETE FROM telegram_usernames WHERE username = $2 AND user_id <> $1`, userID, username); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO telegram_usernames (user_id, username) VALUES ($1, $2)
		ON CONFLICT (user_id) DO UPDATE SET username = EXCLUDED.username, seen_at = now()`, userID, username); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// recordGiftRecipient stores who a gift is for, in the transaction that
// creates the gift. A gift addressed to its own sender is refused.
func (store *Store) recordGiftRecipient(ctx context.Context, tx pgx.Tx, giftID, senderID pgtype.UUID, recipient GiftRecipient) error {
	if !recipient.set() {
		return nil
	}
	locale := recipient.Locale
	if locale != "en" {
		locale = "ru"
	}
	var self bool
	if recipient.Username != "" {
		if !usernamePattern.MatchString(recipient.Username) {
			return ErrGiftRecipientInvalid
		}
		// A username nobody has written to the bot under yet is accepted: the
		// gift waits for its owner's first message, and is claimed for them
		// then (a "phantom" recipient, as Bedolaga calls it).
		var holder pgtype.UUID
		err := tx.QueryRow(ctx, `SELECT user_id FROM telegram_usernames WHERE username = $1`, recipient.Username).Scan(&holder)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return err
		default:
			self = holder == senderID
		}
	} else if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM contact_channels
			WHERE user_id = $1 AND kind = 'email' AND value_fingerprint = $2 AND revoked_at IS NULL)`,
		senderID, recipient.EmailFingerprint).Scan(&self); err != nil {
		return err
	}
	if self {
		return ErrGiftRecipientSelf
	}
	_, err := tx.Exec(ctx, `INSERT INTO gift_recipients (gift_id, username, email_ciphertext, email_fingerprint, locale)
		VALUES ($1, NULLIF($2, ''), $3, $4, $5)`,
		giftID, recipient.Username, nilIfEmpty(recipient.EmailCiphertext), nilIfEmpty(recipient.EmailFingerprint), locale)
	return err
}

func nilIfEmpty(value []byte) []byte {
	if len(value) == 0 {
		return nil
	}
	return value
}

// AddressedGift is a paid gift waiting to reach the person it is for.
type AddressedGift struct {
	GiftID   string
	SenderID string
	// ExpiresAt is when an unclaimed gift lapses; a gift waiting for a
	// username's owner waits until then.
	ExpiresAt        time.Time
	Username         string
	EmailFingerprint []byte
	Kind             string
	PlanName         string
	CreditMinor      int64
	Currency         string
	Message          string
}

// WaitingAddressedGifts lists paid, unexpired, unclaimed gifts addressed to
// somebody and not yet delivered, oldest first.
func (store *Store) WaitingAddressedGifts(ctx context.Context, limit int) ([]AddressedGift, error) {
	rows, err := store.pool.Query(ctx, `SELECT g.id::text, g.sender_user_id::text, g.expires_at, COALESCE(r.username, ''),
			r.email_fingerprint, g.kind,
			COALESCE((SELECT l.name FROM plan_versions v JOIN plan_localizations l ON l.plan_id = v.plan_id
				WHERE v.id = g.plan_version_id ORDER BY (l.locale = r.locale) DESC LIMIT 1), ''),
			COALESCE(g.credit_minor, 0), g.currency, COALESCE(g.sender_message, '')
		FROM gift_recipients r JOIN gifts g ON g.id = r.gift_id
		WHERE r.status = 'waiting' AND g.status = 'deliverable' AND g.expires_at > now()
		ORDER BY r.created_at LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []AddressedGift{}
	for rows.Next() {
		var gift AddressedGift
		if err = rows.Scan(&gift.GiftID, &gift.SenderID, &gift.ExpiresAt, &gift.Username, &gift.EmailFingerprint, &gift.Kind,
			&gift.PlanName, &gift.CreditMinor, &gift.Currency, &gift.Message); err != nil {
			return nil, err
		}
		result = append(result, gift)
	}
	return result, rows.Err()
}

// GiftRecipientCustomer finds the Telegram customer an addressed gift is for:
// the one who last wrote to the bot under the username, or one with a
// verified e-mail contact at the address and a Telegram identity. found is
// false when there is none yet.
func (store *Store) GiftRecipientCustomer(ctx context.Context, gift AddressedGift) (string, bool, error) {
	var customerID string
	var err error
	if gift.Username != "" {
		err = store.pool.QueryRow(ctx, `SELECT t.user_id::text FROM telegram_usernames t
			JOIN users u ON u.id = t.user_id AND u.status = 'active'
			WHERE t.username = $1`, gift.Username).Scan(&customerID)
	} else {
		err = store.pool.QueryRow(ctx, `SELECT c.user_id::text FROM contact_channels c
			JOIN users u ON u.id = c.user_id AND u.status = 'active'
			WHERE c.kind = 'email' AND c.value_fingerprint = $1 AND c.revoked_at IS NULL AND c.verified_at IS NOT NULL
			  AND EXISTS (SELECT 1 FROM identities i WHERE i.user_id = c.user_id AND i.provider = 'telegram' AND i.status = 'active')
			LIMIT 1`, gift.EmailFingerprint).Scan(&customerID)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return customerID, err == nil, err
}

// DeliverAddressedGift claims an addressed gift for the customer it reached
// and records the delivery, in one transaction, so the gift is claimed and
// marked delivered together or not at all. A gift already claimed with its
// code, or addressed to its own sender, is closed instead and reports
// ErrGiftNotFound.
func (store *Store) DeliverAddressedGift(ctx context.Context, giftID, customerID string) (dbgen.Gift, error) {
	gift, err := parseUUID(giftID)
	if err != nil {
		return dbgen.Gift{}, ErrGiftNotFound
	}
	recipientID, err := parseUUID(customerID)
	if err != nil {
		return dbgen.Gift{}, err
	}
	var claimed dbgen.Gift
	err = retrySerializable(ctx, func() error {
		var attemptErr error
		claimed, attemptErr = store.claimGiftByID(ctx, gift, recipientID, "telegram")
		return attemptErr
	})
	if errors.Is(err, ErrGiftNotFound) || errors.Is(err, ErrGiftOwnClaim) {
		if _, closeErr := store.pool.Exec(ctx, `UPDATE gift_recipients SET status = 'closed',
			email_ciphertext = NULL, email_fingerprint = NULL WHERE gift_id = $1 AND status = 'waiting'`, gift); closeErr != nil {
			return dbgen.Gift{}, closeErr
		}
		return dbgen.Gift{}, ErrGiftNotFound
	}
	return claimed, err
}

// TelegramUsernameKnown reports whether a customer of this bot has written
// to it under the username.
func (store *Store) TelegramUsernameKnown(ctx context.Context, username string) (bool, error) {
	var known bool
	err := store.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM telegram_usernames WHERE username = $1)`,
		NormalizeTelegramUsername(username)).Scan(&known)
	return known, err
}
