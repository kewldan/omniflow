//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"testing"

	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/commercepg"
)

// deviceAddon offers a device-slot add-on on a plan version.
func (harness *harness) deviceAddon(ctx context.Context, t *testing.T, planVersionID, code string, amountMinor int64, slots int) string {
	t.Helper()
	var addonID, versionID string
	if err := harness.pool.QueryRow(ctx, `INSERT INTO addons (code, kind, visible) VALUES ($1, 'devices', true) RETURNING id::text`, code).Scan(&addonID); err != nil {
		t.Fatalf("create add-on: %v", err)
	}
	if _, err := harness.pool.Exec(ctx, `INSERT INTO addon_localizations (addon_id, locale, name) VALUES ($1::uuid, 'en', $2), ($1::uuid, 'ru', $2)`, addonID, code); err != nil {
		t.Fatalf("localize add-on: %v", err)
	}
	if err := harness.pool.QueryRow(ctx, `INSERT INTO addon_versions (addon_id, version, device_slots, max_quantity, proration)
		VALUES ($1::uuid, 1, $2, 5, 'full_price') RETURNING id::text`, addonID, slots).Scan(&versionID); err != nil {
		t.Fatalf("create add-on version: %v", err)
	}
	if _, err := harness.pool.Exec(ctx, `INSERT INTO addon_prices (addon_version_id, currency, amount_minor) VALUES ($1::uuid, 'RUB', $2)`, versionID, amountMinor); err != nil {
		t.Fatalf("price add-on: %v", err)
	}
	if _, err := harness.pool.Exec(ctx, `INSERT INTO plan_version_addons (plan_version_id, addon_id) VALUES ($1::uuid, $2::uuid)`, planVersionID, addonID); err != nil {
		t.Fatalf("offer add-on: %v", err)
	}
	return versionID
}

func (harness *harness) deviceLimit(ctx context.Context, t *testing.T, subscriptionID string) int {
	t.Helper()
	var limit int
	if err := harness.pool.QueryRow(ctx, `SELECT device_limit FROM entitlements
		WHERE subscription_id = $1::uuid AND status <> 'superseded' ORDER BY ends_at DESC LIMIT 1`, subscriptionID).Scan(&limit); err != nil {
		t.Fatalf("read device limit: %v", err)
	}
	return limit
}

// A customer gives back bought device slots: never below the plan's own
// limit, never while more devices are connected than the new limit, and the
// credit for the slots given back lands in the wallet once.
func TestADeviceReductionCreditsBoughtSlotsOnce(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	customerID := harness.customer(ctx, t)
	planVersionID := harness.catalog(ctx, t, "devices-plan", 30000)
	slots := harness.deviceAddon(ctx, t, planVersionID, "two-slots", 6000, 2)
	harness.seedWallet(ctx, t, customerID, "devices-wallet", 100000)
	bought, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: planVersionID, Currency: "RUB", Operation: "purchase",
		IdempotencyKey: "devices-buy", Addons: []commercepg.AddonSelection{{AddonVersionID: slots, Quantity: 1}},
	})
	if err != nil {
		t.Fatalf("buy: %v", err)
	}
	subscriptionID := uuidText(bought.SubscriptionID)
	harness.halfwayWithPacks(ctx, t, subscriptionID)
	current, minimum, ok, err := store.DeviceLimitRange(ctx, customerID, subscriptionID)
	if err != nil || !ok || current != 5 || minimum != 3 {
		t.Fatalf("range = %d..%d %v, %v", minimum, current, ok, err)
	}
	input := commercepg.CapacityChangeInput{CustomerID: customerID, SubscriptionID: subscriptionID, Currency: "RUB", DeviceLimit: 4, ConnectedDevices: 5, IdempotencyKey: "devices-down"}
	quote, err := store.DeviceReductionQuote(ctx, input)
	// One of two slots, with half the period left: a quarter of 6000.
	if err != nil || quote.ExcessDevices != 1 || quote.CreditMinor < 1495 || quote.CreditMinor > 1500 || quote.ChargeMinor != 0 {
		t.Fatalf("quote = %+v, %v", quote, err)
	}
	if _, err = store.CreateDeviceReductionOrder(ctx, input); !errors.Is(err, commerce.ErrDevicesOverLimit) {
		t.Fatalf("too many devices connected: %v", err)
	}
	input.ConnectedDevices = 4
	before := harness.walletBalance(ctx, t, customerID)
	for range 2 {
		if _, err = store.CreateDeviceReductionOrder(ctx, input); err != nil {
			t.Fatalf("reduce: %v", err)
		}
	}
	if after := harness.walletBalance(ctx, t, customerID); after != before+quote.CreditMinor {
		t.Fatalf("wallet = %d, want %d", after, before+quote.CreditMinor)
	}
	if limit := harness.deviceLimit(ctx, t, subscriptionID); limit != 4 {
		t.Fatalf("device limit = %d", limit)
	}
	below := input
	below.DeviceLimit, below.IdempotencyKey = 2, "devices-below"
	if _, err = store.CreateDeviceReductionOrder(ctx, below); !errors.Is(err, commerce.ErrDeviceReductionUnavailable) {
		t.Fatalf("below the plan's own limit: %v", err)
	}
	last := input
	last.DeviceLimit, last.ConnectedDevices, last.IdempotencyKey = 3, 1, "devices-last"
	if _, err = store.CreateDeviceReductionOrder(ctx, last); err != nil {
		t.Fatalf("give back the last slot: %v", err)
	}
	var released int
	if err = harness.pool.QueryRow(ctx, `SELECT count(*) FROM entitlement_addons WHERE released_at IS NOT NULL AND released_slots = 2`).Scan(&released); err != nil || released != 1 {
		t.Fatalf("emptied pack released = %d, %v", released, err)
	}
	if _, _, ok, err = store.DeviceLimitRange(ctx, customerID, subscriptionID); err != nil || ok {
		t.Fatalf("nothing left to give back: %v, %v", ok, err)
	}
}
