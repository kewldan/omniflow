//go:build integration

package integrationtest

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/omniflow/omniflow/internal/botapp"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// A version sold for five periods lets the chat customer pick any of them,
// marks the one the operator highlighted, and the order buys the period
// chosen at its price.
func TestTheBotSellsTheVersionsPeriods(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	flow := newBotFlow(ctx, t, harness)
	versionID := harness.catalog(ctx, t, "five_periods", 19900)
	if _, err := harness.pool.Exec(ctx, `INSERT INTO plan_version_periods
		(plan_version_id, days, currency, amount_minor, for_purchase, for_renewal, highlighted) VALUES
		($1::uuid, 7, 'RUB', 7900, true, true, false),
		($1::uuid, 90, 'RUB', 49900, true, true, true),
		($1::uuid, 180, 'RUB', 89900, true, true, false),
		($1::uuid, 360, 'RUB', 159900, true, false, false)`, versionID); err != nil {
		t.Fatal(err)
	}
	const buyer = int64(770001)
	customerID := flow.customerOf(ctx, t, buyer)

	flow.tap(ctx, buyer, 10, "act:buy:"+versionID+":purchase")
	picker := flow.telegram.lastScreen(t)
	buttons := picker.callbacks(t)
	for _, want := range []string{"act:period:0", "act:period:7", "act:period:90", "act:period:180", "act:period:360"} {
		if !slices.Contains(buttons, want) {
			t.Fatalf("the picker lacks %s: %v\n%s", want, buttons, picker.Fields["text"])
		}
	}
	if !strings.Contains(picker.Fields["reply_markup"], "best price") {
		t.Fatalf("the highlighted period is not marked: %s", picker.Fields["reply_markup"])
	}

	flow.tap(ctx, buyer, 10, "act:period:90")
	checkout, found, err := flow.store.Checkout(ctx, customerID)
	if err != nil || !found || checkout.PeriodDays != 90 {
		t.Fatalf("checkout period = %+v %v %v", checkout.PeriodDays, found, err)
	}
	methods := flow.telegram.lastScreen(t)
	if !strings.Contains(methods.Fields["reply_markup"], "499") {
		t.Fatalf("the payment methods do not show the 90-day price: %s", methods.Fields["reply_markup"])
	}
	quote, err := flow.commerce.Quote(ctx, checkout)
	if err != nil || quote.Subtotal.Amount != 49900 {
		t.Fatalf("quote = %+v %v", quote.Subtotal, err)
	}
	if _, err = flow.store.SetCheckoutProvider(ctx, checkout.ID, "manual", "RUB"); err != nil {
		t.Fatal(err)
	}
	checkout, _, _ = flow.store.Checkout(ctx, customerID)
	orderID, err := flow.commerce.Confirm(ctx, checkout, botapp.Plan{}, botapp.Customer{})
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	order, err := dbgen.New(harness.pool).GetOrder(ctx, mustUUID(t, orderID))
	if err != nil || order.SubtotalMinor != 49900 || order.PeriodSeconds.Int64 != 90*86_400 {
		t.Fatalf("order = %d for %v, %v; want 49900 for 90 days", order.SubtotalMinor, order.PeriodSeconds, err)
	}

	// A period the version does not sell for a purchase is refused, and the
	// picker comes back.
	flow.tap(ctx, buyer, 11, "act:buy:"+versionID+":purchase")
	flow.tap(ctx, buyer, 11, "act:period:30")
	if again := flow.telegram.lastScreen(t); !slices.Contains(again.callbacks(t), "act:period:90") {
		t.Fatalf("an unsold period: %s", again.Fields["text"])
	}

	// A renewal offers the renewal periods: 360 days is purchase-only.
	flow.tap(ctx, buyer, 12, "act:buy:"+versionID+":extension")
	if renewal := flow.telegram.lastScreen(t).callbacks(t); slices.Contains(renewal, "act:period:360") ||
		!slices.Contains(renewal, "act:period:180") {
		t.Fatalf("renewal periods: %v", renewal)
	}
}
