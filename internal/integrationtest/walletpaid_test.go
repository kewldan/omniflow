//go:build integration

package integrationtest

import (
	"context"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/commercepg"
)

// TestAWalletCoveredOrderIsPaidTheMomentItIsCreated pins paid_minor and paid_at
// to the creating insert. Before, both stayed 0 and NULL until the worker moved
// the order to fulfilled, so a wallet sale landed in the reporting period of
// its provisioning rather than its payment.
func TestAWalletCoveredOrderIsPaidTheMomentItIsCreated(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	store := commercepg.New(harness.pool, nil, testOptions())
	customerID := harness.customer(ctx, t)
	planVersionID := harness.catalog(ctx, t, "wallet-plan", 25000)

	topUp, err := store.CreateTopUpOrder(ctx, commercepg.TopUpInput{
		CustomerID: customerID, Currency: "RUB", AmountMinor: 25000, IdempotencyKey: "wallet-funding",
	})
	if err != nil {
		t.Fatalf("create top-up: %v", err)
	}
	harness.settle(ctx, t, store, uuidText(topUp.ID), 25000, "event-wallet-funding")

	before := time.Now().Add(-time.Minute)
	order, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: planVersionID, Currency: "RUB",
		Operation: "purchase", IdempotencyKey: "wallet-purchase",
	})
	if err != nil {
		t.Fatalf("create order: %v", err)
	}
	if order.State != "paid" || order.WalletMinor != 25000 || order.ExternalMinor != 0 {
		t.Fatalf("the wallet must cover the order: %+v", order)
	}
	if order.PaidMinor != 25000 {
		t.Fatalf("paid_minor at creation = %d, want the wallet amount", order.PaidMinor)
	}
	if !order.PaidAt.Valid || order.PaidAt.Time.Before(before) {
		t.Fatalf("paid_at at creation = %v, want now", order.PaidAt)
	}

	// A pending order is not paid by anything yet.
	pending, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: planVersionID, Currency: "RUB",
		Operation: "purchase", IdempotencyKey: "wallet-empty", NewSubscription: true,
	})
	if err != nil {
		t.Fatalf("create pending order: %v", err)
	}
	if pending.State != "pending" || pending.PaidMinor != 0 || pending.PaidAt.Valid {
		t.Fatalf("a pending order must not be marked paid: %+v", pending)
	}
}
