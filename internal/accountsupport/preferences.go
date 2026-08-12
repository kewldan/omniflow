package accountsupport

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// consentPolicyVersion labels the terms a consent record was given under.
//
// It is stamped rather than derived so a later change to the wording cannot
// retroactively claim the customer agreed to text they never saw. The bot writes
// its own value into the same column for the same reason.
const consentPolicyVersion = "web-v0.10"

// suppressionCustomerRequest is the only reason this package ever writes into
// `communication_suppressions`. The other reasons in that column — a bounce, a
// complaint, an operator decision — are somebody else's findings, and a customer
// action must never be able to claim one of them.
const suppressionCustomerRequest = "customer_request"

// Preferences is everything the notification screen shows.
type Preferences struct {
	// Locale is the notification language: 'auto', 'ru', or 'en'.
	Locale        string
	Notifications Notifications
	// QuietHours is nil when the customer has not set a window.
	QuietHours *QuietWindow
	Marketing  MarketingConsent
	Contacts   []ContactChannel
	// Suppression is set when every non-essential message to this customer is
	// being held back. It is reported rather than acted on: this package shows
	// the state, and the communication pipeline is what honours it.
	Suppression *Suppression
}

// Notifications are the per-kind switches the customer controls.
type Notifications struct {
	Expiry  bool
	Traffic bool
	Renewal bool
	News    bool
}

// QuietWindow is a local-time window during which non-urgent messages wait.
type QuietWindow struct {
	StartHour int
	EndHour   int
}

// MarketingConsent is the latest recorded decision, with the evidence behind it.
//
// The evidence is part of the answer on purpose. "You opted in" is not a useful
// thing to tell somebody who does not remember doing so; "you opted in on this
// date, from this surface" is.
type MarketingConsent struct {
	Enabled       bool
	DecidedAt     time.Time
	Source        string
	PolicyVersion string
}

// ContactChannel is one way to reach the customer.
//
// The address itself is never in this struct. `contact_channels` stores it
// encrypted alongside a fingerprint, and a preferences screen needs to say which
// channels exist and what each is used for — not to hand the value back out to
// whoever is holding the session.
type ContactChannel struct {
	ID            string
	Kind          string
	Verified      bool
	Transactional bool
	Marketing     bool
	CreatedAt     time.Time
}

// Suppression is an active hold on non-essential messages.
type Suppression struct {
	Reason    string
	CreatedAt time.Time
}

// PreferencesUpdate is a partial change. A nil field is a field the request did
// not mention, which is left exactly as it was rather than reset to a default —
// the difference between PATCH and PUT, and the difference between a customer
// changing one switch and silently changing all of them.
type PreferencesUpdate struct {
	Locale  *string
	Expiry  *bool
	Traffic *bool
	Renewal *bool
	News    *bool
	// Marketing is the consent switch. Setting it appends to the consent history;
	// it never edits what is already there.
	Marketing *bool
	// QuietHours sets the window. A window whose hours are equal clears it, which
	// is how "deliver at any time" is expressed — the same convention the bot
	// uses, so the two screens mean the same thing by the same input.
	QuietHours *QuietWindow
}

// Preferences reads the notification, consent, contact, and suppression state.
func (service *Service) Preferences(ctx context.Context, customerID string) (Preferences, error) {
	preferences := Preferences{Locale: "auto", Notifications: Notifications{
		Expiry: true, Traffic: true, Renewal: true, News: true,
	}}
	var start, end pgtype.Int2
	var marketingEnabled bool
	// A customer who has never opened a settings screen has no bot_preferences
	// row, and the column defaults are their real preferences. Reading them from
	// the schema's own defaults here keeps that answer in one place.
	err := service.pool.QueryRow(ctx, `SELECT
		COALESCE(p.locale, 'auto'),
		COALESCE(p.expiry_notifications, true), COALESCE(p.traffic_notifications, true),
		COALESCE(p.renewal_notifications, true), COALESCE(p.news_notifications, true),
		COALESCE(p.marketing_enabled, false),
		p.quiet_hours_start, p.quiet_hours_end
		FROM users u LEFT JOIN bot_preferences p ON p.user_id = u.id
		WHERE u.id = $1::uuid`, customerID).
		Scan(&preferences.Locale, &preferences.Notifications.Expiry,
			&preferences.Notifications.Traffic, &preferences.Notifications.Renewal,
			&preferences.Notifications.News, &marketingEnabled, &start, &end)
	if errors.Is(err, pgx.ErrNoRows) {
		return Preferences{}, ErrNotFound
	}
	if err != nil {
		return Preferences{}, err
	}
	if start.Valid && end.Valid {
		preferences.QuietHours = &QuietWindow{StartHour: int(start.Int16), EndHour: int(end.Int16)}
	}

	consent, err := service.marketingConsent(ctx, service.pool, customerID, marketingEnabled)
	if err != nil {
		return Preferences{}, err
	}
	preferences.Marketing = consent

	if preferences.Contacts, err = service.contactChannels(ctx, customerID); err != nil {
		return Preferences{}, err
	}
	if preferences.Suppression, err = service.suppression(ctx, customerID); err != nil {
		return Preferences{}, err
	}
	return preferences, nil
}

