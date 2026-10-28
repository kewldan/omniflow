//go:build integration

package integrationtest

import (
	"context"
	"testing"

	"github.com/omniflow/omniflow/internal/commercepg"
)

// TestTheAvailableWalletBalanceHasOneDefinition proves the store's balance —
// what order creation spends — and the customer panel's wallet screen are the
// same figures: credit reserved by an unpaid order is not available to a new
// one, and comes back when that order closes.
func TestTheAvailableWalletBalanceHasOneDefinition(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	store := commercepg.New(harness.pool, nil, testOptions())
	checkout := newAccountCheckout(t, harness, store)
	customerID := harness.customer(ctx, t)
	harness.creditWallet(ctx, t, customerID, "wallet-definition", 50000)
	planVersionID := harness.catalog(ctx, t, "wallet-definition", 80000)

	order, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: planVersionID, Currency: "RUB",
		Operation: "purchase", IdempotencyKey: "wallet-definition-order",
	})
	if err != nil {
		t.Fatalf("order: %v", err)
	}
	if order.WalletMinor != 50000 || order.State != "pending" {
		t.Fatalf("the order should reserve the whole wallet and wait for the rest: %+v", order)
	}

	check := func(stage string, wantReserved, wantAvailable int64) {
		t.Helper()
		balances, err := store.WalletBalances(ctx, customerID)
		if err != nil || len(balances) != 1 {
			t.Fatalf("%s: balances %+v %v", stage, balances, err)
		}
		balance := balances[0]
		if balance.Currency != "RUB" || balance.TotalMinor != 50000 || balance.ReservedMinor != wantReserved || balance.AvailableMinor != wantAvailable {
			t.Fatalf("%s: %+v, want reserved %d available %d", stage, balance, wantReserved, wantAvailable)
		}
		available, err := store.AvailableWalletBalance(ctx, customerID, "RUB")
		if err != nil || available != wantAvailable {
			t.Fatalf("%s: available %d %v, want %d", stage, available, err, wantAvailable)
		}
		if none, err := store.AvailableWalletBalance(ctx, customerID, "USD"); err != nil || none != 0 {
			t.Fatalf("%s: a currency never held must read zero, got %d %v", stage, none, err)
		}
		view, err := checkout.Wallet(ctx, customerID)
		if err != nil {
			t.Fatalf("%s: wallet view: %v", stage, err)
		}
		for _, shown := range view.Balances {
			if shown.Currency == "RUB" && shown != balance {
				t.Fatalf("%s: the panel shows %+v, the store spends %+v", stage, shown, balance)
			}
		}
	}
	check("while the order is pending", 50000, 0)
	if _, err := store.CancelOrder(ctx, uuidText(order.ID), "wallet-definition-cancel", "changed my mind"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	check("after the order closed", 0, 50000)
}
