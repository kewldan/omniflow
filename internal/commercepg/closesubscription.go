package commercepg

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

var (
	// ErrPrimarySubscription refuses closing a customer's first subscription:
	// it is the one the customer-level Remnawave mapping and every
	// single-subscription screen resolve to.
	ErrPrimarySubscription = errors.New("subscription_primary")
	// ErrSubscriptionBusy refuses closing a subscription an unpaid order still
	// targets: the payment would have nowhere to land.
	ErrSubscriptionBusy = errors.New("subscription_busy")
)

// CloseSubscription closes one of a customer's additional subscriptions at
// their own request. Whatever time it had left is given up — nothing is
// refunded — and its Remnawave user is disabled or deleted, as the
// installation's close mode says, through a fulfillment operation committed in
// the same transaction.
//
// Its entitlements are retired, so reconciliation, renewal, reminders, and
// every catalogue stop treating it as held, and the slot it occupied frees up
// for another purchase. A repeated call finds the subscription already closed
// and answers ErrSubscriptionUnknown.
func (store *Store) CloseSubscription(ctx context.Context, customerID, subscriptionID string) error {
	userID, err := parseUUID(customerID)
	if err != nil {
		return ErrSubscriptionUnknown
	}
	id, err := parseUUID(subscriptionID)
	if err != nil {
		return ErrSubscriptionUnknown
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := dbgen.New(tx)
	if err = queries.LockCustomerSubscriptions(ctx, customerID); err != nil {
		return err
	}
	subscription, err := queries.GetCustomerSubscription(ctx, dbgen.GetCustomerSubscriptionParams{SubscriptionID: id, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) || err == nil && subscription.Status != "active" {
		return ErrSubscriptionUnknown
	}
	if err != nil {
		return err
	}
	if subscription.Slot <= 1 {
		return ErrPrimarySubscription
	}
	open, err := queries.CountOpenSubscriptionOrders(ctx, id)
	if err != nil {
		return err
	}
	if open > 0 {
		return ErrSubscriptionBusy
	}
	policy, err := queries.GetSubscriptionPolicy(ctx)
	mode := "disable"
	if err == nil {
		mode = policy.RemnawaveCloseMode
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	retired, err := queries.RetireSubscriptionEntitlements(ctx, id)
	if err != nil {
		return err
	}
	if _, err = queries.CloseSubscription(ctx, dbgen.CloseSubscriptionParams{SubscriptionID: id, UserID: userID}); err != nil {
		return err
	}
	if subscription.RemnawaveUserID.Valid && len(retired) > 0 {
		desired, _ := json.Marshal(map[string]any{"closeMode": mode, "effectiveAt": store.clock().UTC()})
		operation, opErr := queries.CreateFulfillmentOperation(ctx, dbgen.CreateFulfillmentOperationParams{
			EntitlementID: latestRetired(retired).ID, Operation: "close",
			IdempotencyKey: "subscription:" + subscriptionID + ":close",
			CorrelationID:  "subscription:" + subscriptionID, DesiredState: desired,
		})
		if opErr != nil {
			return opErr
		}
		if err = store.enqueue(ctx, tx, uuidString(operation.ID)); err != nil {
			return err
		}
	}
	metadata, _ := json.Marshal(map[string]any{"subscriptionId": subscriptionID, "closeMode": mode, "slot": subscription.Slot})
	if _, err = queries.InsertAuditEvent(ctx, dbgen.InsertAuditEventParams{
		ActorType: "customer", ActorID: pgtype.Text{String: customerID, Valid: true},
		Action: "subscription.closed", Category: "customer", Outcome: "success",
		TargetType: "subscription", TargetID: subscriptionID, Metadata: metadata,
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// latestRetired is the entitlement that was in force: the one ending last.
func latestRetired(entitlements []dbgen.Entitlement) dbgen.Entitlement {
	latest := entitlements[0]
	for _, entitlement := range entitlements[1:] {
		if entitlement.EndsAt.Time.After(latest.EndsAt.Time) {
			latest = entitlement
		}
	}
	return latest
}
