//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"testing"

	"github.com/omniflow/omniflow/internal/commercepg"
)

// With a daily limit of one distinct code, a customer who used one code today
// is refused a second, at checkout and on its own; the same code again is not
// a second code, and an operator's personal offer is not counted.
func TestDailyLimitOfDistinctPromoCodes(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	if _, err := harness.pool.Exec(ctx, `UPDATE installation_settings SET document = '{"promoDailyCodeLimit": 1}' WHERE section = 'marketing'`); err != nil {
		t.Fatal(err)
	}
	versionID := harness.catalog(ctx, t, "daily-plan", 10000)
	harness.promotion(ctx, t, "DAILYTEN", harness.planIDOf(ctx, t, versionID), 5)
	harness.rewardPromo(ctx, t, "DAILYCASH", "wallet_credit", 1000, "RUB", 1)

	customer := harness.customer(ctx, t)
	if _, err := store.RedeemRewardPromo(ctx, "DAILYCASH", customer); err != nil {
		t.Fatalf("first code: %v", err)
	}
	_, err := store.PreviewOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customer, PlanVersionID: versionID, Currency: "RUB", Operation: "purchase",
		PromoCode: "DAILYTEN", IdempotencyKey: "daily-1",
	})
	if !errors.Is(err, commercepg.ErrPromoDailyLimit) {
		t.Fatalf("second distinct code today: %v, want the daily limit", err)
	}

	other := harness.customer(ctx, t)
	input := commercepg.CreateOrderInput{
		CustomerID: other, PlanVersionID: versionID, Currency: "RUB", Operation: "purchase",
		PromoCode: "DAILYTEN", IdempotencyKey: "daily-2",
	}
	if _, err := store.CreateOrder(ctx, input); err != nil {
		t.Fatalf("first code for another customer: %v", err)
	}
	input.IdempotencyKey = "daily-3"
	if _, err := store.PreviewOrder(ctx, input); err != nil {
		t.Fatalf("the same code again is not a second code: %v", err)
	}
	if _, err := store.RedeemRewardPromo(ctx, "DAILYCASH", other); !errors.Is(err, commercepg.ErrPromoNotApplicable) {
		t.Fatalf("reward code past the limit: %v", err)
	}
}
