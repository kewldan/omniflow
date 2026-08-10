package botapp

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/commerce"
)

// Customer is the canonical Omniflow customer behind a Telegram account. It is
// independent of both the Telegram identifier and the Remnawave user, which may
// not exist yet for someone who has not bought anything.
type Customer struct {
	ID          string
	Locale      string
	Status      string
	Timezone    string
	CreatedAt   time.Time
	RemnawaveID int64
}

// Provisioned reports whether Remnawave already holds a VPN user for this
// customer.
func (customer Customer) Provisioned() bool { return customer.RemnawaveID > 0 }

// EnsureCustomer resolves — and, for a first-time visitor, creates — the
// canonical customer behind a Telegram account. A transaction-scoped advisory
// lock keyed on the Telegram identifier makes concurrent updates from the same
// account converge on one customer instead of racing to create two.
func (store *PostgresStore) EnsureCustomer(ctx context.Context, telegramID int64, telegramLocale string) (Customer, error) {
	subject := strconv.FormatInt(telegramID, 10)
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return Customer{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('omniflow:telegram:' || $1, 0))`, subject); err != nil {
		return Customer{}, err
	}
	customer, err := scanCustomer(tx.QueryRow(ctx, `SELECT u.id::text, u.locale, u.status, u.timezone, u.created_at,
		COALESCE(r.remnawave_id, 0)
		FROM identities i
		JOIN users u ON u.id = i.user_id
		LEFT JOIN remnawave_users r ON r.user_id = u.id
		WHERE i.provider = 'telegram' AND i.provider_subject = $1 AND i.status = 'active'`, subject))
	if errors.Is(err, pgx.ErrNoRows) {
		// A v0.2 installation linked Telegram accounts through the Remnawave
		// mapping only. Adopt that mapping instead of creating a second customer.
		customer, err = scanCustomer(tx.QueryRow(ctx, `SELECT u.id::text, u.locale, u.status, u.timezone, u.created_at, r.remnawave_id
			FROM remnawave_users r JOIN users u ON u.id = r.user_id WHERE r.telegram_id = $1`, telegramID))
	}
	if errors.Is(err, pgx.ErrNoRows) {
		customer, err = scanCustomer(tx.QueryRow(ctx, `INSERT INTO users (locale) VALUES ($1)
			RETURNING id::text, locale, status, timezone, created_at, 0::bigint`, string(localeFrom(telegramLocale))))
	}
	if err != nil {
		return Customer{}, fmt.Errorf("resolve Telegram customer: %w", err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO identities (user_id, provider, provider_subject, verified_at, status)
		VALUES ($1::uuid, 'telegram', $2, now(), 'active')
		ON CONFLICT (provider, provider_subject) DO NOTHING`, customer.ID, subject); err != nil {
		return Customer{}, fmt.Errorf("link Telegram identity: %w", err)
	}
	// Keep the v0.2 Remnawave mapping addressable by Telegram ID so existing
	// notification and self-service queries continue to resolve.
	if _, err = tx.Exec(ctx, `UPDATE remnawave_users SET telegram_id = $2
		WHERE user_id = $1::uuid AND telegram_id IS NULL
		  AND NOT EXISTS (SELECT 1 FROM remnawave_users other WHERE other.telegram_id = $2)`, customer.ID, telegramID); err != nil {
		return Customer{}, fmt.Errorf("backfill Telegram mapping: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return Customer{}, err
	}
	return customer, nil
}

func scanCustomer(row pgx.Row) (Customer, error) {
	var customer Customer
	err := row.Scan(&customer.ID, &customer.Locale, &customer.Status, &customer.Timezone, &customer.CreatedAt, &customer.RemnawaveID)
	return customer, err
}

// CustomerPreferences extends the v0.2 bot preferences with the communication
// controls v0.4 requires.
type CustomerPreferences struct {
	Preferences
	RenewalNotifications bool
	NewsNotifications    bool
	MarketingEnabled     bool
	QuietHours           commerce.QuietHours
	Timezone             string
}

