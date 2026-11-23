//go:build integration

package integrationtest

import (
	"context"
	"log/slog"
	"testing"

	"github.com/omniflow/omniflow/internal/accountcheckout"
	"github.com/omniflow/omniflow/internal/panelpg"
	"github.com/omniflow/omniflow/internal/paymentservice"
)

// A payment button prices the period the checkout has chosen. It priced the
// version's own month instead, so a customer who picked 90 days for 499 saw
// every method offer to charge 229 beside a total of 499.
func TestCheckoutMethodsPriceTheChosenPeriod(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	panel := newOperations(t, harness)
	actor := harness.operator(ctx, t, "period-price@example.test")
	store := harness.commerceStore(t, testOptions())
	planID := harness.planID(ctx, t, harness.catalog(ctx, t, "period-price-plan", 0))
	version, err := panel.CreatePlanVersion(ctx, planID, panelpg.PlanVersionInput{
		BillingPeriod: "month", DurationSeconds: 30 * 86_400, SquadSelection: "automatic",
		UpgradePolicy: "extend", DowngradePolicy: "at_expiry", CancellationPolicy: "at_expiry",
		TrialEligibility: "new_customer", Prices: map[string]int64{"RUB": 22_900},
		PlanVersionOptions: panelpg.PlanVersionOptions{
			Periods: []panelpg.PlanPeriod{
				{Days: 90, Currency: "RUB", AmountMinor: 49_900, ForPurchase: true, ForRenewal: true},
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
	view, err := checkout.Open(ctx, customerID, "en", accountcheckout.OpenRequest{
		PlanVersionID: version.ID, Operation: "purchase",
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if len(view.Providers) != 1 || view.Providers[0].AmountMinor != 22_900 {
		t.Fatalf("the version's own month offers %+v, want 22900", view.Providers)
	}

	days := 90
	if view, err = checkout.Update(ctx, customerID, "en", accountcheckout.UpdateRequest{PeriodDays: &days}); err != nil {
		t.Fatalf("choose 90 days: %v", err)
	}
	if len(view.Providers) != 1 || view.Providers[0].AmountMinor != 49_900 {
		t.Fatalf("90 days offers %+v, want 49900", view.Providers)
	}

	// Choosing the method keeps the period's price: the order is checked against
	// the choices for the period, not the version's month.
	key := "cryptobot"
	if view, err = checkout.Update(ctx, customerID, "en", accountcheckout.UpdateRequest{Provider: &key}); err != nil {
		t.Fatalf("choose the method: %v", err)
	}
	if view.Provider != key || view.Providers[0].AmountMinor != 49_900 {
		t.Fatalf("after choosing the method: provider %q, %+v", view.Provider, view.Providers)
	}
}
