package botapp

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/accountcheckout"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// Plan is one purchasable catalog entry, already localized and priced in the
// customer's settlement currency.
type Plan struct {
	PlanID        string
	PlanVersionID string
	Code          string
	Kind          string
	Name          string
	Description   string
	// Highlighted marks the operator's best-deal plan.
	Highlighted           bool
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
	` + accountcheckout.EffectivePolicyColumns + `, v.cancellation_policy, v.grace_period_seconds,
	v.trial_eligibility, v.recurring_capable, pr.currency, pr.amount_minor, p.highlighted`

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
		&trialRule, &plan.RecurringCapable, &plan.Currency, &plan.AmountMinor, &plan.Highlighted)
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
	ID       string
	PlanName string
	PlanCode string
	// PlanID is the plan, as opposed to the plan version, behind the
	// entitlement. Offering an extension has to compare plans: a customer on
	// version 1 of a plan still holds that plan after version 2 is published.
	PlanID        string
	PlanVersionID string
	Status        string
	StartsAt      time.Time
	EndsAt        time.Time
	GracePeriod   time.Duration
	Currency      string
	AmountMinor   int64
	Found         bool
	// SubscriptionID names the subscription this entitlement belongs to. It is
	// empty for an entitlement whose subscription has been removed.
	SubscriptionID string
	// SubscriptionLabel is set when a notification has to name the subscription
	// unambiguously, which is whenever a customer holds more than one.
	SubscriptionLabel string
	// PlanKind is the kind of plan behind the entitlement; a trial gets the
	// installation's hours-before-the-end warning.
	PlanKind string
	// TrialWarningHours is the installation's trial warning window; zero sends
	// nothing.
	TrialWarningHours int
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
		v.grace_period_seconds, v.id::text, p.id::text, p.code, l.name, pr.amount_minor,
		COALESCE(e.subscription_id::text, ''), p.kind,
		COALESCE((SELECT sp.trial_warning_hours FROM subscription_policy sp WHERE sp.singleton), 0)
		FROM entitlements e
		JOIN plan_versions v ON v.id = e.plan_version_id
		JOIN plans p ON p.id = v.plan_id
		LEFT JOIN plan_localizations l ON l.plan_id = p.id AND l.locale = $2
		LEFT JOIN plan_prices pr ON pr.plan_version_id = v.id AND pr.currency = $3
		WHERE e.user_id = $1::uuid AND e.status <> 'superseded'
		  AND ($4 = '' OR e.subscription_id = $4::uuid)
		ORDER BY (e.starts_at <= now()) DESC, e.ends_at DESC LIMIT 1`, customerID, string(locale), currency, subscriptionID).
		Scan(&entitlement.ID, &entitlement.Status, &entitlement.StartsAt, &entitlement.EndsAt,
			&graceSeconds, &entitlement.PlanVersionID, &entitlement.PlanID, &entitlement.PlanCode, &planName, &price, &subscription,
			&entitlement.PlanKind, &entitlement.TrialWarningHours)
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
// already reserved by a draft or pending order. Both figures come from the one
// query that defines a wallet for every surface, so the bot cannot show a
// spendable amount the web or order creation would disagree with.
func (store *PostgresStore) WalletBalance(ctx context.Context, customerID, currency string) (int64, error) {
	row, err := store.walletBalance(ctx, customerID, currency)
	return row.AvailableMinor, err
}

// WalletReserved is the part of the ledger balance that unpaid orders have
// already claimed. The wallet screen prints it beside the spendable balance,
// because a balance that silently shrank while an order waits for payment
// reads as money gone missing.
func (store *PostgresStore) WalletReserved(ctx context.Context, customerID, currency string) (int64, error) {
	row, err := store.walletBalance(ctx, customerID, currency)
	return row.ReservedMinor, err
}

func (store *PostgresStore) walletBalance(ctx context.Context, customerID, currency string) (dbgen.GetWalletBalancesRow, error) {
	var userID pgtype.UUID
	if err := userID.Scan(customerID); err != nil {
		return dbgen.GetWalletBalancesRow{}, err
	}
	rows, err := dbgen.New(store.pool).GetWalletBalances(ctx, dbgen.GetWalletBalancesParams{
		UserID: userID, Currency: pgtype.Text{String: currency, Valid: true},
	})
	if err != nil || len(rows) == 0 {
		return dbgen.GetWalletBalancesRow{Currency: currency}, err
	}
	return rows[0], nil
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
	var methods, disabled []string
	err := store.pool.QueryRow(ctx, `SELECT u.created_at,
		EXISTS (SELECT 1 FROM trial_claims c LEFT JOIN orders co ON co.id = c.order_id
			WHERE c.user_id = u.id AND (c.order_id IS NULL OR co.state NOT IN ('cancelled', 'expired'))),
		(SELECT count(*) FROM orders o WHERE o.user_id = u.id
			AND o.state IN ('paid','fulfilled','partially_refunded','refunded')),
		(SELECT count(*) FROM entitlements e WHERE e.user_id = u.id
			AND e.status IN ('pending','active','limited') AND e.ends_at > now()),
		EXISTS (
			SELECT 1 FROM identities mine
			JOIN identities other ON other.provider = mine.provider
				AND other.provider_subject = mine.provider_subject AND other.user_id <> mine.user_id
			JOIN trial_claims claimed ON claimed.user_id = other.user_id
			LEFT JOIN orders claimed_order ON claimed_order.id = claimed.order_id
			WHERE mine.user_id = u.id AND mine.status = 'active'
				AND (claimed.order_id IS NULL OR claimed_order.state NOT IN ('cancelled', 'expired'))
		),
		`+accountcheckout.TrialSignInColumns+`
		FROM users u WHERE u.id = $1::uuid`, customerID).
		Scan(&createdAt, &request.AlreadyClaimed, &settledOrders, &activeEntitlements, &request.SharedIdentitySignal,
			&methods, &disabled)
	if err != nil {
		return commerce.TrialRequest{}, err
	}
	request.AccountAge = time.Since(createdAt)
	request.CompletedOrders = int(settledOrders)
	request.ActiveEntitlement = activeEntitlements > 0
	request.SignInMethodExcluded = commerce.TrialSignInMethodExcluded(methods, disabled)
	return request, nil
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
