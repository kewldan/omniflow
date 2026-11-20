//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"testing"

	"github.com/omniflow/omniflow/internal/accountcheckout"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/panelpg"
)

// A plan version sold with a traffic volume of the customer's choosing,
// priced per gigabyte: the price joins the period's and scales with it, a
// volume outside the range is refused, the entitlement carries the volume
// bought, and an automatic renewal repeats it.
func TestCustomTrafficIsPricedPerGigabyte(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	panel := newOperations(t, harness)
	actor := harness.operator(ctx, t, "traffic@example.test")
	store := harness.commerceStore(t, testOptions())
	planID := harness.planID(ctx, t, harness.catalog(ctx, t, "traffic-plan", 0))
	own := int64(10) * commerce.GiB
	minimum, maximum := int32(20), int32(200)
	version, err := panel.CreatePlanVersion(ctx, planID, panelpg.PlanVersionInput{
		BillingPeriod: "month", DurationSeconds: 30 * 86_400, SquadSelection: "automatic",
		UpgradePolicy: "extend", DowngradePolicy: "at_expiry", CancellationPolicy: "at_expiry",
		TrialEligibility: "new_customer", Prices: map[string]int64{"RUB": 1000},
		TrafficAllowanceBytes: &own,
		PlanVersionOptions: panelpg.PlanVersionOptions{
			Periods:            []panelpg.PlanPeriod{{Days: 60, Currency: "RUB", AmountMinor: 2000, ForPurchase: true, ForRenewal: true}},
			CustomTrafficMinGB: &minimum, CustomTrafficMaxGB: &maximum,
			GBPrices: map[string]int64{"RUB": 50},
		},
	}, actor)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if detail, detailErr := panel.PlanDetail(ctx, planID); detailErr != nil || detail.Versions[0].GBPrices["RUB"] != 50 ||
		*detail.Versions[0].CustomTrafficMinGB != 20 {
		t.Fatalf("the editor does not read the range back: %+v, %v", detail.Versions, detailErr)
	}
	customerID := harness.customer(ctx, t)
	preview := func(key string, periodDays, gigabytes int) (commercepg.OrderQuote, error) {
		return store.PreviewOrder(ctx, commercepg.CreateOrderInput{
			CustomerID: customerID, PlanVersionID: version.ID, Currency: "RUB", Operation: "purchase",
			IdempotencyKey: key, PeriodDays: periodDays, TrafficGB: gigabytes,
		})
	}
	if quote, quoteErr := preview("traffic-own", 0, 100); quoteErr != nil || quote.TotalMinor() != 6000 {
		t.Fatalf("100 GB for the own period = %d, %v; want 6000", quote.TotalMinor(), quoteErr)
	}
	if quote, quoteErr := preview("traffic-60", 60, 100); quoteErr != nil || quote.TotalMinor() != 12000 {
		t.Fatalf("100 GB for 60 days = %d, %v; want 12000", quote.TotalMinor(), quoteErr)
	}
	if _, err = preview("traffic-too-much", 0, 201); !errors.Is(err, commercepg.ErrTrafficUnavailable) {
		t.Fatalf("a volume past the maximum: %v", err)
	}

	harness.seedWallet(ctx, t, customerID, "traffic-wallet", 20000)
	order, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: version.ID, Currency: "RUB", Operation: "purchase",
		IdempotencyKey: "traffic-purchase", TrafficGB: 100,
	})
	if err != nil {
		t.Fatalf("buy 100 GB: %v", err)
	}
	var allowance int64
	if err = harness.pool.QueryRow(ctx, `SELECT traffic_allowance_bytes FROM entitlements WHERE order_id = $1`, order.ID).Scan(&allowance); err != nil {
		t.Fatalf("read entitlement: %v", err)
	}
	if allowance != 100*commerce.GiB || !order.TrafficGb.Valid || order.TrafficGb.Int32 != 100 {
		t.Fatalf("entitlement allowance %d, order traffic %+v", allowance, order.TrafficGb)
	}
	renewalDays, renewalGB, err := store.RenewalSelection(ctx, uuidText(order.SubscriptionID), version.ID, "RUB")
	if err != nil || renewalDays != 0 || renewalGB != 100 {
		t.Fatalf("renewal selection = %d days, %d GB, %v", renewalDays, renewalGB, err)
	}

	checkout := newAccountCheckout(t, harness, store)
	if _, err = checkout.Open(ctx, customerID, "en", accountcheckout.OpenRequest{
		PlanVersionID: version.ID, Operation: "extension", SubscriptionID: uuidText(order.SubscriptionID),
	}); err != nil {
		t.Fatalf("open checkout: %v", err)
	}
	tooMuch := 500
	if _, err = checkout.Update(ctx, customerID, "en", accountcheckout.UpdateRequest{TrafficGB: &tooMuch}); !errors.Is(err, commercepg.ErrTrafficUnavailable) {
		t.Fatalf("checkout accepted 500 GB: %v", err)
	}
	fifty := 50
	view, err := checkout.Update(ctx, customerID, "en", accountcheckout.UpdateRequest{TrafficGB: &fifty})
	if err != nil {
		t.Fatalf("choose 50 GB: %v", err)
	}
	if view.TrafficGB != 50 || view.Traffic.MinGB != 20 || view.Traffic.PricePerGBMinor != 50 || view.Quote.Subtotal.Amount != 3500 {
		t.Fatalf("checkout traffic = %+v (chosen %d, subtotal %+v)", view.Traffic, view.TrafficGB, view.Quote.Subtotal)
	}
}

