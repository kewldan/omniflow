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

// SubscriptionSummary is one of a customer's subscriptions, with just enough of
// its current entitlement to render a picker row without a second query.
type SubscriptionSummary struct {
	ID          string
	Slot        int
	Label       string
	RemnawaveID int64
	PlanName    string
	// PlanID is the plan behind the current entitlement, so a purchase of the
	// same plan can recognise an expired subscription it should revive instead
	// of opening a slot beside it.
	PlanID      string
	Status      string
	EndsAt      time.Time
	GracePeriod time.Duration
	Found       bool
}

// Provisioned reports whether Remnawave already holds a user for this
// subscription.
func (summary SubscriptionSummary) Provisioned() bool { return summary.RemnawaveID > 0 }

const subscriptionColumns = `s.id::text, s.slot, s.label, COALESCE(s.remnawave_user_id, 0),
	COALESCE(l.name, p.code, ''), COALESCE(p.id::text, ''), COALESCE(e.status, ''), e.ends_at, COALESCE(v.grace_period_seconds, 0)`

const subscriptionJoins = `FROM subscriptions s
	LEFT JOIN LATERAL (
		SELECT * FROM entitlements ent
		WHERE ent.subscription_id = s.id AND ent.status <> 'superseded'
		ORDER BY ent.ends_at DESC LIMIT 1
	) e ON true
	LEFT JOIN plan_versions v ON v.id = e.plan_version_id
	LEFT JOIN plans p ON p.id = v.plan_id
	LEFT JOIN plan_localizations l ON l.plan_id = p.id AND l.locale = $2`

