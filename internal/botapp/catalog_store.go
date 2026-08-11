package botapp

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/commerce"
)

// Plan is one purchasable catalog entry, already localized and priced in the
// customer's settlement currency.
type Plan struct {
	PlanID                string
	PlanVersionID         string
	Code                  string
	Kind                  string
	Name                  string
	Description           string
	SortOrder             int32
	BillingPeriod         string
	Duration              time.Duration
	TrafficAllowanceBytes *int64
	DeviceLimit           *int32
	Currency              string
	AmountMinor           int64
	UpgradePolicy         string
	DowngradePolicy       string
	CancellationPolicy    string
	GracePeriod           time.Duration
	TrialRule             commerce.TrialRule
	RecurringCapable      bool
}

const planColumns = `p.id::text, p.code, p.kind, p.sort_order, l.name, l.description,
	v.id::text, v.billing_period, v.duration_seconds, v.traffic_allowance_bytes, v.device_limit,
	v.upgrade_policy, v.downgrade_policy, v.cancellation_policy, v.grace_period_seconds,
	v.trial_eligibility, v.recurring_capable, pr.currency, pr.amount_minor`

// Plans lists every visible plan that has a price in the requested currency.
// A plan without a price in that currency is omitted rather than shown as
// unavailable, because it cannot be bought there at all.
func (store *PostgresStore) Plans(ctx context.Context, locale Locale, currency string) ([]Plan, error) {
	rows, err := store.pool.Query(ctx, `SELECT `+planColumns+`
		FROM plans p
		JOIN plan_localizations l ON l.plan_id = p.id AND l.locale = $1
		JOIN LATERAL (
			SELECT * FROM plan_versions pv
			WHERE pv.plan_id = p.id AND pv.retired_at IS NULL
			ORDER BY pv.version DESC LIMIT 1
		) v ON true
		JOIN plan_prices pr ON pr.plan_version_id = v.id AND pr.currency = $2
		WHERE p.visible AND p.archived_at IS NULL
		ORDER BY p.sort_order, p.code`, string(locale), currency)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	plans := make([]Plan, 0, 8)
	for rows.Next() {
		plan, scanErr := scanPlan(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		plans = append(plans, plan)
	}
	return plans, rows.Err()
}

// Plan reads one plan version by identifier so a details screen and a checkout
// confirmation always describe the same catalog row.
func (store *PostgresStore) Plan(ctx context.Context, planVersionID string, locale Locale, currency string) (Plan, error) {
	plan, err := scanPlan(store.pool.QueryRow(ctx, `SELECT `+planColumns+`
		FROM plan_versions v
		JOIN plans p ON p.id = v.plan_id
		JOIN plan_localizations l ON l.plan_id = p.id AND l.locale = $2
		JOIN plan_prices pr ON pr.plan_version_id = v.id AND pr.currency = $3
		WHERE v.id = $1::uuid AND p.archived_at IS NULL`, planVersionID, string(locale), currency))
	if errors.Is(err, pgx.ErrNoRows) {
		return Plan{}, ErrPlanUnavailable
	}
	return plan, err
}

// ErrPlanUnavailable is returned when a plan version disappeared or lost its
// price between rendering a screen and acting on it.
var ErrPlanUnavailable = errors.New("plan is no longer available")

func scanPlan(row pgx.Row) (Plan, error) {
	var (
		plan            Plan
		durationSeconds int64
		graceSeconds    int64
		traffic         pgtype.Int8
		deviceLimit     pgtype.Int4
		trialRule       string
	)
	err := row.Scan(&plan.PlanID, &plan.Code, &plan.Kind, &plan.SortOrder, &plan.Name, &plan.Description,
		&plan.PlanVersionID, &plan.BillingPeriod, &durationSeconds, &traffic, &deviceLimit,
		&plan.UpgradePolicy, &plan.DowngradePolicy, &plan.CancellationPolicy, &graceSeconds,
		&trialRule, &plan.RecurringCapable, &plan.Currency, &plan.AmountMinor)
	if err != nil {
		return Plan{}, err
	}
	plan.Duration = time.Duration(durationSeconds) * time.Second
	plan.GracePeriod = time.Duration(graceSeconds) * time.Second
	plan.TrialRule = commerce.TrialRule(trialRule)
	if traffic.Valid {
		value := traffic.Int64
		plan.TrafficAllowanceBytes = &value
	}
	if deviceLimit.Valid {
		value := deviceLimit.Int32
		plan.DeviceLimit = &value
	}
	return plan, nil
}

// PlanPrices lists every currency a plan version is priced in. Payment-method
// selection needs it because the chosen adapter fixes the order currency.
func (store *PostgresStore) PlanPrices(ctx context.Context, planVersionID string) (map[string]int64, error) {
	rows, err := store.pool.Query(ctx, `SELECT currency, amount_minor FROM plan_prices
		WHERE plan_version_id = $1::uuid ORDER BY currency`, planVersionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	prices := make(map[string]int64, 4)
	for rows.Next() {
		var currency string
		var amount int64
		if err := rows.Scan(&currency, &amount); err != nil {
			return nil, err
		}
		prices[currency] = amount
	}
	return prices, rows.Err()
}

// Entitlement is the customer's current subscription as Omniflow records it.
type Entitlement struct {
	ID            string
	PlanName      string
	PlanCode      string
	PlanVersionID string
	Status        string
	StartsAt      time.Time
	EndsAt        time.Time
	GracePeriod   time.Duration
	Currency      string
	AmountMinor   int64
	Found         bool
	// SubscriptionID names the subscription this entitlement belongs to. It is
	// empty for an entitlement created before v0.5 that has not been adopted.
	SubscriptionID string
	// SubscriptionLabel is set when a notification has to name the subscription
	// unambiguously, which is whenever a customer holds more than one.
	SubscriptionLabel string
}

// Entitlement reads the customer's most recent non-superseded entitlement across
// every subscription. It is what a single-subscription installation uses.
func (store *PostgresStore) Entitlement(ctx context.Context, customerID string, locale Locale, currency string) (Entitlement, error) {
	return store.entitlement(ctx, customerID, "", locale, currency)
}

// EntitlementForSubscription reads the entitlement of one named subscription, so
// an alert, a renewal, or an add-on always acts on the right one.
func (store *PostgresStore) EntitlementForSubscription(ctx context.Context, customerID, subscriptionID string, locale Locale, currency string) (Entitlement, error) {
	return store.entitlement(ctx, customerID, subscriptionID, locale, currency)
}

func (store *PostgresStore) entitlement(ctx context.Context, customerID, subscriptionID string, locale Locale, currency string) (Entitlement, error) {
	var (
		entitlement  Entitlement
		graceSeconds int64
		planName     pgtype.Text
		price        pgtype.Int8
		subscription pgtype.Text
	)
	err := store.pool.QueryRow(ctx, `SELECT e.id::text, e.status, e.starts_at, e.ends_at,
		v.grace_period_seconds, v.id::text, p.code, l.name, pr.amount_minor,
		COALESCE(e.subscription_id::text, '')
		FROM entitlements e
		JOIN plan_versions v ON v.id = e.plan_version_id
		JOIN plans p ON p.id = v.plan_id
		LEFT JOIN plan_localizations l ON l.plan_id = p.id AND l.locale = $2
		LEFT JOIN plan_prices pr ON pr.plan_version_id = v.id AND pr.currency = $3
		WHERE e.user_id = $1::uuid AND e.status <> 'superseded'
		  AND ($4 = '' OR e.subscription_id = $4::uuid)
		ORDER BY e.ends_at DESC LIMIT 1`, customerID, string(locale), currency, subscriptionID).
		Scan(&entitlement.ID, &entitlement.Status, &entitlement.StartsAt, &entitlement.EndsAt,
			&graceSeconds, &entitlement.PlanVersionID, &entitlement.PlanCode, &planName, &price, &subscription)
	if errors.Is(err, pgx.ErrNoRows) {
		return Entitlement{}, nil
	}
	if err != nil {
		return Entitlement{}, err
	}
	entitlement.SubscriptionID = subscription.String
	entitlement.Found = true
	entitlement.GracePeriod = time.Duration(graceSeconds) * time.Second
	entitlement.PlanName = planName.String
	if entitlement.PlanName == "" {
		entitlement.PlanName = entitlement.PlanCode
	}
	entitlement.Currency, entitlement.AmountMinor = currency, price.Int64
	return entitlement, nil
}

// LatestFulfillmentStatus reports the status of the most recent provisioning
// attempt for an entitlement, or an empty string when none has been recorded.
func (store *PostgresStore) LatestFulfillmentStatus(ctx context.Context, entitlementID string) (string, error) {
	var status string
	err := store.pool.QueryRow(ctx, `SELECT status FROM fulfillment_operations
		WHERE entitlement_id = $1::uuid ORDER BY created_at DESC LIMIT 1`, entitlementID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return status, err
}

// WalletBalance reports the balance a new order may spend. It excludes credit
// already reserved by a draft or pending order.
func (store *PostgresStore) WalletBalance(ctx context.Context, customerID, currency string) (int64, error) {
	var balance int64
	err := store.pool.QueryRow(ctx, `SELECT GREATEST(
			COALESCE((SELECT sum(amount_minor) FROM ledger_entries
				WHERE account_type = 'customer_wallet' AND user_id = $1::uuid AND currency = $2), 0)
			- COALESCE((SELECT sum(wallet_minor) FROM orders
				WHERE user_id = $1::uuid AND currency = $2 AND state IN ('draft','pending')), 0), 0)::bigint`,
		customerID, currency).Scan(&balance)
	return balance, err
}

// WalletEntry is one customer-visible ledger movement.
type WalletEntry struct {
	Type        string
	AmountMinor int64
	Currency    string
	OccurredAt  time.Time
	Reason      string
}

// WalletHistory lists the customer's own ledger entries, newest first.
func (store *PostgresStore) WalletHistory(ctx context.Context, customerID, currency string, limit int) ([]WalletEntry, error) {
	rows, err := store.pool.Query(ctx, `SELECT t.type, e.amount_minor, e.currency, e.created_at, COALESCE(t.reason, '')
		FROM ledger_entries e JOIN ledger_transactions t ON t.id = e.transaction_id
		WHERE e.account_type = 'customer_wallet' AND e.user_id = $1::uuid AND e.currency = $2
		ORDER BY e.created_at DESC LIMIT $3`, customerID, currency, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := make([]WalletEntry, 0, limit)
	for rows.Next() {
		var entry WalletEntry
		if err := rows.Scan(&entry.Type, &entry.AmountMinor, &entry.Currency, &entry.OccurredAt, &entry.Reason); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

// TrialContext gathers the abuse-control inputs for a trial activation.
func (store *PostgresStore) TrialContext(ctx context.Context, customerID string) (commerce.TrialRequest, error) {
	var request commerce.TrialRequest
	var createdAt time.Time
	var settledOrders, activeEntitlements int64
	err := store.pool.QueryRow(ctx, `SELECT u.created_at,
		EXISTS (SELECT 1 FROM trial_claims c WHERE c.user_id = u.id),
		(SELECT count(*) FROM orders o WHERE o.user_id = u.id
			AND o.state IN ('paid','fulfilled','partially_refunded','refunded')),
		(SELECT count(*) FROM entitlements e WHERE e.user_id = u.id
			AND e.status IN ('pending','active','limited') AND e.ends_at > now()),
		EXISTS (
			SELECT 1 FROM identities mine
			JOIN identities other ON other.provider = mine.provider
				AND other.provider_subject = mine.provider_subject AND other.user_id <> mine.user_id
			JOIN trial_claims claimed ON claimed.user_id = other.user_id
			WHERE mine.user_id = u.id AND mine.status = 'active'
		)
		FROM users u WHERE u.id = $1::uuid`, customerID).
		Scan(&createdAt, &request.AlreadyClaimed, &settledOrders, &activeEntitlements, &request.SharedIdentitySignal)
	if err != nil {
		return commerce.TrialRequest{}, err
	}
	request.AccountAge = time.Since(createdAt)
	request.CompletedOrders = int(settledOrders)
	request.ActiveEntitlement = activeEntitlements > 0
	return request, nil
}

// AutoRenew is the customer's recurring-billing intent.
type AutoRenew struct {
	Enabled       bool
	PlanVersionID string
	Provider      string
	Currency      string
	CancelledAt   time.Time
}

// AutoRenew reads the customer's auto-renew intent.
func (store *PostgresStore) AutoRenew(ctx context.Context, customerID string) (AutoRenew, error) {
	var (
		setting       AutoRenew
		planVersionID pgtype.Text
		provider      pgtype.Text
		currency      pgtype.Text
		cancelledAt   pgtype.Timestamptz
	)
	err := store.pool.QueryRow(ctx, `SELECT enabled, plan_version_id::text, provider, currency, cancelled_at
		FROM auto_renew_settings WHERE user_id = $1::uuid`, customerID).
		Scan(&setting.Enabled, &planVersionID, &provider, &currency, &cancelledAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return AutoRenew{}, nil
	}
	if err != nil {
		return AutoRenew{}, err
	}
	setting.PlanVersionID, setting.Provider, setting.Currency = planVersionID.String, provider.String, currency.String
	setting.CancelledAt = cancelledAt.Time
	return setting, nil
}

// SetAutoRenew records or cancels the customer's auto-renew intent. Enabling it
// requires a plan, a provider, and a currency; cancelling only needs the
// customer, so a customer can always turn it off.
func (store *PostgresStore) SetAutoRenew(ctx context.Context, customerID string, setting AutoRenew) error {
	if !setting.Enabled {
		// Turning it off returns the state to idle so the worker stops looking at
		// the row, and clears the consent that made it chargeable. Saved methods
		// are deliberately left alone: withdrawing agreement to automatic
		// charging is not the same as asking for a card to be forgotten.
		_, err := store.pool.Exec(ctx, `INSERT INTO auto_renew_settings (user_id, enabled, cancelled_at)
			VALUES ($1::uuid, false, now())
			ON CONFLICT (user_id) DO UPDATE SET enabled = false, plan_version_id = NULL, provider = NULL,
				currency = NULL, cancelled_at = now(), consent_at = NULL, state = 'idle',
				updated_at = now()`, customerID)
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
			(user_id, enabled, plan_version_id, provider, currency, consent_at, state)
		VALUES ($1::uuid, true, $2::uuid, $3, $4, now(), 'scheduled')
		ON CONFLICT (user_id) DO UPDATE SET enabled = true, plan_version_id = EXCLUDED.plan_version_id,
			provider = EXCLUDED.provider, currency = EXCLUDED.currency, cancelled_at = NULL,
			consent_at = now(), state = 'scheduled', last_failure_code = NULL, updated_at = now()`,
		customerID, setting.PlanVersionID, setting.Provider, setting.Currency)
	return err
}

// ObservedState decodes the Remnawave observed state recorded on the customer's
// mapping. It never carries subscription links or tokens.
func (store *PostgresStore) ObservedState(ctx context.Context, customerID string) (map[string]any, error) {
	var raw []byte
	err := store.pool.QueryRow(ctx, `SELECT observed_state FROM remnawave_users WHERE user_id = $1::uuid`, customerID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	observed := map[string]any{}
	if err := json.Unmarshal(raw, &observed); err != nil {
		return nil, err
	}
	return observed, nil
}
