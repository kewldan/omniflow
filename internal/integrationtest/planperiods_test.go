//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/accountcheckout"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/panelpg"
)

// One plan version sold for several periods and a custom number of days,
// published from the panel: an order for a listed period costs and lasts
// exactly that, a renewal-only period is refused on a purchase and accepted on
// an extension, custom days are priced by the day within their bounds, and
// the web checkout carries the choice to the order.
func TestAPlanSoldForSeveralPeriodsAndCustomDays(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	panel := newOperations(t, harness)
	actor := harness.operator(ctx, t, "periods@example.test")
	store := harness.commerceStore(t, testOptions())
	planID := harness.planID(ctx, t, harness.catalog(ctx, t, "period-plan", 0))
	minimum, maximum := int32(7), int32(60)
	version, err := panel.CreatePlanVersion(ctx, planID, panelpg.PlanVersionInput{
		BillingPeriod: "month", DurationSeconds: 30 * 86_400, SquadSelection: "automatic",
		UpgradePolicy: "extend", DowngradePolicy: "at_expiry", CancellationPolicy: "at_expiry",
		TrialEligibility: "new_customer", Prices: map[string]int64{"RUB": 0},
		PlanVersionOptions: panelpg.PlanVersionOptions{
			Periods: []panelpg.PlanPeriod{
				{Days: 90, Currency: "RUB", AmountMinor: 0, ForPurchase: true, ForRenewal: true},
				{Days: 180, Currency: "RUB", AmountMinor: 0, ForRenewal: true},
			},
			CustomDaysMin: &minimum, CustomDaysMax: &maximum, DayPrices: map[string]int64{"RUB": 100},
		},
	}, actor)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	customerID := harness.customer(ctx, t)
	ends := func(orderID any) time.Duration {
		var startsAt, endsAt time.Time
		if scanErr := harness.pool.QueryRow(ctx, `SELECT starts_at, ends_at FROM entitlements WHERE order_id = $1`, orderID).Scan(&startsAt, &endsAt); scanErr != nil {
			t.Fatalf("read entitlement: %v", scanErr)
		}
		return endsAt.Sub(startsAt)
	}

	if _, err = store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: version.ID, Currency: "RUB", Operation: "purchase",
		IdempotencyKey: "period-180-purchase", PeriodDays: 180,
	}); !errors.Is(err, commercepg.ErrPeriodUnavailable) {
		t.Fatalf("a renewal-only period on a purchase: %v", err)
	}
	first, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: version.ID, Currency: "RUB", Operation: "purchase",
		IdempotencyKey: "period-90-purchase", PeriodDays: 90,
	})
	if err != nil {
		t.Fatalf("buy 90 days: %v", err)
	}
	if got := ends(first.ID); got != 90*24*time.Hour {
		t.Fatalf("a 90-day order lasts %v", got)
	}

	harness.seedWallet(ctx, t, customerID, "period-wallet", 1000)
	custom, err := store.PreviewOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: version.ID, Currency: "RUB", Operation: "extension",
		IdempotencyKey: "period-custom", PeriodDays: 10, SubscriptionID: uuidText(first.SubscriptionID),
	})
	if err != nil || custom.TotalMinor() != 1000 {
		t.Fatalf("ten custom days = %d, %v; want 1000", custom.TotalMinor(), err)
	}
	if _, err = store.PreviewOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: version.ID, Currency: "RUB", Operation: "extension",
		IdempotencyKey: "period-too-long", PeriodDays: 61, SubscriptionID: uuidText(first.SubscriptionID),
	}); !errors.Is(err, commercepg.ErrPeriodUnavailable) {
		t.Fatalf("custom days past the maximum: %v", err)
	}

	checkout := newAccountCheckout(t, harness, store)
	if _, err = checkout.Open(ctx, customerID, "en", accountcheckout.OpenRequest{
		PlanVersionID: version.ID, Operation: "extension", SubscriptionID: uuidText(first.SubscriptionID),
	}); err != nil {
		t.Fatalf("open checkout: %v", err)
	}
	days := 180
	view, err := checkout.Update(ctx, customerID, "en", accountcheckout.UpdateRequest{PeriodDays: &days})
	if err != nil {
		t.Fatalf("choose 180 days: %v", err)
	}
	if view.PeriodDays != 180 || len(view.Periods.Choices) != 2 || view.Periods.MinDays != 7 {
		t.Fatalf("checkout periods = %+v (chosen %d)", view.Periods, view.PeriodDays)
	}
	order, err := checkout.ConfirmCheckout(ctx, customerID, "en")
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if got := ends(order.ID); got < 180*24*time.Hour {
		t.Fatalf("a 180-day extension added %v", got)
	}
}
