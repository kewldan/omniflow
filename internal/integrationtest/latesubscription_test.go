//go:build integration

package integrationtest

import (
	"context"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/fulfillment"
)

// expireAndRelease closes an order the way the expiry sweep does and runs the
// ghost cleanup that follows it.
func (harness *harness) expireAndRelease(ctx context.Context, t *testing.T, store *commercepg.Store, customerID, orderID string) {
	t.Helper()
	if _, err := harness.pool.Exec(ctx, `UPDATE orders SET state = 'expired', expires_at = now() - interval '1 minute'
		WHERE id = $1::uuid`, orderID); err != nil {
		t.Fatalf("expire: %v", err)
	}
	if _, err := store.ReleaseGhostSubscriptions(ctx, customerID); err != nil {
		t.Fatalf("release: %v", err)
	}
	var detached bool
	if err := harness.pool.QueryRow(ctx, `SELECT subscription_id IS NULL FROM orders WHERE id = $1::uuid`, orderID).Scan(&detached); err != nil {
		t.Fatalf("read order: %v", err)
	}
	if !detached {
		t.Fatal("the ghost cleanup must have detached the expired order")
	}
}

// TestALatePaymentForAReleasedSubscriptionNeverLandsOnThePrimary reproduces
// the review's scenario end to end: a customer with a primary subscription and
// months left buys a second one by manual transfer, the order expires and its
// empty subscription is released, and the operator confirms the transfer
// afterwards. The thirty days must go to a subscription of their own, and the
// primary's expiry must not move — it used to be overwritten with now+30d.
func TestALatePaymentForAReleasedSubscriptionNeverLandsOnThePrimary(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	store := commercepg.New(harness.pool, nil, testOptions())
	customerID := harness.customer(ctx, t)
	panel := newMemoryRemnawave()
	worker := fulfillment.NewWorker(harness.pool, panel)

	// The primary subscription, provisioned, with three hundred days left.
	primaryOrder, _, _ := harness.paidOrder(ctx, t, store, customerID, "late-primary", 25000)
	harness.fulfillAll(ctx, t, worker)
	var primarySubscription string
	var primaryRemote int64
	if err := harness.pool.QueryRow(ctx, `UPDATE entitlements SET ends_at = now() + interval '300 days'
		WHERE order_id = $1::uuid RETURNING subscription_id::text, remnawave_user_id`, primaryOrder).Scan(&primarySubscription, &primaryRemote); err != nil {
		t.Fatalf("lengthen the primary: %v", err)
	}
	harness.fulfillAll(ctx, t, worker)
	var primaryEnd time.Time
	if err := harness.pool.QueryRow(ctx, `SELECT ends_at FROM entitlements WHERE order_id = $1::uuid`, primaryOrder).Scan(&primaryEnd); err != nil {
		t.Fatalf("read primary: %v", err)
	}
	primaryRemoteExpiry := panel.expiryOf(primaryRemote)

	// A second subscription bought by manual transfer, which then expires.
	planVersionID := harness.catalog(ctx, t, "late-second", 25000)
	second, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: planVersionID, Currency: "RUB",
		Operation: "purchase", IdempotencyKey: "late-second-order", NewSubscription: true, Provider: "manual",
	})
	if err != nil {
		t.Fatalf("second order: %v", err)
	}
	secondID := uuidText(second.ID)
	harness.expireAndRelease(ctx, t, store, customerID, secondID)

	// The operator confirms the transfer after the window.
	if classification := harness.settle(ctx, t, store, secondID, 25000, "late-second-approval"); classification != commerce.ClassificationLate {
		t.Fatalf("expected a late settlement, got %q", classification)
	}
	harness.fulfillAll(ctx, t, worker)

	var secondSubscription, secondStatus string
	var secondRemote int64
	if err := harness.pool.QueryRow(ctx, `SELECT e.subscription_id::text, e.status, e.remnawave_user_id
		FROM entitlements e JOIN orders o ON o.id = e.order_id AND o.subscription_id = e.subscription_id
		WHERE e.order_id = $1::uuid`, secondID).Scan(&secondSubscription, &secondStatus, &secondRemote); err != nil {
		t.Fatalf("the late order's entitlement must sit on the order's own subscription: %v", err)
	}
	if secondSubscription == primarySubscription {
		t.Fatal("the late payment was settled onto the primary subscription")
	}
	if secondStatus != "active" || secondRemote == primaryRemote || panel.userCount() != 2 {
		t.Fatalf("the second subscription must be its own Remnawave user: status %s, remote %d (primary %d), users %d",
			secondStatus, secondRemote, primaryRemote, panel.userCount())
	}
	var primaryStatus string
	var primaryEndAfter time.Time
	if err := harness.pool.QueryRow(ctx, `SELECT status, ends_at FROM entitlements WHERE order_id = $1::uuid`, primaryOrder).Scan(&primaryStatus, &primaryEndAfter); err != nil {
		t.Fatalf("read primary: %v", err)
	}
	if primaryStatus != "active" || !primaryEndAfter.Equal(primaryEnd) {
		t.Fatalf("the primary moved: status %s, end %s (was %s)", primaryStatus, primaryEndAfter, primaryEnd)
	}
	if expiry := panel.expiryOf(primaryRemote); !expiry.Equal(primaryRemoteExpiry) {
		t.Fatalf("the primary's Remnawave expiry moved from %s to %s", primaryRemoteExpiry, expiry)
	}
}