// The traffic mode decides what a version sells: a fixed version sells no
// traffic add-on, at checkout or later, and no volume of the customer's
// choosing; a selectable one sells its packages at their own price.
func TestTrafficModeDecidesWhatIsSold(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	panel := newOperations(t, harness)
	actor := harness.operator(ctx, t, "traffic-mode@example.test")
	store := harness.commerceStore(t, testOptions())
	customerID := harness.customer(ctx, t)
	fixed := harness.catalog(ctx, t, "fixed-traffic", 0)
	pack := harness.addon(ctx, t, fixed, "fixed-pack", 0, 10*commerce.GiB, "full_price")
	if _, err := harness.pool.Exec(ctx, `UPDATE plan_versions SET traffic_mode = 'fixed' WHERE id = $1::uuid`, fixed); err != nil {
		t.Fatalf("fix the traffic: %v", err)
	}
	if _, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: fixed, Currency: "RUB", Operation: "purchase",
		IdempotencyKey: "fixed-with-pack", Addons: []commercepg.AddonSelection{{AddonVersionID: pack, Quantity: 1}},
	}); !errors.Is(err, commercepg.ErrAddonUnavailable) {
		t.Fatalf("a traffic add-on on a fixed version at checkout: %v", err)
	}
	if _, err := store.PreviewOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: fixed, Currency: "RUB", Operation: "purchase",
		IdempotencyKey: "fixed-volume", TrafficGB: 50,
	}); !errors.Is(err, commercepg.ErrTrafficUnavailable) {
		t.Fatalf("a chosen volume on a fixed version: %v", err)
	}
	if _, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: fixed, Currency: "RUB", Operation: "purchase",
		IdempotencyKey: "fixed-plain",
	}); err != nil {
		t.Fatalf("buy the fixed version: %v", err)
	}
	if _, err := store.CreateAddonOrder(ctx, commercepg.AddonOrderInput{
		CustomerID: customerID, Currency: "RUB", IdempotencyKey: "fixed-topup",
		Addons: []commercepg.AddonSelection{{AddonVersionID: pack, Quantity: 1}},
	}); !errors.Is(err, commercepg.ErrAddonUnavailable) {
		t.Fatalf("a traffic top-up on a fixed version: %v", err)
	}

	planID := harness.planID(ctx, t, harness.catalog(ctx, t, "selectable-traffic", 0))
	own := int64(10) * commerce.GiB
	input := panelpg.PlanVersionInput{
		BillingPeriod: "month", DurationSeconds: 30 * 86_400, SquadSelection: "automatic",
		UpgradePolicy: "extend", DowngradePolicy: "at_expiry", CancellationPolicy: "at_expiry",
		TrialEligibility: "new_customer", Prices: map[string]int64{"RUB": 1000}, TrafficAllowanceBytes: &own,
		PlanVersionOptions: panelpg.PlanVersionOptions{
			TrafficMode:     "fixed",
			TrafficPackages: []panelpg.TrafficPackage{{GB: 50, Currency: "RUB", AmountMinor: 500}},
		},
	}
	if _, err := panel.CreatePlanVersion(ctx, planID, input, actor); !errors.Is(err, panelpg.ErrValidaton) {
		t.Fatalf("packages on a fixed version: %v", err)
	}
	input.TrafficMode = "selectable"
	version, err := panel.CreatePlanVersion(ctx, planID, input, actor)
	if err != nil {
		t.Fatalf("publish a selectable version: %v", err)
	}
	if version.TrafficMode != "selectable" {
		t.Fatalf("published mode = %q", version.TrafficMode)
	}
	other := harness.customer(ctx, t)
	if quote, quoteErr := store.PreviewOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: other, PlanVersionID: version.ID, Currency: "RUB", Operation: "purchase",
		IdempotencyKey: "selectable-package", TrafficGB: 50,
	}); quoteErr != nil || quote.TotalMinor() != 1500 {
		t.Fatalf("the 50 GB package = %d, %v; want 1500", quote.TotalMinor(), quoteErr)
	}
	if _, err = store.PreviewOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: other, PlanVersionID: version.ID, Currency: "RUB", Operation: "purchase",
		IdempotencyKey: "selectable-other", TrafficGB: 60,
	}); !errors.Is(err, commercepg.ErrTrafficUnavailable) {
		t.Fatalf("a volume that is no package: %v", err)
	}
}
