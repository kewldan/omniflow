// Package recurringpg is the PostgreSQL half of a customer's automatic
// renewal settings and saved payment methods.
//
// It is shared by the Telegram bot and the customer web panel so both surfaces
// read and change the same rows through the same statements. A customer who
// turns auto-renew on in the browser and looks at it in the bot must see one
// setting, not two implementations of one.
package recurringpg

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/recurring"
)

// Store reads and writes renewal settings and saved methods.
type Store struct {
	pool *pgxpool.Pool
}

// New builds the store.
func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// AutoRenew is the customer's recurring-billing intent for one subscription.
type AutoRenew struct {
	Enabled       bool
	PlanVersionID string
	Provider      string
	Currency      string
	CancelledAt   time.Time
	// SubscriptionID names the subscription the setting renews. The table is
	// keyed on (user_id, subscription_id), so a customer with several
	// subscriptions may renew one automatically and keep another manual. It is
	// empty only for a row that names no subscription.
	SubscriptionID string
}

// AutoRenew reads the auto-renew intent for one subscription. An empty
// subscription identifier addresses the row with no subscription.
func (store *Store) AutoRenew(ctx context.Context, customerID, subscriptionID string) (AutoRenew, error) {
	var (
		setting       AutoRenew
		planVersionID pgtype.Text
		provider      pgtype.Text
		currency      pgtype.Text
		cancelledAt   pgtype.Timestamptz
		subscription  pgtype.Text
	)
	err := store.pool.QueryRow(ctx, `SELECT enabled, plan_version_id::text, provider, currency, cancelled_at,
			subscription_id::text
		FROM auto_renew_settings
		WHERE user_id = $1::uuid AND subscription_id IS NOT DISTINCT FROM NULLIF($2, '')::uuid`,
		customerID, subscriptionID).
		Scan(&setting.Enabled, &planVersionID, &provider, &currency, &cancelledAt, &subscription)
	if errors.Is(err, pgx.ErrNoRows) {
		return AutoRenew{SubscriptionID: subscriptionID}, nil
	}
	if err != nil {
		return AutoRenew{}, err
	}
	setting.PlanVersionID, setting.Provider, setting.Currency = planVersionID.String, provider.String, currency.String
	setting.CancelledAt = cancelledAt.Time
	setting.SubscriptionID = subscription.String
	return setting, nil
}

// SetAutoRenew records or cancels the customer's auto-renew intent for one
// subscription. Enabling it requires a plan, a provider, and a currency;
// cancelling only needs the customer and the subscription, so a customer can
// always turn it off.
//
// Both statements name `(user_id, subscription_id)` as the conflict target. That
// is the table's uniqueness, and the index is declared NULLS NOT DISTINCT, so a
// row with no subscription is still one row.
// The renewal worker joins the entitlement on the same pair, which is why the
// subscription has to be written here: a row without one matches no
// entitlement and is never renewed.
func (store *Store) SetAutoRenew(ctx context.Context, customerID string, setting AutoRenew) error {
	if !setting.Enabled {
		// Turning it off returns the state to idle so the worker stops looking at
		// the row, and clears the consent that made it chargeable. Saved methods
		// are deliberately left alone: withdrawing agreement to automatic
		// charging is not the same as asking for a card to be forgotten.
		_, err := store.pool.Exec(ctx, `INSERT INTO auto_renew_settings (user_id, subscription_id, enabled, cancelled_at)
			VALUES ($1::uuid, NULLIF($2, '')::uuid, false, now())
			ON CONFLICT (user_id, subscription_id) DO UPDATE SET enabled = false, plan_version_id = NULL, provider = NULL,
				currency = NULL, cancelled_at = now(), consent_at = NULL, state = 'idle',
				updated_at = now()`, customerID, setting.SubscriptionID)
		return err
	}
	if setting.PlanVersionID == "" || setting.Provider == "" || setting.Currency == "" {
		return errors.New("auto-renew requires a plan, a provider, and a currency")
	}
	// Enabling is the moment the customer agrees, so it is the moment consent is
	// recorded. The worker checks the timestamp rather than the flag: a row
	// written by an import or a migration has a true `enabled` and no evidence
	// that anybody agreed to anything, and it must not produce a charge.
	_, err := store.pool.Exec(ctx, `INSERT INTO auto_renew_settings
			(user_id, subscription_id, enabled, plan_version_id, provider, currency, consent_at, state)
		VALUES ($1::uuid, NULLIF($2, '')::uuid, true, $3::uuid, $4, $5, now(), 'scheduled')
		ON CONFLICT (user_id, subscription_id) DO UPDATE SET enabled = true, plan_version_id = EXCLUDED.plan_version_id,
			provider = EXCLUDED.provider, currency = EXCLUDED.currency, cancelled_at = NULL,
			consent_at = now(), state = 'scheduled', last_failure_code = NULL, updated_at = now()`,
		customerID, setting.SubscriptionID, setting.PlanVersionID, setting.Provider, setting.Currency)
	return err
}

