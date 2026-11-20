//go:build integration

package integrationtest

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/botapp"
	"github.com/omniflow/omniflow/internal/commercepg"
)

// A trial that ends without a purchase earns one personal discount on a first
// subscription, separate from the expiry waves of the same entitlement; it
// applies itself to the first purchase, after which the customer counts as
// converted and gets no further trial win-back.
func TestTrialWinbackOffersADiscountOnTheFirstSubscription(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	commerceStore := harness.commerceStore(t, testOptions())
	bot, err := botapp.NewPostgresStoreWithPool(harness.pool)
	if err != nil {
		t.Fatal(err)
	}
	planVersionID := harness.catalog(ctx, t, "trial-winback-plan", 30000)
	customer := harness.customer(ctx, t)
	entitlement := "00000000-0000-0000-0000-0000000000a1"

	paid, err := bot.HasPaidSubscription(ctx, customer)
	if err != nil || paid {
		t.Fatalf("a trial-only customer paid = %v (%v)", paid, err)
	}
	trial := botapp.WinbackWave{Number: 1, Trial: true, AfterHours: 2, Percent: 20, ValidFor: 48 * time.Hour}
	code, expires, err := bot.IssueWinbackOffer(ctx, customer, entitlement, trial)
	if err != nil || !strings.HasPrefix(code, "TRIAL20-") || time.Until(expires) < 47*time.Hour {
		t.Fatalf("issue: %q %v %v", code, expires, err)
	}
	expiry, _, err := bot.IssueWinbackOffer(ctx, customer, entitlement,
		botapp.WinbackWave{Number: 2, AfterDays: 2, Percent: 10, ValidFor: 24 * time.Hour})
	if err != nil || expiry == code {
		t.Fatalf("an expiry wave of the same entitlement reused the trial offer: %q (%v)", expiry, err)
	}
	var title string
	if err = harness.pool.QueryRow(ctx, `SELECT o.title_en FROM personal_offers o
		JOIN promo_codes c ON c.promotion_id = o.promotion_id WHERE c.normalized_code = $1`, code).Scan(&title); err != nil ||
		title != "20% off your first subscription" {
		t.Fatalf("offer title = %q (%v)", title, err)
	}
	if _, err = harness.pool.Exec(ctx, `UPDATE personal_offers SET status = 'expired'
		WHERE user_id = $1::uuid AND promotion_id <> (SELECT promotion_id FROM promo_codes WHERE normalized_code = $2)`,
		customer, code); err != nil {
		t.Fatalf("retire the expiry offer: %v", err)
	}

	order, err := commerceStore.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customer, PlanVersionID: planVersionID, Currency: "RUB", Operation: "purchase", IdempotencyKey: "trial-winback-order",
	})
	if err != nil {
		t.Fatal(err)
	}
	if order.DiscountMinor != 6000 {
		t.Fatalf("discount = %d, want 20%% of 30000 applied without a code", order.DiscountMinor)
	}
	harness.settle(ctx, t, commerceStore, uuidText(order.ID), order.ExternalMinor, "trial-winback-payment")
	if paid, err = bot.HasPaidSubscription(ctx, customer); err != nil || !paid {
		t.Fatalf("after the first purchase paid = %v (%v)", paid, err)
	}
}
