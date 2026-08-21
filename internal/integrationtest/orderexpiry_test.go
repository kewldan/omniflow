//go:build integration

package integrationtest

import (
	"context"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/commercepg"
)

// TestAManualTransferGetsADaysLongPaymentWindow covers both ways the window
// reaches an order: named at creation, or extended when the provider is chosen
// afterwards. The extension never shortens a window and never reopens a closed
// order.
func TestAManualTransferGetsADaysLongPaymentWindow(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	store := commercepg.New(harness.pool, nil, testOptions())
	customerID := harness.customer(ctx, t)
	planVersionID := harness.catalog(ctx, t, "manual-plan", 25000)

	expiry := func(orderID any) time.Time {
		t.Helper()
		var at time.Time
		if err := harness.pool.QueryRow(ctx, `SELECT expires_at FROM orders WHERE id = $1`, orderID).Scan(&at); err != nil {
			t.Fatalf("read expiry: %v", err)
		}
		return at
	}

	named, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: planVersionID, Currency: "RUB",
		Operation: "purchase", IdempotencyKey: "manual-named", Provider: commerce.ManualProvider,
	})
	if err != nil {
		t.Fatalf("create order: %v", err)
	}
	if window := expiry(named.ID).Sub(named.CreatedAt.Time); window < commerce.ManualPaymentWindow-time.Minute {
		t.Fatalf("an order opened for a manual transfer got a %s window", window)
	}

	later, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: planVersionID, Currency: "RUB",
		Operation: "purchase", IdempotencyKey: "manual-later",
	})
	if err != nil {
		t.Fatalf("create order: %v", err)
	}
	before := expiry(later.ID)
	if window := before.Sub(later.CreatedAt.Time); window > commerce.DefaultPaymentWindow+time.Minute {
		t.Fatalf("an order with no provider yet got a %s window", window)
	}
	until := time.Now().Add(commerce.ManualPaymentWindow)
	if err := store.ExtendOrderExpiry(ctx, uuidText(later.ID), until); err != nil {
		t.Fatalf("extend: %v", err)
	}
	if got := expiry(later.ID); got.Before(until.Add(-time.Second)) {
		t.Fatalf("the window was not extended: %s", got)
	}
	// Shortening is refused silently: the longer deadline stands.
	if err := store.ExtendOrderExpiry(ctx, uuidText(later.ID), time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("extend shorter: %v", err)
	}
	if got := expiry(later.ID); got.Before(until.Add(-time.Second)) {
		t.Fatalf("a shorter deadline overwrote the longer one: %s", got)
	}
	// A closed order is never reopened.
	if _, err := harness.pool.Exec(ctx, `UPDATE orders SET state = 'cancelled' WHERE id = $1`, named.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	closedBefore := expiry(named.ID)
	if err := store.ExtendOrderExpiry(ctx, uuidText(named.ID), time.Now().Add(30*24*time.Hour)); err != nil {
		t.Fatalf("extend closed: %v", err)
	}
	if got := expiry(named.ID); !got.Equal(closedBefore) {
		t.Fatalf("a cancelled order's window moved from %s to %s", closedBefore, got)
	}
}