// Subscriptions lists the customer's active subscriptions in slot order. A
// single-subscription installation always gets exactly one row, which is what
// keeps the one-screen experience unchanged.
func (store *PostgresStore) Subscriptions(ctx context.Context, customerID string, locale Locale) ([]SubscriptionSummary, error) {
	rows, err := store.pool.Query(ctx, `SELECT `+subscriptionColumns+` `+subscriptionJoins+`
		WHERE s.user_id = $1::uuid AND s.status = 'active' ORDER BY s.slot`, customerID, string(locale))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	summaries := make([]SubscriptionSummary, 0, 4)
	for rows.Next() {
		summary, scanErr := scanSubscription(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		summaries = append(summaries, summary)
	}
	return summaries, rows.Err()
}

// ErrSubscriptionNotFound is returned when a subscription identifier from
// callback data does not belong to the caller.
var ErrSubscriptionNotFound = errors.New("subscription not found")

// Subscription reads one of the customer's own subscriptions. Ownership is part
// of the query, so an identifier taken from callback data cannot address someone
// else's subscription.
func (store *PostgresStore) Subscription(ctx context.Context, customerID, subscriptionID string, locale Locale) (SubscriptionSummary, error) {
	summary, err := scanSubscription(store.pool.QueryRow(ctx, `SELECT `+subscriptionColumns+` `+subscriptionJoins+`
		WHERE s.id = $3::uuid AND s.user_id = $1::uuid AND s.status = 'active'`, customerID, string(locale), subscriptionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return SubscriptionSummary{}, ErrSubscriptionNotFound
	}
	return summary, err
}

// SubscriptionBySlot reads one of the customer's own subscriptions by its slot
// number. Callback data is capped at 64 bytes by Telegram, and an action that
// has to name both an add-on version and a subscription cannot fit two UUIDs;
// the slot is one or two digits and is unique per customer.
func (store *PostgresStore) SubscriptionBySlot(ctx context.Context, customerID string, slot int, locale Locale) (SubscriptionSummary, error) {
	summary, err := scanSubscription(store.pool.QueryRow(ctx, `SELECT `+subscriptionColumns+` `+subscriptionJoins+`
		WHERE s.user_id = $1::uuid AND s.slot = $3 AND s.status = 'active'`, customerID, string(locale), slot))
	if errors.Is(err, pgx.ErrNoRows) {
		return SubscriptionSummary{}, ErrSubscriptionNotFound
	}
	return summary, err
}

// PrimarySubscription is the lowest-slot active subscription, which is the one a
// single-subscription installation always uses.
func (store *PostgresStore) PrimarySubscription(ctx context.Context, customerID string, locale Locale) (SubscriptionSummary, error) {
	summary, err := scanSubscription(store.pool.QueryRow(ctx, `SELECT `+subscriptionColumns+` `+subscriptionJoins+`
		WHERE s.user_id = $1::uuid AND s.status = 'active' ORDER BY s.slot LIMIT 1`, customerID, string(locale)))
	if errors.Is(err, pgx.ErrNoRows) {
		return SubscriptionSummary{}, ErrSubscriptionNotFound
	}
	return summary, err
}

func scanSubscription(row pgx.Row) (SubscriptionSummary, error) {
	var (
		summary      SubscriptionSummary
		slot         int32
		endsAt       pgtype.Timestamptz
		graceSeconds int64
	)
	err := row.Scan(&summary.ID, &slot, &summary.Label, &summary.RemnawaveID,
		&summary.PlanName, &summary.PlanID, &summary.Status, &endsAt, &graceSeconds)
	if err != nil {
		return SubscriptionSummary{}, err
	}
	summary.Slot = int(slot)
	summary.EndsAt = endsAt.Time
	summary.GracePeriod = time.Duration(graceSeconds) * time.Second
	summary.Found = summary.Status != ""
	return summary, nil
}

// Phase reduces a subscription to the phase the bot explains on screen.
func (summary SubscriptionSummary) Phase(now time.Time, usedBytes, limitBytes int64) commerce.SubscriptionPhase {
	return commerce.EvaluatePhase(now, commerce.Subscription{
		Status: summary.Status, EndsAt: summary.EndsAt, GracePeriod: summary.GracePeriod,
		TrafficUsedBytes: usedBytes, TrafficLimitBytes: limitBytes,
	})
}

// RenameSubscription stores the customer's own label for a subscription. The
// label is what every screen and notification uses to name it.
func (store *PostgresStore) RenameSubscription(ctx context.Context, customerID, subscriptionID, label string) error {
	normalized, err := commerce.NormalizeSubscriptionLabel(label)
	if err != nil {
		return err
	}
	tag, err := store.pool.Exec(ctx, `UPDATE subscriptions SET label = $3, updated_at = now()
		WHERE id = $2::uuid AND user_id = $1::uuid AND status = 'active'`, customerID, subscriptionID, normalized)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrSubscriptionNotFound
	}
	return nil
}

// SelectableSquad is one squad a customer may add to a plan.
type SelectableSquad struct {
	SquadID string
	Label   string
}

// PlanSquadPolicy is the configurator input for one plan version.
type PlanSquadPolicy struct {
	Selection string
	Minimum   int
	Maximum   *int
	Offered   []SelectableSquad
}

// Configurable reports whether the customer has any choice to make.
func (policy PlanSquadPolicy) Configurable() bool {
	return policy.Selection != "" && policy.Selection != "automatic" && len(policy.Offered) > 0
}

// PlanSquads reads the squad configurator for one plan version.
func (store *PostgresStore) PlanSquads(ctx context.Context, planVersionID string, locale Locale) (PlanSquadPolicy, error) {
	var (
		policy  PlanSquadPolicy
		minimum int32
		maximum pgtype.Int4
	)
	err := store.pool.QueryRow(ctx, `SELECT squad_selection, min_selectable_squads, max_selectable_squads
		FROM plan_versions WHERE id = $1::uuid`, planVersionID).Scan(&policy.Selection, &minimum, &maximum)
	if errors.Is(err, pgx.ErrNoRows) {
		return PlanSquadPolicy{}, ErrPlanUnavailable
	}
	if err != nil {
		return PlanSquadPolicy{}, err
	}
	policy.Minimum = int(minimum)
	if maximum.Valid {
		value := int(maximum.Int32)
		policy.Maximum = &value
	}
	labelColumn := "label_en"
	if locale == LocaleRussian {
		labelColumn = "label_ru"
	}
	rows, err := store.pool.Query(ctx, `SELECT squad_id::text, `+labelColumn+`
		FROM plan_version_squads WHERE plan_version_id = $1::uuid ORDER BY sort_order, squad_id`, planVersionID)
	if err != nil {
		return PlanSquadPolicy{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var squad SelectableSquad
		if err = rows.Scan(&squad.SquadID, &squad.Label); err != nil {
			return PlanSquadPolicy{}, err
		}
		policy.Offered = append(policy.Offered, squad)
	}
	return policy, rows.Err()
}

// Addon is one purchasable add-on, localized and priced in the checkout
// currency.
type Addon struct {
	AddonID        string
	AddonVersionID string
	Code           string
	Kind           string
	Name           string
	Description    string
	TrafficBytes   *int64
	DeviceSlots    *int32
	SquadCount     int
	MaxQuantity    int
	Proration      commerce.Proration
	Currency       string
	AmountMinor    int64
}

// PlanAddons lists the add-ons offered for a plan version in one currency.
func (store *PostgresStore) PlanAddons(ctx context.Context, planVersionID string, locale Locale, currency string) ([]Addon, error) {
	rows, err := store.pool.Query(ctx, `SELECT a.id::text, v.id::text, a.code, a.kind, l.name, l.description,
		v.traffic_bytes, v.device_slots, cardinality(v.remnawave_squad_ids), v.max_quantity, v.proration,
		pr.currency, pr.amount_minor
		FROM plan_version_addons pa
		JOIN addons a ON a.id = pa.addon_id
		JOIN addon_localizations l ON l.addon_id = a.id AND l.locale = $2
		JOIN LATERAL (
			SELECT * FROM addon_versions av
			WHERE av.addon_id = a.id AND av.retired_at IS NULL
			ORDER BY av.version DESC LIMIT 1
		) v ON true
		JOIN addon_prices pr ON pr.addon_version_id = v.id AND pr.currency = $3
		WHERE pa.plan_version_id = $1::uuid AND a.visible AND a.archived_at IS NULL
		ORDER BY a.sort_order, a.code`, planVersionID, string(locale), currency)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	addons := make([]Addon, 0, 6)
	for rows.Next() {
		var (
			addon       Addon
			traffic     pgtype.Int8
			slots       pgtype.Int4
			squadCount  int32
			maxQuantity int32
			proration   string
		)
		if err = rows.Scan(&addon.AddonID, &addon.AddonVersionID, &addon.Code, &addon.Kind, &addon.Name,
			&addon.Description, &traffic, &slots, &squadCount, &maxQuantity, &proration,
			&addon.Currency, &addon.AmountMinor); err != nil {
			return nil, err
		}
		if traffic.Valid {
			value := traffic.Int64
			addon.TrafficBytes = &value
		}
		if slots.Valid {
			value := slots.Int32
			addon.DeviceSlots = &value
		}
		addon.SquadCount, addon.MaxQuantity, addon.Proration = int(squadCount), int(maxQuantity), commerce.Proration(proration)
		addons = append(addons, addon)
	}
	return addons, rows.Err()
}

// RecordAuditEvent appends an operator action to the append-only audit trail.
func (store *PostgresStore) RecordAuditEvent(ctx context.Context, actorID, action, targetType, targetID, reason string) error {
	metadata, err := json.Marshal(map[string]any{"channel": "telegram_bot"})
	if err != nil {
		return err
	}
	_, err = store.pool.Exec(ctx, `INSERT INTO audit_events
		(actor_type, actor_id, action, target_type, target_id, reason, metadata)
		VALUES ('operator', $1, $2, $3, $4, $5, $6)`, actorID, action, targetType, targetID, reason, metadata)
	return err
}

// Maintenance reads the customer-facing maintenance notice.
func (store *PostgresStore) Maintenance(ctx context.Context) (commerce.Maintenance, error) {
	var (
		state    commerce.Maintenance
		expected pgtype.Timestamptz
	)
	err := store.pool.QueryRow(ctx, `SELECT active, source, reason, notice_ru, notice_en, expected_return_at
		FROM maintenance_state WHERE singleton`).
		Scan(&state.Active, &state.Source, &state.Reason, &state.NoticeRU, &state.NoticeEN, &expected)
	if errors.Is(err, pgx.ErrNoRows) {
		return commerce.Maintenance{}, nil
	}
	if err != nil {
		return commerce.Maintenance{}, err
	}
	state.ExpectedReturnAt = expected.Time
	return state, nil
}
