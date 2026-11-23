//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/omniflow/omniflow/internal/accountcheckout"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/panelpg"
	"github.com/omniflow/omniflow/internal/paymentservice"
)

// The store card prices a plan's further periods before the checkout opens:
// the catalogue lists the periods its first operation may be bought for, in
// the settlement currency, and the checkout opens at the one the card chose.
// A renewal-only period is not offered for a purchase, on the card or at the
// checkout.
func TestTheCatalogueListsThePeriodsTheCardSells(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	panel := newOperations(t, harness)
	actor := harness.operator(ctx, t, "catalogue-periods@example.test")
	store := harness.commerceStore(t, testOptions())
	planID := harness.planID(ctx, t, harness.catalog(ctx, t, "catalogue-periods", 0))
	version, err := panel.CreatePlanVersion(ctx, planID, panelpg.PlanVersionInput{
		BillingPeriod: "month", DurationSeconds: 30 * 86_400, SquadSelection: "automatic",
		UpgradePolicy: "extend", DowngradePolicy: "at_expiry", CancellationPolicy: "at_expiry",
		TrialEligibility: "new_customer", Prices: map[string]int64{"RUB": 22_900, "USD": 300},
		PlanVersionOptions: panelpg.PlanVersionOptions{
			Periods: []panelpg.PlanPeriod{
				{Days: 7, Currency: "RUB", AmountMinor: 7_900, ForPurchase: true},
				{Days: 365, Currency: "RUB", AmountMinor: 199_000, ForPurchase: true, ForRenewal: true, Highlighted: true},
				{Days: 180, Currency: "RUB", AmountMinor: 110_000, ForRenewal: true},
				{Days: 90, Currency: "USD", AmountMinor: 800, ForPurchase: true},
			},
		},
	}, actor)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}

	provider := &hostedStubProvider{name: "cryptobot", currency: "RUB"}
	checkout, err := accountcheckout.New(harness.pool, store, paymentservice.New(harness.pool, store, provider),
		accountcheckout.Options{
			Logger:   slog.New(slog.DiscardHandler),
			Settings: accountcheckout.Settings{Currency: "RUB", PublicURL: "https://example.test"},
		})
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	customerID := harness.customer(ctx, t)

	plans, err := checkout.Plans(ctx, customerID, "en")
	if err != nil {
		t.Fatalf("catalogue: %v", err)
	}
	var offer *accountcheckout.PlanOffer
	for index := range plans {
		if plans[index].PlanVersionID == version.ID {
			offer = &plans[index]
		}
	}
	if offer == nil {
		t.Fatalf("the plan is not in the catalogue: %+v", plans)
	}
	if len(offer.Operations) == 0 || offer.Operations[0] != "purchase" {
		t.Fatalf("operations = %v, want a purchase first", offer.Operations)
	}
	want := []accountcheckout.PeriodChoice{
		{Days: 7, AmountMinor: 7_900},
		{Days: 365, AmountMinor: 199_000, Highlighted: true},
	}
	if len(offer.Periods) != len(want) {
		t.Fatalf("periods = %+v, want the RUB purchase periods %+v", offer.Periods, want)
	}
	for index := range want {
		if offer.Periods[index] != want[index] {
			t.Fatalf("periods = %+v, want %+v", offer.Periods, want)
		}
	}

	detail, err := checkout.Plan(ctx, customerID, version.ID, "en")
	if err != nil || len(detail.Periods) != len(want) {
		t.Fatalf("plan page periods = %+v, %v; want the catalogue's", detail.Periods, err)
	}

	// The card's choice opens the checkout at that period and its price.
	view, err := checkout.Open(ctx, customerID, "en", accountcheckout.OpenRequest{
		PlanVersionID: version.ID, Operation: "purchase", PeriodDays: 365,
	})
	if err != nil {
		t.Fatalf("open at 365 days: %v", err)
	}
	if view.PeriodDays != 365 || view.Quote.Subtotal.Amount != 199_000 {
		t.Fatalf("opened at %d days for %d, want 365 for 199000", view.PeriodDays, view.Quote.Subtotal.Amount)
	}

	// A renewal-only period is refused for a purchase.
	if _, err = checkout.Open(ctx, customerID, "en", accountcheckout.OpenRequest{
		PlanVersionID: version.ID, Operation: "purchase", PeriodDays: 180,
	}); !errors.Is(err, commercepg.ErrPeriodUnavailable) {
		t.Fatalf("open at a renewal-only period: %v, want period_unavailable", err)
	}
}
