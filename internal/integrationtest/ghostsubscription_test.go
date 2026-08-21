//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"testing"

	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/commercepg"
)

// TestACancelledOrderReleasesTheSubscriptionItOpened proves the ghost-row
// cleanup on both paths — an operator cancel and the expiry sweep — and that
// the cleanup never reaches a subscription something else still uses.
func TestACancelledOrderReleasesTheSubscriptionItOpened(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	store := commercepg.New(harness.pool, nil, testOptions())
	customerID := harness.customer(ctx, t)
	planVersionID := harness.catalog(ctx, t, "ghost-plan", 25000)

	countSubscriptions := func() int {
		t.Helper()
		var count int
		if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM subscriptions WHERE user_id = $1::uuid`, customerID).Scan(&count); err != nil {
			t.Fatalf("count subscriptions: %v", err)
		}
		return count
	}

	// The policy allows two concurrent subscriptions. Two unpaid orders fill
	// both slots with rows that have nothing behind them.
	first, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: planVersionID, Currency: "RUB",
		Operation: "purchase", IdempotencyKey: "ghost-first", NewSubscription: true,
	})
	if err != nil {
		t.Fatalf("first order: %v", err)
	}
	second, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: planVersionID, Currency: "RUB",
		Operation: "purchase", IdempotencyKey: "ghost-second", NewSubscription: true,
	})
	if err != nil {
		t.Fatalf("second order: %v", err)
	}
	if countSubscriptions() != 2 {
		t.Fatalf("expected two subscription rows, got %d", countSubscriptions())
	}
	if _, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: planVersionID, Currency: "RUB",
		Operation: "purchase", IdempotencyKey: "ghost-third", NewSubscription: true,
	}); !errors.Is(err, commerce.ErrSubscriptionRejected) {
		t.Fatalf("two ghosts must still fill the limit until released, got %v", err)
	}

	// An operator cancels the first: its subscription goes with it.
	if _, err := store.CancelOrder(ctx, uuidText(first.ID), "operator:ghost-first", "customer asked"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if countSubscriptions() != 1 {
		t.Fatalf("cancelling must release the subscription it opened, got %d rows", countSubscriptions())
	}
	var detached bool
	if err := harness.pool.QueryRow(ctx, `SELECT subscription_id IS NULL FROM orders WHERE id = $1`, first.ID).Scan(&detached); err != nil {
		t.Fatalf("read order: %v", err)
	}
	if !detached {
		t.Fatal("the cancelled order must no longer point at a deleted subscription")
	}

	// The second is paid: its subscription is real and stays, however many
	// times the cleanup runs.
	harness.settle(ctx, t, store, uuidText(second.ID), 25000, "event-ghost-second")
	if released, err := store.ReleaseGhostSubscriptions(ctx, customerID); err != nil || released != 0 {
		t.Fatalf("a subscription with an entitlement must never be released: %d %v", released, err)
	}
	if countSubscriptions() != 1 {
		t.Fatalf("the paid subscription disappeared")
	}

	// A third order opens a new slot and then expires through the sweep.
	third, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: planVersionID, Currency: "RUB",
		Operation: "purchase", IdempotencyKey: "ghost-expiring", NewSubscription: true,
	})
	if err != nil {
		t.Fatalf("third order: %v", err)
	}
	if _, err := harness.pool.Exec(ctx, `UPDATE orders SET expires_at = now() - interval '1 minute' WHERE id = $1`, third.ID); err != nil {
		t.Fatalf("age the order: %v", err)
	}
	if _, err := harness.pool.Exec(ctx, `UPDATE orders SET state = 'expired' WHERE id = $1`, third.ID); err != nil {
		t.Fatalf("expire the order: %v", err)
	}
	if released, err := store.ReleaseGhostSubscriptions(ctx, customerID); err != nil || released != 1 {
		t.Fatalf("an expired order's subscription must be released: %d %v", released, err)
	}
	if countSubscriptions() != 1 {
		t.Fatalf("expected only the paid subscription to remain, got %d", countSubscriptions())
	}
}
