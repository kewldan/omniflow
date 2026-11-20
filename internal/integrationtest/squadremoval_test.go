//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"testing"

	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/commercepg"
)

const (
	planSquad  = "11111111-1111-1111-1111-111111111111"
	extraSquad = "22222222-2222-2222-2222-222222222222"
)

// squadAddon offers a squad add-on granting the given squads on a plan version.
func (harness *harness) squadAddon(ctx context.Context, t *testing.T, planVersionID, code string, amountMinor int64, squads ...string) string {
	t.Helper()
	var addonID, versionID string
	if err := harness.pool.QueryRow(ctx, `INSERT INTO addons (code, kind, visible) VALUES ($1, 'squads', true) RETURNING id::text`, code).Scan(&addonID); err != nil {
		t.Fatalf("create add-on: %v", err)
	}
	if _, err := harness.pool.Exec(ctx, `INSERT INTO addon_localizations (addon_id, locale, name) VALUES ($1::uuid, 'en', $2), ($1::uuid, 'ru', $2)`, addonID, code); err != nil {
		t.Fatalf("localize add-on: %v", err)
	}
	if err := harness.pool.QueryRow(ctx, `INSERT INTO addon_versions (addon_id, version, remnawave_squad_ids, max_quantity, proration)
		VALUES ($1::uuid, 1, $2::uuid[], 1, 'full_price') RETURNING id::text`, addonID, squads).Scan(&versionID); err != nil {
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

// Removing a paid squad add-on mid-period credits what is left of it once,
// takes off the squads it alone granted, keeps a squad the plan grants too,
// and pushes the result through one fulfillment operation.
func TestRemovingASquadAddonCreditsItAndKeepsThePlansSquads(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	customerID := harness.customer(ctx, t)
	planVersionID := harness.catalog(ctx, t, "squads-plan", 30000)
	if _, err := harness.pool.Exec(ctx, `UPDATE plan_versions SET remnawave_squad_ids = ARRAY[$2::uuid] WHERE id = $1::uuid`, planVersionID, planSquad); err != nil {
		t.Fatalf("plan squads: %v", err)
	}
	countries := harness.squadAddon(ctx, t, planVersionID, "countries", 4000, planSquad, extraSquad)
	harness.seedWallet(ctx, t, customerID, "squads-wallet", 100000)
	bought, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: planVersionID, Currency: "RUB", Operation: "purchase", IdempotencyKey: "squads-buy",
	})
	if err != nil {
		t.Fatalf("buy: %v", err)
	}
	subscriptionID := uuidText(bought.SubscriptionID)
	harness.halfwayThrough(ctx, t, subscriptionID)
	if _, err = store.CreateAddonOrder(ctx, commercepg.AddonOrderInput{
		CustomerID: customerID, SubscriptionID: subscriptionID, Currency: "RUB", IdempotencyKey: "squads-addon",
		Addons: []commercepg.AddonSelection{{AddonVersionID: countries, Quantity: 1}},
	}); err != nil {
		t.Fatalf("buy the add-on: %v", err)
	}
	options, err := store.SquadRemovals(ctx, customerID, subscriptionID, "RUB")
	if err != nil || len(options) != 1 || options[0].Quote.CreditMinor < 3990 || options[0].Quote.CreditMinor > 4000 {
		t.Fatalf("removals = %+v, %v", options, err)
	}
	input := commercepg.CapacityChangeInput{CustomerID: customerID, SubscriptionID: subscriptionID, Currency: "RUB", AddonPackID: options[0].PackID, IdempotencyKey: "squads-remove"}
	before := harness.walletBalance(ctx, t, customerID)
	for range 2 {
		if _, err = store.CreateSquadRemovalOrder(ctx, input); err != nil {
			t.Fatalf("remove: %v", err)
		}
	}
	if after := harness.walletBalance(ctx, t, customerID); after != before+options[0].Quote.CreditMinor {
		t.Fatalf("wallet = %d, want %d", after, before+options[0].Quote.CreditMinor)
	}
	var squads []string
	if err = harness.pool.QueryRow(ctx, `SELECT remnawave_squad_ids::text[] FROM entitlements
		WHERE subscription_id = $1::uuid AND status <> 'superseded' ORDER BY ends_at DESC LIMIT 1`, subscriptionID).Scan(&squads); err != nil {
		t.Fatalf("read squads: %v", err)
	}
	if len(squads) != 1 || squads[0] != planSquad {
		t.Fatalf("squads after removal = %v, want only the plan's", squads)
	}
	var changes int
	if err = harness.pool.QueryRow(ctx, `SELECT count(*) FROM fulfillment_operations WHERE idempotency_key LIKE 'order:%:change'`).Scan(&changes); err != nil || changes != 1 {
		t.Fatalf("change operations = %d, %v", changes, err)
	}
	again := input
	again.IdempotencyKey = "squads-remove-again"
	if _, err = store.CreateSquadRemovalOrder(ctx, again); !errors.Is(err, commerce.ErrSquadRemovalUnavailable) {
		t.Fatalf("removing it twice: %v", err)
	}
}
