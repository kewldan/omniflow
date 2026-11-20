//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"testing"

	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/commercepg"
)

// A customer pays to restart their traffic counter: the price is the plan's
// share, the wallet settles it once however often the key is replayed, and a
// single reset_traffic operation is queued. A plan that sells no resets, and
// unlimited traffic, refuse before any money moves.
func TestAPaidTrafficResetIsChargedOnceAndQueuedOnce(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	customerID := harness.customer(ctx, t)
	planVersionID := harness.catalog(ctx, t, "resettable-plan", 30000)
	harness.seedWallet(ctx, t, customerID, "reset-wallet", 60000)
	plan, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: planVersionID, Currency: "RUB",
		Operation: "purchase", IdempotencyKey: "reset-plan-order",
	})
	if err != nil {
		t.Fatalf("buy: %v", err)
	}
	subscriptionID := uuidText(plan.SubscriptionID)
	input := commercepg.TrafficResetOrderInput{CustomerID: customerID, SubscriptionID: subscriptionID, Currency: "RUB", IdempotencyKey: "reset-1"}
	if _, err = store.CreateTrafficResetOrder(ctx, input); !errors.Is(err, commerce.ErrTrafficResetUnavailable) {
		t.Fatalf("a plan that sells no resets: %v", err)
	}
	if _, err = harness.pool.Exec(ctx, `UPDATE plan_versions SET traffic_reset_price_bps = 5000 WHERE id = $1::uuid`, planVersionID); err != nil {
		t.Fatalf("sell resets: %v", err)
	}
	quote, err := store.TrafficResetQuote(ctx, customerID, subscriptionID, "RUB")
	if err != nil || !quote.Available || quote.AmountMinor != 15000 {
		t.Fatalf("quote = %+v, %v", quote, err)
	}
	for range 2 {
		order, orderErr := store.CreateTrafficResetOrder(ctx, input)
		if orderErr != nil || order.Operation != "traffic_reset" {
			t.Fatalf("reset order = %+v, %v", order, orderErr)
		}
	}
	if balance := harness.walletBalance(ctx, t, customerID); balance != 60000-30000-15000 {
		t.Fatalf("balance = %d, want one reset charged", balance)
	}
	var resets int
	if err = harness.pool.QueryRow(ctx, `SELECT count(*) FROM fulfillment_operations WHERE operation = 'reset_traffic'`).Scan(&resets); err != nil || resets != 1 {
		t.Fatalf("reset operations = %d, %v", resets, err)
	}

	if _, err = harness.pool.Exec(ctx, `UPDATE entitlements SET traffic_allowance_bytes = NULL WHERE subscription_id = $1::uuid`, subscriptionID); err != nil {
		t.Fatalf("make it unlimited: %v", err)
	}
	if _, err = store.CreateTrafficResetOrder(ctx, commercepg.TrafficResetOrderInput{
		CustomerID: customerID, SubscriptionID: subscriptionID, Currency: "RUB", IdempotencyKey: "reset-2",
	}); !errors.Is(err, commerce.ErrTrafficResetUnavailable) {
		t.Fatalf("a reset on unlimited traffic: %v", err)
	}
}
