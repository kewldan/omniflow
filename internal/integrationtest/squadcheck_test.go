//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"testing"

	"github.com/omniflow/omniflow/internal/commercepg"
)

// An order for a squad the Remnawave panel no longer has is refused before
// any money moves; a panel that does not answer does not block the sale.
func TestAnOrderForADeletedSquadIsRefusedBeforeCharging(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	customerID := harness.customer(ctx, t)
	planVersionID := harness.catalog(ctx, t, "squad-check-plan", 1000)
	squad := "55555555-5555-4555-8555-555555555555"
	if _, err := harness.pool.Exec(ctx, `UPDATE plan_versions SET remnawave_squad_ids = ARRAY[$2::uuid] WHERE id = $1::uuid`, planVersionID, squad); err != nil {
		t.Fatalf("give the plan a squad: %v", err)
	}
	harness.seedWallet(ctx, t, customerID, "squad-check-wallet", 1000)
	panelHas := []string{"66666666-6666-4666-8666-666666666666"}
	var panelDown error
	store.SetSquadLister(func(context.Context) ([]string, error) { return panelHas, panelDown })

	input := commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: planVersionID, Currency: "RUB",
		Operation: "purchase", IdempotencyKey: "squad-check-order",
	}
	if _, err := store.CreateOrder(ctx, input); !errors.Is(err, commercepg.ErrSquadUnavailable) {
		t.Fatalf("an order for a deleted squad: %v, want squad_unavailable", err)
	}
	if balance := harness.walletBalance(ctx, t, customerID); balance != 1000 {
		t.Fatalf("the wallet moved to %d", balance)
	}

	// A fresh store reads the panel again rather than the minute-long cache.
	store = harness.commerceStore(t, testOptions())
	panelDown = errors.New("panel unreachable")
	store.SetSquadLister(func(context.Context) ([]string, error) { return nil, panelDown })
	if _, err := store.CreateOrder(ctx, input); err != nil {
		t.Fatalf("an unreachable panel blocked the sale: %v", err)
	}
}
