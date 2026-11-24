//go:build integration

package integrationtest

import (
	"context"
	"testing"

	"github.com/omniflow/omniflow/internal/accountcheckout"
	"github.com/omniflow/omniflow/internal/commercepg"
)

// A customer who bought a version keeps it when they extend from the web
// store or the bot's plan list (both open the checkout the same way), even
// after the operator publishes a newer version with fewer
// devices; a new customer gets the newer one.
func TestWebExtensionKeepsTheBoughtVersion(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	original := harness.catalog(ctx, t, "grandfather-plan", 1000)
	planID := harness.planID(ctx, t, original)

	customerID := harness.customer(ctx, t)
	harness.seedWallet(ctx, t, customerID, "grandfather-wallet", 10000)
	order, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: original, Currency: "RUB", Operation: "purchase",
		IdempotencyKey: "grandfather-purchase",
	})
	if err != nil {
		t.Fatalf("buy: %v", err)
	}

	// The operator publishes version 2 with one device.
	var newer string
	if err = harness.pool.QueryRow(ctx, `INSERT INTO plan_versions (plan_id, version, billing_period, duration_seconds, traffic_allowance_bytes, device_limit)
		VALUES ($1::uuid, 2, 'month', 2592000, 107374182400, 1) RETURNING id::text`, planID).Scan(&newer); err != nil {
		t.Fatalf("publish version 2: %v", err)
	}
	if _, err = harness.pool.Exec(ctx, `INSERT INTO plan_prices (plan_version_id, currency, amount_minor) VALUES ($1::uuid, 'RUB', 1000)`, newer); err != nil {
		t.Fatalf("price version 2: %v", err)
	}

	checkout := newAccountCheckout(t, harness, store)
	view, err := checkout.Open(ctx, customerID, "en", accountcheckout.OpenRequest{
		PlanVersionID: newer, Operation: "extension", SubscriptionID: uuidText(order.SubscriptionID),
	})
	if err != nil {
		t.Fatalf("open the extension: %v", err)
	}
	if view.PlanVersionID != original {
		t.Fatalf("the extension moved the customer to version %s, want their own %s", view.PlanVersionID, original)
	}

	newcomer := harness.customer(ctx, t)
	view, err = checkout.Open(ctx, newcomer, "en", accountcheckout.OpenRequest{PlanVersionID: newer, Operation: "purchase"})
	if err != nil {
		t.Fatalf("open a purchase: %v", err)
	}
	if view.PlanVersionID != newer {
		t.Fatalf("a new customer got version %s, want %s", view.PlanVersionID, newer)
	}
}
