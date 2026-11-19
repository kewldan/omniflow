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

// A plan's traffic reset strategy, external squad, and tag reach the
// Remnawave user, published and configured from the panel the way an
// operator does it.
func TestThePlanProfileReachesTheRemnawaveUser(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	panel := newOperations(t, harness)
	actor := harness.operator(ctx, t, "profile@example.test")
	store := harness.commerceStore(t, testOptions())
	seeded := harness.catalog(ctx, t, "profile-plan", 0)
	planID := harness.planID(ctx, t, seeded)
	external := "44444444-4444-4444-8444-444444444444"

	version, err := panel.CreatePlanVersion(ctx, planID, panelpg.PlanVersionInput{
		BillingPeriod: "month", DurationSeconds: 30 * 86_400, SquadSelection: "automatic",
		UpgradePolicy: "extend", DowngradePolicy: "at_expiry", CancellationPolicy: "at_expiry",
		TrialEligibility: "new_customer", Prices: map[string]int64{"RUB": 0},
		PlanVersionOptions: panelpg.PlanVersionOptions{TrafficResetStrategy: "MONTH", ExternalSquadID: external},
	}, actor)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if _, err = panel.SavePlanOptions(ctx, planID, panelpg.PlanOptions{RemnawaveTag: "vip_plan"}, actor); err != nil {
		t.Fatalf("tag the plan: %v", err)
	}
	if _, err = panel.SavePlanOptions(ctx, planID, panelpg.PlanOptions{RemnawaveTag: "not a tag!"}, actor); !errors.Is(err, panelpg.ErrValidaton) {
		t.Fatalf("a tag the panel would refuse was accepted: %v", err)
	}

	customerID := harness.customer(ctx, t)
	operationID := harness.purchaseOperation(ctx, t, store, customerID, version.ID, "profile-order")
	memory := newMemoryRemnawave()
	if err = runFulfillment(ctx, fulfillment.NewWorker(harness.pool, memory), operationID); err != nil {
		t.Fatalf("provision: %v", err)
	}
	pushed := memory.pushed[len(memory.pushed)-1]
	if pushed.TrafficLimitStrategy != "MONTH" || pushed.ExternalSquadID != external ||
		pushed.Tag != "VIP_PLAN" || !pushed.TagManaged {
		t.Fatalf("pushed profile = %+v", pushed)
	}
}

// purchaseOperation buys a version for the customer and returns its
// provisioning operation.
func (harness *harness) purchaseOperation(ctx context.Context, t *testing.T, store *commercepg.Store, customerID, planVersionID, key string) string {
	t.Helper()
	order, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: planVersionID, Currency: "RUB",
		Operation: "purchase", IdempotencyKey: key,
	})
	if err != nil {
		t.Fatalf("buy: %v", err)
	}
	var operationID string
	if err = harness.pool.QueryRow(ctx, `SELECT f.id::text FROM fulfillment_operations f
		JOIN entitlements e ON e.id = f.entitlement_id WHERE e.order_id = $1`, order.ID).Scan(&operationID); err != nil {
		t.Fatalf("read operation: %v", err)
	}
	return operationID
}