// CustomerPreferences reads every communication preference for one customer.
func (store *PostgresStore) CustomerPreferences(ctx context.Context, customerID string) (CustomerPreferences, error) {
	preferences := CustomerPreferences{Preferences: Preferences{Locale: "auto", ExpiryNotifications: true, TrafficNotifications: true}, RenewalNotifications: true, NewsNotifications: true}
	var quietStart, quietEnd pgtype.Int2
	err := store.pool.QueryRow(ctx, `SELECT COALESCE(p.locale, 'auto'), COALESCE(p.expiry_notifications, true),
		COALESCE(p.traffic_notifications, true), COALESCE(p.renewal_notifications, true),
		COALESCE(p.news_notifications, true), COALESCE(p.marketing_enabled, false),
		p.quiet_hours_start, p.quiet_hours_end, u.timezone
		FROM users u LEFT JOIN bot_preferences p ON p.user_id = u.id
		WHERE u.id = $1::uuid`, customerID).Scan(&preferences.Locale, &preferences.ExpiryNotifications,
		&preferences.TrafficNotifications, &preferences.RenewalNotifications, &preferences.NewsNotifications,
		&preferences.MarketingEnabled, &quietStart, &quietEnd, &preferences.Timezone)
	if errors.Is(err, pgx.ErrNoRows) {
		return preferences, ErrNotLinked
	}
	if err != nil {
		return preferences, err
	}
	if quietStart.Valid && quietEnd.Valid {
		preferences.QuietHours = commerce.QuietHours{Configured: true, StartHour: int(quietStart.Int16), EndHour: int(quietEnd.Int16), Location: loadLocation(preferences.Timezone)}
	}
	return preferences, nil
}

// notificationToggles maps a settings action onto the column it flips. Keeping
// the set closed prevents callback data from selecting an arbitrary column.
var notificationToggles = map[string]string{
	"expiry":    "expiry_notifications",
	"traffic":   "traffic_notifications",
	"renewal":   "renewal_notifications",
	"news":      "news_notifications",
	"marketing": "marketing_enabled",
}

// ToggleCustomerNotification flips one preference for a customer and records a
// consent record whenever marketing consent changes.
func (store *PostgresStore) ToggleCustomerNotification(ctx context.Context, customerID, kind string) (bool, error) {
	column, allowed := notificationToggles[kind]
	if !allowed {
		return false, errors.New("unsupported notification kind")
	}
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var enabled bool
	// The insert branch stores the negation of the column default so a first tap
	// always changes something the customer can see.
	command := `INSERT INTO bot_preferences (user_id, ` + column + `) VALUES ($1::uuid, $2)
		ON CONFLICT (user_id) DO UPDATE SET ` + column + ` = NOT bot_preferences.` + column + `, updated_at = now()
		RETURNING ` + column
	if err = tx.QueryRow(ctx, command, customerID, kind == "marketing").Scan(&enabled); err != nil {
		return false, err
	}
	if kind == "marketing" {
		if _, err = tx.Exec(ctx, `INSERT INTO consent_records (user_id, purpose, granted, policy_version, source)
			VALUES ($1::uuid, 'marketing', $2, 'bot-v0.4', 'telegram_bot')`, customerID, enabled); err != nil {
			return false, err
		}
	}
	return enabled, tx.Commit(ctx)
}

