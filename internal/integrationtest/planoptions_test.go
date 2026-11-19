//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"testing"

	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/panelpg"
)

// planID reads the plan a seeded version belongs to.
func (harness *harness) planID(ctx context.Context, t *testing.T, planVersionID string) string {
	t.Helper()
	var planID string
	if err := harness.pool.QueryRow(ctx, `SELECT plan_id::text FROM plan_versions WHERE id = $1::uuid`, planVersionID).Scan(&planID); err != nil {
		t.Fatalf("read plan: %v", err)
	}
	return planID
}

// A plan version offering a choice of squads could not be published from the
// panel: the editor's values were refused by the database and the database's
// by the editor. Published through the panel service now, its offered set is
// stored, read back for the next version, and a customer can buy it with a
// selection.
func TestPanelPublishesAVersionWithACustomerSquadChoice(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	panel := newOperations(t, harness)
	actor := harness.operator(ctx, t, "catalog@example.test")
	seeded := harness.catalog(ctx, t, "choice-plan", 29900)
	planID := harness.planID(ctx, t, seeded)
	germany, finland := "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"
	included := "33333333-3333-4333-8333-333333333333"
	maximum := int32(1)

	input := panelpg.PlanVersionInput{
		BillingPeriod: "month", DurationSeconds: 30 * 86_400,
		SquadIDs: []string{included}, SquadSelection: "required",
		MinSelectableSquads: 1, MaxSelectableSquads: &maximum,
		UpgradePolicy: "extend", DowngradePolicy: "at_expiry", CancellationPolicy: "at_expiry",
		TrialEligibility: "never_trialled", Prices: map[string]int64{"RUB": 29900},
		PlanVersionOptions: panelpg.PlanVersionOptions{OfferedSquads: []panelpg.OfferedSquad{
			{SquadID: germany, LabelRU: "Германия", LabelEN: "Germany"},
			{SquadID: finland, LabelRU: "Финляндия", LabelEN: "Finland"},
		}},
	}
	version, err := panel.CreatePlanVersion(ctx, planID, input, actor)
	if err != nil {
		t.Fatalf("publish a version with a squad choice: %v", err)
	}
	detail, err := panel.PlanDetail(ctx, planID)
	if err != nil {
		t.Fatalf("read plan: %v", err)
	}
	if len(detail.Versions[0].OfferedSquads) != 2 || detail.Versions[0].OfferedSquads[0].LabelEN != "Germany" {
		t.Fatalf("offered squads were not read back: %+v", detail.Versions[0].OfferedSquads)
	}

	stale := input
	stale.SquadSelection = "customer_choice"
	if _, err = panel.CreatePlanVersion(ctx, planID, stale, actor); !errors.Is(err, panelpg.ErrValidaton) {
		t.Fatalf("the old editor value was accepted: %v", err)
	}

	store := harness.commerceStore(t, testOptions())
	customerID := harness.customer(ctx, t)
	quote, err := store.PreviewOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: version.ID, Currency: "RUB", Operation: "purchase",
		IdempotencyKey: "squad-choice-0001", SelectedSquadIDs: []string{finland},
	})
	if err != nil {
		t.Fatalf("quote with a selection: %v", err)
	}
	if len(quote.SquadIDs) != 2 {
		t.Fatalf("resolved squads = %v, want the included one plus the choice", quote.SquadIDs)
	}
}
