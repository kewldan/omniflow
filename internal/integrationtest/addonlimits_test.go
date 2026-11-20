//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"testing"

	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/panelpg"
)

// A plan may cap the traffic add-ons add, the installation may cap devices,
// and a traffic pack with a lifetime is taken back off the entitlement when it
// runs out — once, however often the maintenance pass runs.
func TestAddonCapsAndTrafficPackLifetime(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	customerID := harness.customer(ctx, t)
	planVersionID := harness.catalog(ctx, t, "capped-plan", 0)
	const gib = int64(1 << 30)
	if _, err := harness.pool.Exec(ctx, `UPDATE plan_versions SET max_addon_traffic_bytes = $2 WHERE id = $1::uuid`, planVersionID, 15*gib); err != nil {
		t.Fatalf("cap bought traffic: %v", err)
	}
	pack := harness.addon(ctx, t, planVersionID, "capped-pack", 0, 10*gib, "full_price")
	if _, err := harness.pool.Exec(ctx, `UPDATE addon_versions SET lifetime_days = 30 WHERE id = $1::uuid`, pack); err != nil {
		t.Fatalf("give the pack a lifetime: %v", err)
	}
	if _, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: planVersionID, Currency: "RUB",
		Operation: "purchase", IdempotencyKey: "capped-plan-order",
	}); err != nil {
		t.Fatalf("buy the plan: %v", err)
	}
	if _, err := store.CreateAddonOrder(ctx, commercepg.AddonOrderInput{
		CustomerID: customerID, Currency: "RUB", IdempotencyKey: "capped-pack-1",
		Addons: []commercepg.AddonSelection{{AddonVersionID: pack, Quantity: 1}},
	}); err != nil {
		t.Fatalf("buy the first pack: %v", err)
	}
	if _, err := store.CreateAddonOrder(ctx, commercepg.AddonOrderInput{
		CustomerID: customerID, Currency: "RUB", IdempotencyKey: "capped-pack-2",
		Addons: []commercepg.AddonSelection{{AddonVersionID: pack, Quantity: 1}},
	}); !errors.Is(err, commerce.ErrAddonLimit) {
		t.Fatalf("a pack past the cap: %v, want addon_limit_reached", err)
	}

	allowance := func() int64 {
		var bytes int64
		if err := harness.pool.QueryRow(ctx, `SELECT traffic_allowance_bytes FROM entitlements
			WHERE user_id = $1::uuid AND status <> 'superseded'`, customerID).Scan(&bytes); err != nil {
			t.Fatalf("read allowance: %v", err)
		}
		return bytes
	}
	if got := allowance(); got != 110*gib {
		t.Fatalf("allowance with the pack = %d", got)
	}
	if _, err := harness.pool.Exec(ctx, `UPDATE entitlement_addons SET expires_at = now() - interval '1 minute'
		WHERE expires_at IS NOT NULL`); err != nil {
		t.Fatalf("run the pack out: %v", err)
	}
	for range 2 {
		if _, err := store.ExpireTrafficPacks(ctx); err != nil {
			t.Fatalf("expire packs: %v", err)
		}
	}
	if got := allowance(); got != 100*gib {
		t.Fatalf("allowance after the pack ran out = %d, want the plan's alone", got)
	}
	var operations int
	if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM fulfillment_operations WHERE idempotency_key LIKE 'addon:%:expire'`).Scan(&operations); err != nil {
		t.Fatalf("count operations: %v", err)
	}
	if operations != 1 {
		t.Fatalf("expiry operations = %d, want one", operations)
	}

	// The installation's device ceiling refuses slots past it.
	harness.savePolicy(ctx, t, func(policy *panelpg.SubscriptionPolicy) {
		ceiling := int32(3)
		policy.MaxDeviceLimit = &ceiling
	})
	var slotsAddon string
	if err := harness.pool.QueryRow(ctx, `WITH a AS (
			INSERT INTO addons (code, kind, visible) VALUES ('slots', 'devices', true) RETURNING id
		), v AS (
			INSERT INTO addon_versions (addon_id, version, device_slots, max_quantity, proration)
			SELECT id, 1, 1, 5, 'full_price' FROM a RETURNING id, addon_id
		), p AS (
			INSERT INTO addon_prices (addon_version_id, currency, amount_minor) SELECT id, 'RUB', 0 FROM v
		), o AS (
			INSERT INTO plan_version_addons (plan_version_id, addon_id) SELECT $1::uuid, addon_id FROM v
		)
		SELECT id::text FROM v`, planVersionID).Scan(&slotsAddon); err != nil {
		t.Fatalf("create a device add-on: %v", err)
	}
	if _, err := store.CreateAddonOrder(ctx, commercepg.AddonOrderInput{
		CustomerID: customerID, Currency: "RUB", IdempotencyKey: "capped-slots",
		Addons: []commercepg.AddonSelection{{AddonVersionID: slotsAddon, Quantity: 1}},
	}); !errors.Is(err, commerce.ErrAddonLimit) {
		t.Fatalf("a slot past the device ceiling: %v, want addon_limit_reached", err)
	}
}
