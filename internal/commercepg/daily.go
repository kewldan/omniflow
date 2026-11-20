package commercepg

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	databaseutil "github.com/omniflow/omniflow/internal/database"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// Daily plans (Bedolaga's daily subscriptions). A version with daily billing
// is a one-day version the worker renews from the wallet one day at a time
// (internal/renewal), and the one kind of subscription a customer may pause
// themselves: a pause stops the charges and disables access through
// fulfillment, a resume gives back the part of the day the pause held and
// re-enables it.

var (
	// ErrNotDaily refuses a pause or resume of a subscription whose plan is
	// not billed by the day. A pause of a dated period stays an operator's
	// decision (internal/httpapi/adminsubscriptions.go says why).
	ErrNotDaily = errors.New("not_daily")
	// ErrNotPausable refuses a pause of a subscription that is not running,
	// or a resume of one that is not paused.
	ErrNotPausable = errors.New("not_pausable")
)

// DailyStatus is what the customer sees of a daily plan.
type DailyStatus struct {
	// Daily reports whether the subscription's plan is billed by the day; the
	// other fields mean nothing when it is not.
	Daily bool
	// Paused is a pause the customer (or an operator) started; Suspended is
	// a day the wallet did not cover, which resumes on its own once it does.
	Paused, Suspended bool
	// EndsAt is when the paid day runs out, or would have for a paused one.
	EndsAt     time.Time
	PriceMinor int64
	Currency   string
}

// DailyBillingStatus reads the daily billing state of one of the customer's
// subscriptions.
func (store *Store) DailyBillingStatus(ctx context.Context, customerID, subscriptionID string) (DailyStatus, error) {
	status := DailyStatus{}
	var (
		daily    bool
		state    string
		endsAt   time.Time
		currency string
		price    pgtype.Int8
	)
	err := store.pool.QueryRow(ctx, `SELECT v.daily_billing, e.status, e.ends_at, o.currency, p.amount_minor
		FROM entitlements e
		JOIN subscriptions s ON s.id = e.subscription_id AND s.user_id = $1::uuid AND s.status = 'active'
		JOIN plan_versions v ON v.id = e.plan_version_id
		JOIN orders o ON o.id = e.order_id
		LEFT JOIN plan_prices p ON p.plan_version_id = v.id AND p.currency = o.currency
		WHERE e.subscription_id = $2::uuid AND e.status <> 'superseded'
		ORDER BY e.ends_at DESC LIMIT 1`, customerID, subscriptionID).Scan(&daily, &state, &endsAt, &currency, &price)
	if errors.Is(err, pgx.ErrNoRows) {
		return status, nil
	}
	if err != nil {
		return DailyStatus{}, err
	}
	status.Daily = daily
	status.Paused = state == "paused"
	status.Suspended = !status.Paused && !endsAt.After(store.clock())
	status.EndsAt, status.Currency, status.PriceMinor = endsAt.UTC(), currency, price.Int64
	return status, nil
}

// SetDailyPause pauses or resumes one of the customer's daily subscriptions.
//
// It is one transaction for the same reason an operator's pause is: the
// entitlement's clock stops here, and the Remnawave user is disabled by the
// operation this queues, so neither half can commit without the other. A
// pause of a paused subscription, or a resume of a running one, is the
// outcome already reached and succeeds without doing anything, so a retried
// request is harmless; `key` makes the queued operation idempotent.
func (store *Store) SetDailyPause(ctx context.Context, customerID, subscriptionID string, pause bool, key string) error {
	userID, err := parseUUID(customerID)
	if err != nil {
		return ErrSubscriptionUnknown
	}
	subscription, err := parseUUID(subscriptionID)
	if err != nil {
		return ErrSubscriptionUnknown
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := dbgen.New(tx)
	current, err := queries.GetLatestEntitlementForChange(ctx, dbgen.GetLatestEntitlementForChangeParams{
		UserID: userID, SubscriptionID: subscription,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotPausable
	}
	if err != nil {
		return err
	}
	var daily bool
	if err = tx.QueryRow(ctx, `SELECT daily_billing FROM plan_versions WHERE id = $1`, current.PlanVersionID).Scan(&daily); err != nil {
		return err
	}
	if !daily {
		return ErrNotDaily
	}
	operation := "resume"
	var changed dbgen.Entitlement
	switch {
	case pause && current.Status == "paused", !pause && current.Status != "paused":
		if !pause && !current.EndsAt.Time.After(store.clock()) {
			// A day the wallet did not cover is not paused; it resumes on
			// its own once the wallet covers the next one.
			return ErrNotPausable
		}
		return tx.Commit(ctx)
	case pause:
		operation = "pause"
		changed, err = queries.PauseEntitlement(ctx, current.ID)
	default:
		changed, err = queries.ResumeEntitlement(ctx, current.ID)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotPausable
	}
	if err != nil {
		return err
	}
	desired, err := json.Marshal(map[string]any{
		"effectiveAt": store.clock().UTC(), "endsAt": changed.EndsAt.Time,
		"trafficAllowanceBytes": nullableInt8(changed.TrafficAllowanceBytes),
		"deviceLimit":           nullableInt4(changed.DeviceLimit),
		"squadIds":              databaseutil.UUIDStrings(changed.RemnawaveSquadIds),
	})
	if err != nil {
		return err
	}
	created, err := queries.CreateFulfillmentOperation(ctx, dbgen.CreateFulfillmentOperationParams{
		EntitlementID: changed.ID, Operation: operation,
		IdempotencyKey: "daily-" + operation + ":" + key, CorrelationID: "daily:" + key,
		DesiredState: desired,
	})
	if err != nil {
		return err
	}
	if err = store.enqueue(ctx, tx, uuidString(created.ID)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