// SavedMethod is one stored payment method as the customer sees it.
//
// There is no card number, expiry, or verification value here, and there is no
// column that could hold one. `Label` is whatever the provider supplied — the
// last four digits and a scheme name, typically — and `Token` never leaves the
// server: the bot addresses a method by its own row identifier.
type SavedMethod struct {
	ID        string
	Provider  string
	Label     string
	Status    string
	IsDefault bool
	CreatedAt time.Time
}

// SavedMethods lists the methods a customer can renew with.
//
// Revoked methods are excluded rather than shown greyed out. A customer who
// removed a card has said what they want; keeping it visible invites them to
// wonder whether it is really gone.
func (store *Store) SavedMethods(
	ctx context.Context, customerID string,
) ([]SavedMethod, error) {
	rows, err := store.pool.Query(ctx,
		`SELECT id::text, provider, display_label, status, is_default, created_at
		 FROM payment_methods
		 WHERE user_id = $1::uuid AND status <> 'revoked'
		 ORDER BY is_default DESC, created_at DESC`, customerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	methods := make([]SavedMethod, 0, 4)
	for rows.Next() {
		var method SavedMethod
		if err := rows.Scan(&method.ID, &method.Provider, &method.Label,
			&method.Status, &method.IsDefault, &method.CreatedAt); err != nil {
			return nil, err
		}
		methods = append(methods, method)
	}
	return methods, rows.Err()
}

// SetDefaultMethod makes one saved method the one automatic renewal uses.
//
// Both statements run in one transaction because a partial unique index allows
// exactly one default per customer: clearing and setting separately would leave
// a window with none, and a renewal landing in that window would find nothing
// to charge.
func (store *Store) SetDefaultMethod(
	ctx context.Context, customerID, methodID string,
) error {
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx,
		`UPDATE payment_methods SET is_default = false, updated_at = now()
		 WHERE user_id = $1::uuid AND is_default`, customerID); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx,
		`UPDATE payment_methods SET is_default = true, updated_at = now()
		 WHERE id = $1::uuid AND user_id = $2::uuid AND status = 'active'`, methodID, customerID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		// The identifier came from callback data, which the customer controls.
		// Refusing here is what stops one customer naming another's method.
		return errors.New("payment method does not belong to this customer")
	}
	// Auto-renew follows the default, so the two cannot disagree about which
	// method a charge will use.
	if _, err = tx.Exec(ctx,
		`UPDATE auto_renew_settings SET payment_method_id = $1::uuid, updated_at = now()
		 WHERE user_id = $2::uuid AND funding = 'saved_method'`, methodID, customerID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RemoveMethod revokes a saved method and stops any renewal that depended on it.
//
// Revoking without touching auto-renew would leave a customer consented to a
// charge that can no longer be made, which surfaces later as a failed renewal
// they did nothing to cause. Instead the settings fall back to the wallet when
// another method remains, and auto-renew is suspended when none does — with the
// consent record intact, so re-adding a card does not require consenting again.
func (store *Store) RemoveMethod(ctx context.Context, customerID, methodID string) error {
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx,
		`UPDATE payment_methods
		 SET status = 'revoked', revoked_at = now(), is_default = false, updated_at = now()
		 WHERE id = $1::uuid AND user_id = $2::uuid AND status <> 'revoked'`, methodID, customerID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("payment method does not belong to this customer")
	}
	var remaining string
	err = tx.QueryRow(ctx,
		`SELECT id::text FROM payment_methods
		 WHERE user_id = $1::uuid AND status = 'active'
		 ORDER BY is_default DESC, created_at DESC LIMIT 1`, customerID).Scan(&remaining)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if remaining == "" {
		if _, err = tx.Exec(ctx,
			`UPDATE auto_renew_settings
			 SET funding = 'wallet', payment_method_id = NULL, state = 'suspended', updated_at = now()
			 WHERE user_id = $1::uuid AND funding = 'saved_method'`, customerID); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if _, err = tx.Exec(ctx,
		`UPDATE payment_methods SET is_default = true, updated_at = now()
		 WHERE id = $1::uuid AND NOT EXISTS (
			 SELECT 1 FROM payment_methods
			 WHERE user_id = $2::uuid AND is_default AND status = 'active')`,
		remaining, customerID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx,
		`UPDATE auto_renew_settings SET payment_method_id = $1::uuid, updated_at = now()
		 WHERE user_id = $2::uuid AND funding = 'saved_method'`, remaining, customerID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// SetRenewalFunding chooses what one subscription's automatic renewal is paid
// from.
//
// Choosing a saved method requires one to exist, because the table refuses the
// pair otherwise — and rightly: consenting to charge a card that is not there
// is not a state worth representing.
//
// The row is addressed by `(user_id, subscription_id)`, the table's key since
// v0.5. A saved method itself is customer-wide; which subscription spends it is
// not.
func (store *Store) SetRenewalFunding(
	ctx context.Context, customerID, subscriptionID, funding string,
) error {
	if funding != recurring.FundingWallet && funding != recurring.FundingSavedMethod {
		return errors.New("unsupported renewal funding")
	}
	if funding == recurring.FundingWallet {
		_, err := store.pool.Exec(ctx,
			`UPDATE auto_renew_settings
			 SET funding = 'wallet', payment_method_id = NULL, updated_at = now()
			 WHERE user_id = $1::uuid
			   AND subscription_id IS NOT DISTINCT FROM NULLIF($2, '')::uuid`, customerID, subscriptionID)
		return err
	}
	var methodID string
	err := store.pool.QueryRow(ctx,
		`SELECT id::text FROM payment_methods
		 WHERE user_id = $1::uuid AND status = 'active'
		 ORDER BY is_default DESC, created_at DESC LIMIT 1`, customerID).Scan(&methodID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNoSavedMethod
	}
	if err != nil {
		return err
	}
	_, err = store.pool.Exec(ctx,
		`UPDATE auto_renew_settings
		 SET funding = 'saved_method', payment_method_id = $1::uuid, updated_at = now()
		 WHERE user_id = $2::uuid
		   AND subscription_id IS NOT DISTINCT FROM NULLIF($3, '')::uuid`, methodID, customerID, subscriptionID)
	return err
}

// ErrNoSavedMethod is returned when a customer asks to renew from a card they
// have not saved. It is a normal outcome with its own message, not a fault.
var ErrNoSavedMethod = errors.New("no saved payment method")

// SetRenewalLeadTime sets how far ahead of expiry a renewal is attempted.
//
// The value is clamped by the same domain rule the worker reads, so a customer
// cannot configure a renewal a month early — that charges for a period they
// have not reached and may not want.
func (store *Store) SetRenewalLeadTime(
	ctx context.Context, customerID, subscriptionID string, lead time.Duration,
) error {
	seconds := int64(recurring.NormalizeLeadTime(lead).Seconds())
	_, err := store.pool.Exec(ctx,
		`UPDATE auto_renew_settings SET lead_time_seconds = $1, updated_at = now()
		 WHERE user_id = $2::uuid
		   AND subscription_id IS NOT DISTINCT FROM NULLIF($3, '')::uuid`, seconds, customerID, subscriptionID)
	return err
}

// RenewalSettings is the full auto-renew configuration of one subscription.
type RenewalSettings struct {
	AutoRenew
	Funding     string
	LeadTime    time.Duration
	State       string
	MethodLabel string
	Consented   bool
}

// RenewalSettings reads one subscription's auto-renew configuration and the
// label of whatever method it would charge.
func (store *Store) RenewalSettings(
	ctx context.Context, customerID, subscriptionID string,
) (RenewalSettings, error) {
	var (
		settings      RenewalSettings
		planVersionID pgtype.Text
		provider      pgtype.Text
		currency      pgtype.Text
		cancelledAt   pgtype.Timestamptz
		consentAt     pgtype.Timestamptz
		methodLabel   pgtype.Text
		leadSeconds   int64
	)
	err := store.pool.QueryRow(ctx,
		`SELECT a.enabled, a.plan_version_id::text, a.provider, a.currency, a.cancelled_at,
		        a.funding, a.lead_time_seconds, a.state, a.consent_at, m.display_label
		 FROM auto_renew_settings a
		 LEFT JOIN payment_methods m ON m.id = a.payment_method_id
		 WHERE a.user_id = $1::uuid
		   AND a.subscription_id IS NOT DISTINCT FROM NULLIF($2, '')::uuid`, customerID, subscriptionID).
		Scan(&settings.Enabled, &planVersionID, &provider, &currency, &cancelledAt,
			&settings.Funding, &leadSeconds, &settings.State, &consentAt, &methodLabel)
	if errors.Is(err, pgx.ErrNoRows) {
		// No row is the documented default: auto-renew is off, funded from the
		// wallet, with the standard lead time.
		return RenewalSettings{
			AutoRenew: AutoRenew{SubscriptionID: subscriptionID},
			Funding:   recurring.FundingWallet, LeadTime: recurring.DefaultLeadTime,
			State: recurring.StateIdle,
		}, nil
	}
	if err != nil {
		return RenewalSettings{}, err
	}
	settings.SubscriptionID = subscriptionID
	settings.PlanVersionID, settings.Provider, settings.Currency = planVersionID.String, provider.String, currency.String
	settings.CancelledAt = cancelledAt.Time
	settings.LeadTime = time.Duration(leadSeconds) * time.Second
	settings.MethodLabel = methodLabel.String
	settings.Consented = consentAt.Valid
	return settings, nil
}
