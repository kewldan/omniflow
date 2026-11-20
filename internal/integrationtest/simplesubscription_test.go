//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"testing"

	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/panelpg"
)

// The simple subscription shows one plan priced with its preselected
// period, traffic package, and extra devices, and its Buy button opens a
// checkout with exactly those chosen.
func TestSimpleSubscriptionOpensAPreconfiguredCheckout(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	panel := newOperations(t, harness)
	actor := harness.operator(ctx, t, "simple@example.test")
	store := harness.commerceStore(t, testOptions())
	planID := harness.planID(ctx, t, harness.catalog(ctx, t, "simple-plan", 0))
	var addonID string
	if err := harness.pool.QueryRow(ctx, `WITH a AS (
			INSERT INTO addons (code, kind, visible) VALUES ('simple-slots', 'devices', true) RETURNING id
		), v AS (
			INSERT INTO addon_versions (addon_id, version, device_slots, max_quantity, proration)
			SELECT id, 1, 1, 5, 'full_price' FROM a RETURNING id, addon_id
		), p AS (
			INSERT INTO addon_prices (addon_version_id, currency, amount_minor) SELECT id, 'RUB', 100 FROM v
		)
		SELECT addon_id::text FROM v`).Scan(&addonID); err != nil {
		t.Fatalf("create a devices add-on: %v", err)
	}
	own, devices := int64(10)*commerce.GiB, int32(3)
	if _, err := panel.CreatePlanVersion(ctx, planID, panelpg.PlanVersionInput{
		BillingPeriod: "month", DurationSeconds: 30 * 86_400, SquadSelection: "automatic",
		UpgradePolicy: "extend", DowngradePolicy: "at_expiry", CancellationPolicy: "at_expiry",
		TrialEligibility: "new_customer", Prices: map[string]int64{"RUB": 1000},
		TrafficAllowanceBytes: &own, DeviceLimit: &devices, AddonIDs: []string{addonID},
		PlanVersionOptions: panelpg.PlanVersionOptions{
			Periods:         []panelpg.PlanPeriod{{Days: 90, Currency: "RUB", AmountMinor: 2500, ForPurchase: true, ForRenewal: true}},
			TrafficPackages: []panelpg.TrafficPackage{{GB: 50, Currency: "RUB", AmountMinor: 300}},
		},
	}, actor); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if _, err := panel.SaveSimpleSubscription(ctx, panelpg.SimpleSubscription{Enabled: true}, actor); !errors.Is(err, panelpg.ErrValidaton) {
		t.Fatalf("an enabled setting without a plan: %v", err)
	}
	customerID := harness.customer(ctx, t)
	checkout := newAccountCheckout(t, harness, store)
	if _, err := checkout.SimpleOffer(ctx, customerID, "en"); err == nil {
		t.Fatal("an offer while the setting is off")
	}
	saved, err := panel.SaveSimpleSubscription(ctx, panelpg.SimpleSubscription{
		Enabled: true, PlanID: planID, PeriodDays: 90, TrafficGB: 50, DeviceAddonID: addonID, DeviceQuantity: 2,
	}, actor)
	if err != nil || !saved.Enabled || saved.PeriodDays != 90 {
		t.Fatalf("save: %+v, %v", saved, err)
	}
	// 90 days 2500, the 50 GB package scaled to 90 days 900, two slots 200.
	offer, err := checkout.SimpleOffer(ctx, customerID, "en")
	if err != nil || offer.TotalMinor != 3600 || offer.DeviceQuantity*offer.DeviceSlots != 2 {
		t.Fatalf("offer = %+v, %v", offer, err)
	}
	view, err := checkout.OpenSimple(ctx, customerID, "en")
	if err != nil {
		t.Fatalf("buy: %v", err)
	}
	if view.PeriodDays != 90 || view.TrafficGB != 50 || len(view.SelectedAddons) != 1 || view.SelectedAddons[0].Quantity != 2 ||
		view.Quote.Subtotal.Amount != 3400 || view.Quote.AddonMinor != 200 {
		t.Fatalf("checkout = period %d, traffic %d, add-ons %+v, subtotal %+v",
			view.PeriodDays, view.TrafficGB, view.SelectedAddons, view.Quote.Subtotal)
	}
}
