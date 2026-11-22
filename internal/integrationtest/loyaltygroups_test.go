//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/panelpg"
)

func (harness *harness) loyaltyCode(ctx context.Context, t *testing.T, customerID string) string {
	t.Helper()
	var code string
	if err := harness.pool.QueryRow(ctx, `SELECT t.code FROM loyalty_standings s JOIN loyalty_tiers t ON t.id = s.tier_id
		WHERE s.user_id = $1::uuid`, customerID).Scan(&code); err != nil {
		return ""
	}
	return code
}

// Loyalty tiers work as Bedolaga's promo groups: the worker places a customer
// by what they spent, a tier discounts by billing period, an operator can
// grant a higher-priority group by hand that restricts plans and payment
// methods, removing it restores the earned tier, and a recalculation request
// re-evaluates everybody once.
func TestLoyaltyGroupsPlaceDiscountAndRestrict(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	panel := newOperations(t, harness)
	actor := harness.operator(ctx, t, "loyalty@omniflow.test")

	monthly := harness.catalog(ctx, t, "group-month", 10000)
	yearly := harness.catalog(ctx, t, "group-year", 100000)
	if _, err := harness.pool.Exec(ctx, `UPDATE plan_versions SET billing_period = 'year' WHERE id = $1::uuid`, yearly); err != nil {
		t.Fatal(err)
	}
	monthlyPlan := harness.planIDOf(ctx, t, monthly)

	if _, err := panel.PublishLoyaltyProgram(ctx, panelpg.LoyaltyProgram{
		Metric: "spend", Currency: "RUB", WindowDays: 365, GraceDays: 0,
		Tiers: []panelpg.LoyaltyTier{
			{Code: "bronze", NameEN: "Bronze", NameRU: "Бронза"},
			{Code: "silver", NameEN: "Silver", NameRU: "Серебро", Threshold: 10000, DiscountBPS: 500,
				PeriodDiscounts: map[string]int{"year": 2000}},
			{Code: "vip", NameEN: "VIP", NameRU: "VIP", ManualOnly: true, Priority: 10, DiscountBPS: 3000,
				AllowedPlanIDs: []string{monthlyPlan}, AllowedProviders: []string{"manual"}},
		},
	}, true, actor); err != nil {
		t.Fatalf("publish: %v", err)
	}
	// A period discount for a period no plan has is refused.
	if _, err := panel.PublishLoyaltyProgram(ctx, panelpg.LoyaltyProgram{
		Metric: "spend", Currency: "RUB", WindowDays: 365,
		Tiers: []panelpg.LoyaltyTier{{Code: "bronze", NameEN: "B", NameRU: "Б", PeriodDiscounts: map[string]int{"fortnight": 100}}},
	}, false, actor); !errors.Is(err, panelpg.ErrValidaton) {
		t.Fatalf("an unknown period was accepted: %v", err)
	}

	customer := harness.customer(ctx, t)
	order, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customer, PlanVersionID: monthly, Currency: "RUB", Operation: "purchase", IdempotencyKey: "group-1",
	})
	if err != nil {
		t.Fatalf("first order: %v", err)
	}
	harness.settle(ctx, t, store, uuidText(order.ID), 10000, "group-event-1")

	// The publish asked for a recalculation, so the first pass places everybody.
	if err := panel.LoyaltySweep(ctx, time.Now().UTC()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if code := harness.loyaltyCode(ctx, t, customer); code != "silver" {
		t.Fatalf("after spending 100.00 the customer is %q, want silver", code)
	}
	var recalculated bool
	if err := harness.pool.QueryRow(ctx, `SELECT recalculated_at IS NOT NULL FROM loyalty_programs WHERE enabled`).Scan(&recalculated); err != nil || !recalculated {
		t.Fatalf("recalculation not recorded: %v", err)
	}

	quote, err := store.PreviewOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customer, PlanVersionID: yearly, Currency: "RUB", Operation: "purchase", IdempotencyKey: "group-2",
	})
	if err != nil || quote.LoyaltyDiscountMinor != 20000 {
		t.Fatalf("yearly quote: %+v (%v), want the 20%% year discount", quote, err)
	}

	if _, err := panel.AssignLoyaltyGroup(ctx, customer, "vip", "partner", nil, actor); err != nil {
		t.Fatalf("assign: %v", err)
	}
	if _, err := panel.AssignLoyaltyGroup(ctx, customer, "nonexistent", "", nil, actor); !errors.Is(err, panelpg.ErrValidaton) {
		t.Fatalf("a group outside the programme was granted: %v", err)
	}
	quote, err = store.PreviewOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customer, PlanVersionID: monthly, Currency: "RUB", Operation: "purchase", IdempotencyKey: "group-3",
	})
	if err != nil || quote.LoyaltyDiscountMinor != 3000 || quote.LoyaltyTier != "vip" {
		t.Fatalf("vip quote: %+v (%v), want 30%% from vip", quote, err)
	}
	if _, err := store.PreviewOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customer, PlanVersionID: yearly, Currency: "RUB", Operation: "purchase", IdempotencyKey: "group-4",
	}); !errors.Is(err, commercepg.ErrPlanUnavailable) {
		t.Fatalf("a plan outside the vip group was quoted: %v", err)
	}
	if allowed, err := store.ProviderAllowed(ctx, customer, "yookassa"); err != nil || allowed {
		t.Fatalf("yookassa allowed=%v (%v), want refused for vip", allowed, err)
	}
	if allowed, err := store.ProviderAllowed(ctx, customer, "manual"); err != nil || !allowed {
		t.Fatalf("manual allowed=%v (%v), want allowed", allowed, err)
	}
	// The first sweep placed the customer at the programme's launch, which
	// stays quiet; the operator's grant is announced.
	var operatorNotices int
	if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM operator_notifications WHERE kind = 'marketing'`).Scan(&operatorNotices); err != nil || operatorNotices != 1 {
		t.Fatalf("operator notices = %d (%v), want the grant alone", operatorNotices, err)
	}

	if err := panel.RemoveLoyaltyGroup(ctx, customer, "vip", actor); err != nil {
		t.Fatalf("remove: %v", err)
	}
	quote, err = store.PreviewOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customer, PlanVersionID: yearly, Currency: "RUB", Operation: "purchase", IdempotencyKey: "group-5",
	})
	if err != nil || quote.LoyaltyTier != "silver" {
		t.Fatalf("after removal: %+v (%v), want silver again", quote, err)
	}

	// A pass with nothing new evaluates nobody and writes no history.
	var before, after int
	_ = harness.pool.QueryRow(ctx, `SELECT count(*) FROM loyalty_standing_history`).Scan(&before)
	if err := panel.LoyaltySweep(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	_ = harness.pool.QueryRow(ctx, `SELECT count(*) FROM loyalty_standing_history`).Scan(&after)
	if before != after {
		t.Fatalf("an idle pass wrote %d history entries", after-before)
	}
	if err := panel.RequestLoyaltyRecalculation(ctx, actor); err != nil {
		t.Fatalf("request recalculation: %v", err)
	}
	if err := panel.LoyaltySweep(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	_ = harness.pool.QueryRow(ctx, `SELECT count(*) FROM loyalty_standing_history`).Scan(&after)
	if before != after {
		t.Fatalf("recalculating an unchanged standing wrote %d history entries", after-before)
	}
}