// TestALatePaymentWithNoRoomForItsSubscriptionGoesToAnOperator covers the
// other branch: when the concurrency policy will not open a subscription in
// place of the released one, the payment is not settled anywhere.
func TestALatePaymentWithNoRoomForItsSubscriptionGoesToAnOperator(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	options := testOptions()
	options.Subscriptions = commerce.SubscriptionPolicy{MultiEnabled: true, MaxPerCustomer: 1}
	store := commercepg.New(harness.pool, nil, options)
	customerID := harness.customer(ctx, t)

	planVersionID := harness.catalog(ctx, t, "late-full", 25000)
	stale, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: planVersionID, Currency: "RUB",
		Operation: "purchase", IdempotencyKey: "late-full-stale", Provider: "manual",
	})
	if err != nil {
		t.Fatalf("stale order: %v", err)
	}
	staleID := uuidText(stale.ID)
	harness.expireAndRelease(ctx, t, store, customerID, staleID)
	// The customer bought again in the meantime and holds the one slot.
	harness.paidOrder(ctx, t, store, customerID, "late-full-fresh", 25000)

	if classification := harness.settle(ctx, t, store, staleID, 25000, "late-full-approval"); classification != commerce.ClassificationSubscriptionUnavailable {
		t.Fatalf("expected %q, got %q", commerce.ClassificationSubscriptionUnavailable, classification)
	}
	var state string
	var entitlements, incidents, subscriptions int
	if err := harness.pool.QueryRow(ctx, `SELECT state FROM orders WHERE id = $1::uuid`, staleID).Scan(&state); err != nil {
		t.Fatalf("read order: %v", err)
	}
	if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM entitlements WHERE order_id = $1::uuid`, staleID).Scan(&entitlements); err != nil {
		t.Fatalf("count entitlements: %v", err)
	}
	if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM subscriptions WHERE user_id = $1::uuid`, customerID).Scan(&subscriptions); err != nil {
		t.Fatalf("count subscriptions: %v", err)
	}
	if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM operator_notifications
		WHERE kind = 'incident' AND payload ->> 'classification' = 'subscription_unavailable'`).Scan(&incidents); err != nil {
		t.Fatalf("count incidents: %v", err)
	}
	if state != "expired" || entitlements != 0 || subscriptions != 1 || incidents != 1 {
		t.Fatalf("order %s, %d entitlements, %d subscriptions, %d incidents; want expired, 0, 1, 1",
			state, entitlements, subscriptions, incidents)
	}
}

// TestAnEntitlementWithoutASubscriptionIsNeverAdopted proves the worker's half
// of the guard: an entitlement that names no subscription, behind an order
// that names none either, fails permanently instead of being provisioned onto
// the customer's primary subscription.
func TestAnEntitlementWithoutASubscriptionIsNeverAdopted(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	store := commercepg.New(harness.pool, nil, testOptions())
	customerID := harness.customer(ctx, t)
	orderID, _, operationID := harness.paidOrder(ctx, t, store, customerID, "orphan", 25000)
	if _, err := harness.pool.Exec(ctx, `UPDATE entitlements SET subscription_id = NULL WHERE order_id = $1::uuid`, orderID); err != nil {
		t.Fatalf("orphan entitlement: %v", err)
	}
	if _, err := harness.pool.Exec(ctx, `UPDATE orders SET subscription_id = NULL WHERE id = $1::uuid`, orderID); err != nil {
		t.Fatalf("orphan order: %v", err)
	}
	worker := fulfillment.NewWorker(harness.pool, untouchableProvisioner{t: t})
	_ = runFulfillment(ctx, worker, operationID)
	var status, code string
	if err := harness.pool.QueryRow(ctx, `SELECT status, COALESCE(last_error_code, '') FROM fulfillment_operations WHERE id = $1::uuid`, operationID).Scan(&status, &code); err != nil {
		t.Fatalf("read operation: %v", err)
	}
	if status != "failed" || code != "subscription_unresolved" {
		t.Fatalf("operation ended %s/%s, want failed/subscription_unresolved", status, code)
	}
}
