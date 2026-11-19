//go:build integration

package integrationtest

import (
	"context"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/botapp"
	"github.com/omniflow/omniflow/internal/commercepg"
)

// A win-back wave issues one single-use discount per wave and lapsed
// entitlement; the next renewal applies it without the customer typing a
// code, and the offer is spent by that order.
func TestWinbackOfferAppliesItselfAtCheckout(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	commerceStore := harness.commerceStore(t, testOptions())
	bot, err := botapp.NewPostgresStoreWithPool(harness.pool)
	if err != nil {
		t.Fatal(err)
	}
	planVersionID := harness.catalog(ctx, t, "winback-plan", 22900)
	customer := harness.customer(ctx, t)
	wave := botapp.WinbackWave{Number: 2, AfterDays: 2, Percent: 10, ValidFor: 24 * time.Hour}
	code, expires, err := bot.IssueWinbackOffer(ctx, customer, "00000000-0000-0000-0000-000000000001", wave)
	if err != nil || code == "" || time.Until(expires) < 23*time.Hour {
		t.Fatalf("issue: %q %v %v", code, expires, err)
	}
	again, _, err := bot.IssueWinbackOffer(ctx, customer, "00000000-0000-0000-0000-000000000001", wave)
	if err != nil || again != code {
		t.Fatalf("re-issue gave %q (%v), want the same code %q", again, err, code)
	}

	order, err := commerceStore.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customer, PlanVersionID: planVersionID, Currency: "RUB", Operation: "purchase", IdempotencyKey: "winback-order",
	})
	if err != nil {
		t.Fatal(err)
	}
	if order.DiscountMinor != 2290 {
		t.Fatalf("discount = %d, want 10%% of 22900 applied without a code", order.DiscountMinor)
	}
	var status string
	if err := harness.pool.QueryRow(ctx, `SELECT status FROM personal_offers WHERE user_id = $1::uuid`, customer).Scan(&status); err != nil || status != "redeemed" {
		t.Fatalf("offer status = %q (%v), want redeemed", status, err)
	}
}