// querier is the part of a pool or a transaction this file needs, so the consent
// read can happen inside the transaction that is about to append to it.
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// marketingConsent reads the latest decision.
//
// `consent_records` is append-only, so "the current answer" is the newest row
// rather than a column somebody keeps up to date. The preference column is the
// fallback for an installation that predates the consent history, and it is a
// fallback only: where the two disagree, the record of what the customer
// actually decided wins.
func (service *Service) marketingConsent(
	ctx context.Context, db querier, customerID string, fallback bool,
) (MarketingConsent, error) {
	consent := MarketingConsent{Enabled: fallback}
	var occurredAt pgtype.Timestamptz
	err := db.QueryRow(ctx, `SELECT granted, occurred_at, source, policy_version
		FROM consent_records WHERE user_id = $1::uuid AND purpose = 'marketing'
		ORDER BY occurred_at DESC, id DESC LIMIT 1`, customerID).
		Scan(&consent.Enabled, &occurredAt, &consent.Source, &consent.PolicyVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return MarketingConsent{Enabled: fallback}, nil
	}
	if err != nil {
		return MarketingConsent{}, err
	}
	consent.DecidedAt = occurredAt.Time.UTC()
	return consent, nil
}

func (service *Service) contactChannels(
	ctx context.Context, customerID string,
) ([]ContactChannel, error) {
	// The ciphertext and the fingerprint are not selected. A column that is never
	// read cannot be returned by accident, which is a stronger guarantee than
	// remembering to drop it before the response is written.
	rows, err := service.pool.Query(ctx, `SELECT id::text, kind, verified_at IS NOT NULL,
		transactional_enabled, marketing_enabled, created_at
		FROM contact_channels WHERE user_id = $1::uuid AND revoked_at IS NULL
		ORDER BY created_at, id`, customerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	channels := make([]ContactChannel, 0, 4)
	for rows.Next() {
		var channel ContactChannel
		if err = rows.Scan(&channel.ID, &channel.Kind, &channel.Verified,
			&channel.Transactional, &channel.Marketing, &channel.CreatedAt); err != nil {
			return nil, err
		}
		channel.CreatedAt = channel.CreatedAt.UTC()
		channels = append(channels, channel)
	}
	return channels, rows.Err()
}

func (service *Service) suppression(
	ctx context.Context, customerID string,
) (*Suppression, error) {
	var suppression Suppression
	err := service.pool.QueryRow(ctx, `SELECT reason, created_at
		FROM communication_suppressions WHERE user_id = $1::uuid`, customerID).
		Scan(&suppression.Reason, &suppression.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	suppression.CreatedAt = suppression.CreatedAt.UTC()
	return &suppression, nil
}

// UpdatePreferences applies a partial change and returns the resulting state.
func (service *Service) UpdatePreferences(
	ctx context.Context, customerID string, update PreferencesUpdate, request RequestContext,
) (Preferences, error) {
	// The stored value is normalised rather than passed through: the column's
	// check accepts three exact strings, and refusing "EN" would be a rule about
	// capitalisation rather than about language.
	var locale *string
	if update.Locale != nil {
		normalised := strings.ToLower(strings.TrimSpace(*update.Locale))
		if !validPreferenceLocale(normalised) {
			return Preferences{}, invalid("the notification language must be auto, ru, or en")
		}
		locale = &normalised
	}
	var start, end pgtype.Int2
	if window := update.QuietHours; window != nil {
		if window.StartHour < 0 || window.StartHour > 23 || window.EndHour < 0 || window.EndHour > 23 {
			return Preferences{}, invalid("quiet hours must be whole hours between 0 and 23")
		}
		if window.StartHour != window.EndHour {
			start = pgtype.Int2{Int16: int16(window.StartHour), Valid: true}
			end = pgtype.Int2{Int16: int16(window.EndHour), Valid: true}
		}
	}

	tx, err := service.pool.Begin(ctx)
	if err != nil {
		return Preferences{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var storedMarketing bool
	err = tx.QueryRow(ctx, `SELECT COALESCE(p.marketing_enabled, false)
		FROM users u LEFT JOIN bot_preferences p ON p.user_id = u.id
		WHERE u.id = $1::uuid`, customerID).Scan(&storedMarketing)
	if errors.Is(err, pgx.ErrNoRows) {
		return Preferences{}, ErrNotFound
	}
	if err != nil {
		return Preferences{}, err
	}
	before, err := service.marketingConsent(ctx, tx, customerID, storedMarketing)
	if err != nil {
		return Preferences{}, err
	}

	if _, err = tx.Exec(ctx, `INSERT INTO bot_preferences (user_id, locale,
		expiry_notifications, traffic_notifications, renewal_notifications,
		news_notifications, marketing_enabled, quiet_hours_start, quiet_hours_end)
		VALUES ($1::uuid, COALESCE($2::text, 'auto'),
			COALESCE($3::boolean, true), COALESCE($4::boolean, true),
			COALESCE($5::boolean, true), COALESCE($6::boolean, true),
			COALESCE($7::boolean, false), $8::smallint, $9::smallint)
		ON CONFLICT (user_id) DO UPDATE SET
			locale = COALESCE($2::text, bot_preferences.locale),
			expiry_notifications = COALESCE($3::boolean, bot_preferences.expiry_notifications),
			traffic_notifications = COALESCE($4::boolean, bot_preferences.traffic_notifications),
			renewal_notifications = COALESCE($5::boolean, bot_preferences.renewal_notifications),
			news_notifications = COALESCE($6::boolean, bot_preferences.news_notifications),
			marketing_enabled = COALESCE($7::boolean, bot_preferences.marketing_enabled),
			quiet_hours_start = CASE WHEN $10 THEN $8::smallint ELSE bot_preferences.quiet_hours_start END,
			quiet_hours_end = CASE WHEN $10 THEN $9::smallint ELSE bot_preferences.quiet_hours_end END,
			updated_at = now()`,
		customerID, locale, update.Expiry, update.Traffic, update.Renewal,
		update.News, update.Marketing, start, end, update.QuietHours != nil); err != nil {
		return Preferences{}, err
	}

	if update.Marketing != nil && *update.Marketing != before.Enabled {
		// A decision is appended, never edited. The history is the evidence that
		// an opt-in existed at a moment in time, and a history that can be
		// rewritten is not evidence of anything.
		if err = appendConsent(ctx, tx, customerID, *update.Marketing, request); err != nil {
			return Preferences{}, err
		}
		if *update.Marketing {
			// Re-consenting lifts the hold the customer themselves asked for, and
			// only that one. A suppression recorded for a bounce or a complaint is
			// somebody else's finding about deliverability, and a customer ticking
			// a box does not overturn it.
			if _, err = tx.Exec(ctx, `DELETE FROM communication_suppressions
				WHERE user_id = $1::uuid AND reason = $2`,
				customerID, suppressionCustomerRequest); err != nil {
				return Preferences{}, err
			}
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return Preferences{}, err
	}
	return service.Preferences(ctx, customerID)
}

// Unsubscribe is the one action that stops every non-essential message.
//
// It does not touch the expiry, traffic, or renewal switches. Those carry facts
// a customer needs whether or not they want to hear from the business — that
// their subscription ends on Thursday, that a renewal failed — and an
// unsubscribe that silenced them would turn a marketing preference into a way to
// lose access without warning.
func (service *Service) Unsubscribe(
	ctx context.Context, customerID string, request RequestContext,
) (Preferences, error) {
	tx, err := service.pool.Begin(ctx)
	if err != nil {
		return Preferences{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var exists bool
	if err = tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM users WHERE id = $1::uuid)`, customerID).Scan(&exists); err != nil {
		return Preferences{}, err
	}
	if !exists {
		return Preferences{}, ErrNotFound
	}
	if _, err = tx.Exec(ctx, `INSERT INTO bot_preferences (user_id, marketing_enabled, news_notifications)
		VALUES ($1::uuid, false, false)
		ON CONFLICT (user_id) DO UPDATE SET marketing_enabled = false,
			news_notifications = false, updated_at = now()`, customerID); err != nil {
		return Preferences{}, err
	}
	// The per-channel marketing flags come off with it. Leaving one set would let
	// a later send find a channel that still says yes on an account that said no.
	if _, err = tx.Exec(ctx, `UPDATE contact_channels SET marketing_enabled = false
		WHERE user_id = $1::uuid AND revoked_at IS NULL`, customerID); err != nil {
		return Preferences{}, err
	}
	// Unlike a preference change, this appends whether or not the stored state
	// already said no. The record here is of an action the customer took, and
	// "they had already opted out" is not a reason for their unsubscribe to leave
	// no trace.
	if err = appendConsent(ctx, tx, customerID, false, request); err != nil {
		return Preferences{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO communication_suppressions (user_id, reason)
		VALUES ($1::uuid, $2)
		ON CONFLICT (user_id) DO UPDATE SET reason = EXCLUDED.reason,
			note = NULL, created_by = NULL, created_at = now()`,
		customerID, suppressionCustomerRequest); err != nil {
		return Preferences{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Preferences{}, err
	}
	return service.Preferences(ctx, customerID)
}

// appendConsent writes one row of the consent history.
//
// The source names the surface and the request identifier ties the row back to
// the request that produced it, so a question about where an opt-in came from
// has an answer in the logs rather than in somebody's recollection.
func appendConsent(
	ctx context.Context, tx pgx.Tx, customerID string, granted bool, request RequestContext,
) error {
	requestID := strings.TrimSpace(request.RequestID)
	_, err := tx.Exec(ctx, `INSERT INTO consent_records
		(user_id, purpose, granted, policy_version, source, request_id)
		VALUES ($1::uuid, 'marketing', $2, $3, $4, NULLIF($5, ''))`,
		customerID, granted, consentPolicyVersion, request.surface(), requestID)
	return err
}

func validPreferenceLocale(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "auto", localeRU, localeEN:
		return true
	default:
		return false
	}
}
