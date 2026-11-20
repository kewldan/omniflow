//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"testing"

	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/commercepg"
)

const gigabyte = int64(1) << 30

// halfwayWithPacks moves a subscription's current period, and the packs
// applied to it, so that half of its thirty days have run.
func (harness *harness) halfwayWithPacks(ctx context.Context, t *testing.T, subscriptionID string) {
	t.Helper()
	harness.halfwayThrough(ctx, t, subscriptionID)
	if _, err := harness.pool.Exec(ctx, `UPDATE entitlement_addons ea SET applied_at = e.starts_at
		FROM entitlements e WHERE e.id = ea.entitlement_id AND e.subscription_id = $1::uuid`, subscriptionID); err != nil {
		t.Fatalf("move the packs: %v", err)
	}
}

func (harness *harness) entitlementTraffic(ctx context.Context, t *testing.T, subscriptionID string) int64 {
	t.Helper()
	var traffic int64
	if err := harness.pool.QueryRow(ctx, `SELECT traffic_allowance_bytes FROM entitlements
		WHERE subscription_id = $1::uuid AND status <> 'superseded' ORDER BY ends_at DESC LIMIT 1`, subscriptionID).Scan(&traffic); err != nil {
		t.Fatalf("read traffic: %v", err)
	}
	return traffic
}

// A bigger traffic package costs its prorated price less what is left of the
// one it replaces; a smaller one credits the difference to the wallet. Each
// change is one order, settles once however often it is replayed, releases
// the old pack once, and queues one set_limits.
func TestATrafficPackageChangeChargesUpAndCreditsDown(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	customerID := harness.customer(ctx, t)
	planVersionID := harness.catalog(ctx, t, "package-plan", 30000)
	small := harness.addon(ctx, t, planVersionID, "pack-small", 3000, 10*gigabyte, "remaining_period")
	big := harness.addon(ctx, t, planVersionID, "pack-big", 10000, 50*gigabyte, "remaining_period")
	harness.seedWallet(ctx, t, customerID, "package-wallet", 100000)
	bought, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: planVersionID, Currency: "RUB", Operation: "purchase",
		IdempotencyKey: "package-buy", Addons: []commercepg.AddonSelection{{AddonVersionID: small, Quantity: 1}},
	})
	if err != nil {
		t.Fatalf("buy: %v", err)
	}
	subscriptionID := uuidText(bought.SubscriptionID)
	if traffic := harness.entitlementTraffic(ctx, t, subscriptionID); traffic != 110*gigabyte {
		t.Fatalf("traffic after purchase = %d", traffic)
	}
	harness.halfwayWithPacks(ctx, t, subscriptionID)

	up := commercepg.CapacityChangeInput{CustomerID: customerID, SubscriptionID: subscriptionID, Currency: "RUB", AddonVersionID: big, IdempotencyKey: "package-up"}
	quote, err := store.TrafficChangeQuote(ctx, up)
	if err != nil {
		t.Fatalf("quote up: %v", err)
	}
	if quote.ChargeMinor != 5000 || quote.CreditMinor < 1495 || quote.CreditMinor > 1500 || quote.CurrentTrafficBytes != 10*gigabyte || quote.TargetTrafficBytes != 50*gigabyte {
		t.Fatalf("up quote = %+v", quote)
	}
	before := harness.walletBalance(ctx, t, customerID)
	for range 2 {
		if _, err = store.CreateTrafficChangeOrder(ctx, up); err != nil {
			t.Fatalf("change up: %v", err)
		}
	}
	if after := harness.walletBalance(ctx, t, customerID); after != before-5000+quote.CreditMinor {
		t.Fatalf("wallet after up = %d, want %d", after, before-5000+quote.CreditMinor)
	}
	if traffic := harness.entitlementTraffic(ctx, t, subscriptionID); traffic != 150*gigabyte {
		t.Fatalf("traffic after up = %d", traffic)
	}
	var released, changes int
	if err = harness.pool.QueryRow(ctx, `SELECT count(*) FROM entitlement_addons WHERE released_at IS NOT NULL`).Scan(&released); err != nil || released != 1 {
		t.Fatalf("released packs = %d, %v", released, err)
	}
	if err = harness.pool.QueryRow(ctx, `SELECT count(*) FROM fulfillment_operations WHERE idempotency_key LIKE 'order:%:change'`).Scan(&changes); err != nil || changes != 1 {
		t.Fatalf("change operations = %d, %v", changes, err)
	}

	// Dropping the package hands back nearly all of what was just paid for it.
	down := commercepg.CapacityChangeInput{CustomerID: customerID, SubscriptionID: subscriptionID, Currency: "RUB", IdempotencyKey: "package-down"}
	quote, err = store.TrafficChangeQuote(ctx, down)
	if err != nil || quote.ChargeMinor != 0 || quote.CreditMinor < 4990 || quote.CreditToWalletMinor != quote.CreditMinor {
		t.Fatalf("down quote = %+v, %v", quote, err)
	}
	before = harness.walletBalance(ctx, t, customerID)
	order, err := store.CreateTrafficChangeOrder(ctx, down)
	if err != nil || order.State != "paid" || order.SubtotalMinor != 0 {
		t.Fatalf("change down = %+v, %v", order, err)
	}
	if after := harness.walletBalance(ctx, t, customerID); after != before+quote.CreditMinor {
		t.Fatalf("wallet after down = %d, want %d", after, before+quote.CreditMinor)
	}
	if traffic := harness.entitlementTraffic(ctx, t, subscriptionID); traffic != 100*gigabyte {
		t.Fatalf("traffic after down = %d", traffic)
	}
	if _, err = store.TrafficChangeQuote(ctx, down); !errors.Is(err, commerce.ErrCapacityChangeSame) {
		t.Fatalf("nothing left to drop: %v", err)
	}

	if _, err = harness.pool.Exec(ctx, `UPDATE entitlements SET traffic_allowance_bytes = NULL WHERE subscription_id = $1::uuid`, subscriptionID); err != nil {
		t.Fatalf("make it unlimited: %v", err)
	}
	if _, err = store.TrafficChangeQuote(ctx, up); !errors.Is(err, commerce.ErrTrafficChangeUnavailable) {
		t.Fatalf("unlimited traffic: %v", err)
	}
}