// SetQuietHours stores or clears the customer's quiet window. Passing equal
// hours clears it, which is how the bot offers an "always deliver" choice.
func (store *PostgresStore) SetQuietHours(ctx context.Context, customerID string, startHour, endHour int) error {
	if startHour < 0 || startHour > 23 || endHour < 0 || endHour > 23 {
		return errors.New("quiet hours must be whole hours between 0 and 23")
	}
	start, end := pgtype.Int2{Int16: int16(startHour), Valid: true}, pgtype.Int2{Int16: int16(endHour), Valid: true}
	if startHour == endHour {
		start, end = pgtype.Int2{}, pgtype.Int2{}
	}
	_, err := store.pool.Exec(ctx, `INSERT INTO bot_preferences (user_id, quiet_hours_start, quiet_hours_end)
		VALUES ($1::uuid, $2, $3)
		ON CONFLICT (user_id) DO UPDATE SET quiet_hours_start = EXCLUDED.quiet_hours_start,
			quiet_hours_end = EXCLUDED.quiet_hours_end, updated_at = now()`, customerID, start, end)
	return err
}

// SetCustomerLocale persists the interface language for a customer.
func (store *PostgresStore) SetCustomerLocale(ctx context.Context, customerID, locale string) error {
	if locale != "auto" && locale != "ru" && locale != "en" {
		return errors.New("unsupported locale")
	}
	_, err := store.pool.Exec(ctx, `INSERT INTO bot_preferences (user_id, locale) VALUES ($1::uuid, $2)
		ON CONFLICT (user_id) DO UPDATE SET locale = EXCLUDED.locale, updated_at = now()`, customerID, locale)
	return err
}

// DeliveryState is the durable Telegram reachability record for a customer.
type DeliveryState struct {
	Status              string
	LastErrorCode       string
	ConsecutiveFailures int
	RetryAfter          time.Time
}

// DeliveryState reads the current reachability of a customer's Telegram chat.
func (store *PostgresStore) DeliveryState(ctx context.Context, customerID string) (DeliveryState, error) {
	state := DeliveryState{Status: "active"}
	var errorCode pgtype.Text
	var retryAfter pgtype.Timestamptz
	err := store.pool.QueryRow(ctx, `SELECT status, last_error_code, consecutive_failures, retry_after
		FROM bot_delivery_state WHERE user_id = $1::uuid`, customerID).
		Scan(&state.Status, &errorCode, &state.ConsecutiveFailures, &retryAfter)
	if errors.Is(err, pgx.ErrNoRows) {
		return state, nil
	}
	if err != nil {
		return DeliveryState{}, err
	}
	state.LastErrorCode, state.RetryAfter = errorCode.String, retryAfter.Time
	return state, nil
}

// RecordDeliverySuccess clears any accumulated failure state for a customer.
func (store *PostgresStore) RecordDeliverySuccess(ctx context.Context, customerID string) error {
	_, err := store.pool.Exec(ctx, `INSERT INTO bot_delivery_state (user_id, status, consecutive_failures)
		VALUES ($1::uuid, 'active', 0)
		ON CONFLICT (user_id) DO UPDATE SET status = 'active', consecutive_failures = 0,
			last_error_code = NULL, retry_after = NULL, updated_at = now()`, customerID)
	return err
}

// RecordDeliveryFailure classifies a Telegram failure and stops retrying an
// account that blocked the bot or no longer exists.
func (store *PostgresStore) RecordDeliveryFailure(ctx context.Context, customerID, code string, retryAfter time.Time) error {
	status, _ := commerce.ClassifyTelegramFailure(code)
	retry := pgtype.Timestamptz{}
	if !retryAfter.IsZero() {
		retry = pgtype.Timestamptz{Time: retryAfter, Valid: true}
	}
	_, err := store.pool.Exec(ctx, `INSERT INTO bot_delivery_state (user_id, status, last_error_code, consecutive_failures, retry_after)
		VALUES ($1::uuid, $2, $3, 1, $4)
		ON CONFLICT (user_id) DO UPDATE SET status = EXCLUDED.status, last_error_code = EXCLUDED.last_error_code,
			consecutive_failures = bot_delivery_state.consecutive_failures + 1,
			retry_after = EXCLUDED.retry_after, updated_at = now()`, customerID, status, code, retry)
	return err
}

func loadLocation(name string) *time.Location {
	if name == "" {
		return time.UTC
	}
	location, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return location
}
