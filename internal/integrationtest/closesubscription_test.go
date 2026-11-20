//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"testing"

	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/fulfillment"
	"github.com/omniflow/omniflow/internal/panelpg"
)

// A customer closes their second subscription: its entitlement is retired,
// the slot frees up, and its Remnawave user is deleted under the installation's
// close mode. The first subscription cannot be closed, and a second close of
// the same one finds nothing.
func TestACustomerClosesAnAdditionalSubscription(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	checkout := newAccountCheckout(t, harness, store)
	harness.savePolicy(ctx, t, func(policy *panelpg.SubscriptionPolicy) { policy.RemnawaveCloseMode = "delete" })
	customerID := harness.customer(ctx, t)
	planVersionID := harness.catalog(ctx, t, "closable-plan", 0)
	memory := newMemoryRemnawave()
	worker := fulfillment.NewWorker(harness.pool, memory)

	var subscriptions []string
	for index, key := range []string{"close-first", "close-second"} {
		order, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
			CustomerID: customerID, PlanVersionID: planVersionID, Currency: "RUB",
			Operation: "purchase", IdempotencyKey: key, NewSubscription: index > 0,
		})
		if err != nil {
			t.Fatalf("buy %s: %v", key, err)
		}
		subscriptions = append(subscriptions, uuidText(order.SubscriptionID))
		var operationID string
		if err = harness.pool.QueryRow(ctx, `SELECT f.id::text FROM fulfillment_operations f
			JOIN entitlements e ON e.id = f.entitlement_id WHERE e.order_id = $1`, order.ID).Scan(&operationID); err != nil {
			t.Fatalf("read operation: %v", err)
		}
		if err = runFulfillment(ctx, worker, operationID); err != nil {
			t.Fatalf("provision %s: %v", key, err)
		}
	}
	if memory.userCount() != 2 {
		t.Fatalf("users = %d, want two", memory.userCount())
	}

	if err := checkout.CloseSubscription(ctx, customerID, subscriptions[0]); !errors.Is(err, commercepg.ErrPrimarySubscription) {
		t.Fatalf("closing the first subscription: %v, want subscription_primary", err)
	}
	if err := checkout.CloseSubscription(ctx, customerID, subscriptions[1]); err != nil {
		t.Fatalf("close the second: %v", err)
	}
	if err := checkout.CloseSubscription(ctx, customerID, subscriptions[1]); !errors.Is(err, commercepg.ErrSubscriptionUnknown) {
		t.Fatalf("a second close: %v, want subscription_unknown", err)
	}
	var live int
	if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM entitlements
		WHERE subscription_id = $1::uuid AND status <> 'superseded'`, subscriptions[1]).Scan(&live); err != nil {
		t.Fatalf("count entitlements: %v", err)
	}
	if live != 0 {
		t.Fatalf("%d entitlements of a closed subscription are still live", live)
	}
	var operationID string
	if err := harness.pool.QueryRow(ctx, `SELECT id::text FROM fulfillment_operations WHERE operation = 'close'`).Scan(&operationID); err != nil {
		t.Fatalf("read the close operation: %v", err)
	}
	if err := runFulfillment(ctx, worker, operationID); err != nil {
		t.Fatalf("close in Remnawave: %v", err)
	}
	if memory.userCount() != 1 {
		t.Fatalf("users after the close = %d, want the first only", memory.userCount())
	}
	var status string
	if err := harness.pool.QueryRow(ctx, `SELECT status FROM fulfillment_operations WHERE id = $1::uuid`, operationID).Scan(&status); err != nil || status != "succeeded" {
		t.Fatalf("close operation status = %q, %v", status, err)
	}
	// The freed slot takes another purchase under a ceiling of two.
	if _, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: planVersionID, Currency: "RUB",
		Operation: "purchase", IdempotencyKey: "close-third", NewSubscription: true,
	}); err != nil {
		t.Fatalf("the freed slot refused a purchase: %v", err)
	}
}
