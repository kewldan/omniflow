//go:build integration

package integrationtest

import (
	"context"
	"testing"

	"github.com/omniflow/omniflow/internal/commercepg"
)

// Moving a plan's live subscriptions onto its newest version's squads swaps
// only the plan's own squads, keeps what the customer added, queues one
// set_squads per subscription, and queues nothing more when pressed again.
func TestAPlansNewSquadsReachItsLiveSubscriptions(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	core := harness.commerce(t, testOptions())
	customerID := harness.customer(ctx, t)
	planVersionID := harness.catalog(ctx, t, "squad-sync-plan", 0)
	oldSquad, newSquad := "77777777-7777-4777-8777-777777777777", "88888888-8888-4888-8888-888888888888"
	added := "99999999-9999-4999-8999-999999999999"
	if _, err := harness.pool.Exec(ctx, `UPDATE plan_versions SET remnawave_squad_ids = ARRAY[$2::uuid] WHERE id = $1::uuid`, planVersionID, oldSquad); err != nil {
		t.Fatalf("give the plan a squad: %v", err)
	}
	order, err := core.Store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: planVersionID, Currency: "RUB",
		Operation: "purchase", IdempotencyKey: "squad-sync-order",
	})
	if err != nil {
		t.Fatalf("buy: %v", err)
	}
	// A squad the customer has beyond the plan's own, as an add-on leaves it.
	if _, err = harness.pool.Exec(ctx, `UPDATE entitlements SET remnawave_squad_ids = remnawave_squad_ids || $2::uuid WHERE order_id = $1`, order.ID, added); err != nil {
		t.Fatalf("add a squad: %v", err)
	}
	planID := harness.planID(ctx, t, planVersionID)
	if _, err = harness.pool.Exec(ctx, `INSERT INTO plan_versions (plan_id, version, billing_period, duration_seconds, remnawave_squad_ids)
		VALUES ($1::uuid, 2, 'month', 2592000, ARRAY[$2::uuid])`, planID, newSquad); err != nil {
		t.Fatalf("publish version 2: %v", err)
	}

	for range 2 {
		moved, applyErr := core.Fulfillment.ApplyPlanSquads(ctx, planID, "test")
		if applyErr != nil || moved != 1 {
			t.Fatalf("apply: moved %d, %v", moved, applyErr)
		}
	}
	var squads []string
	if err = harness.pool.QueryRow(ctx, `SELECT remnawave_squad_ids::text[] FROM entitlements WHERE order_id = $1`, order.ID).Scan(&squads); err != nil {
		t.Fatalf("read squads: %v", err)
	}
	if len(squads) != 2 || squads[0] != newSquad || squads[1] != added {
		t.Fatalf("squads = %v, want the new plan squad and the customer's own", squads)
	}
	var operations int
	if err = harness.pool.QueryRow(ctx, `SELECT count(*) FROM fulfillment_operations WHERE operation = 'set_squads'`).Scan(&operations); err != nil {
		t.Fatalf("count operations: %v", err)
	}
	if operations != 1 {
		t.Fatalf("set_squads operations = %d, want one however often it is pressed", operations)
	}
}
