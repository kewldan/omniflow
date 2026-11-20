//go:build integration

package integrationtest

import (
	"context"
	"testing"

	"github.com/omniflow/omniflow/internal/commercepg"
)

// halfwayThrough moves a subscription's current entitlement so that half of
// its thirty days have run.
func (harness *harness) halfwayThrough(ctx context.Context, t *testing.T, subscriptionID string) {
	t.Helper()
	if _, err := harness.pool.Exec(ctx, `UPDATE entitlements
		SET starts_at = now() - interval '15 days', ends_at = now() + interval '15 days'
		WHERE subscription_id = $1::uuid AND status <> 'superseded'`, subscriptionID); err != nil {
		t.Fatalf("move the period: %v", err)
	}
}

// With proration on, an upgrade that replaces the period credits what is left
// of it at what the customer paid; the credit is a ledger movement posted once
// however often the order is replayed, and the order still adds up. A
// downgrade whose credit exceeds the new price leaves the rest in the wallet.
func TestAProratedPlanChangeCreditsTheRemainderOnce(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	customerID := harness.customer(ctx, t)
	basic := harness.catalog(ctx, t, "prorate-basic", 30000)
	premium := harness.catalog(ctx, t, "prorate-premium", 60000)
	lite := harness.catalog(ctx, t, "prorate-lite", 10000)
	if _, err := harness.pool.Exec(ctx, `UPDATE plan_versions SET upgrade_policy = 'replace', downgrade_policy = 'immediate'
		WHERE id IN ($1::uuid, $2::uuid, $3::uuid)`, basic, premium, lite); err != nil {
		t.Fatalf("policies: %v", err)
	}
	harness.seedWallet(ctx, t, customerID, "prorate-wallet", 100000)
	bought, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: basic, Currency: "RUB", Operation: "purchase", IdempotencyKey: "prorate-buy",
	})
	if err != nil {
		t.Fatalf("buy: %v", err)
	}
	subscriptionID := uuidText(bought.SubscriptionID)
	harness.halfwayThrough(ctx, t, subscriptionID)

	upgrade := commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: premium, Currency: "RUB", Operation: "upgrade",
		SubscriptionID: subscriptionID, IdempotencyKey: "prorate-up",
	}
	quote, err := store.PreviewOrder(ctx, upgrade)
	if err != nil || quote.CreditMinor != 0 {
		t.Fatalf("proration off: credit = %d, %v", quote.CreditMinor, err)
	}
	if _, err = harness.pool.Exec(ctx, `UPDATE subscription_policy SET prorate_plan_changes = true`); err != nil {
		t.Fatalf("prorate: %v", err)
	}
	quote, err = store.PreviewOrder(ctx, upgrade)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	// Fifteen of thirty paid days are left: half of 30000, less a few
	// seconds of rounding down.
	if quote.CreditMinor < 14990 || quote.CreditMinor > 15000 || quote.CreditAppliedMinor != quote.CreditMinor || quote.CreditToWalletMinor != 0 {
		t.Fatalf("upgrade quote = %+v", quote)
	}
	if quote.WalletFundsMinor+quote.CreditAppliedMinor+quote.ExternalMinor != 60000 {
		t.Fatalf("the quote does not add up: %+v", quote)
	}
	before := harness.walletBalance(ctx, t, customerID)
	for range 2 {
		if _, err = store.CreateOrder(ctx, upgrade); err != nil {
			t.Fatalf("upgrade: %v", err)
		}
	}
	credit := quote.CreditMinor
	if after := harness.walletBalance(ctx, t, customerID); after != before+credit-60000 {
		t.Fatalf("wallet = %d, want %d", after, before+credit-60000)
	}
	var posted int
	if err = harness.pool.QueryRow(ctx, `SELECT count(*) FROM ledger_transactions WHERE reference_type = 'order_credit'`).Scan(&posted); err != nil || posted != 1 {
		t.Fatalf("credit transactions = %d, %v", posted, err)
	}

	// The upgrade's own period has just started: a downgrade now hands back
	// nearly all of it, which is more than the cheaper plan costs.
	downgrade := commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: lite, Currency: "RUB", Operation: "downgrade",
		SubscriptionID: subscriptionID, IdempotencyKey: "prorate-down",
	}
	quote, err = store.PreviewOrder(ctx, downgrade)
	if err != nil || quote.CreditMinor < 59900 || quote.CreditAppliedMinor != 10000 || quote.CreditToWalletMinor != quote.CreditMinor-10000 || quote.ExternalMinor != 0 {
		t.Fatalf("downgrade quote = %+v, %v", quote, err)
	}
	before = harness.walletBalance(ctx, t, customerID)
	if _, err = store.CreateOrder(ctx, downgrade); err != nil {
		t.Fatalf("downgrade: %v", err)
	}
	if after := harness.walletBalance(ctx, t, customerID); after != before+quote.CreditToWalletMinor {
		t.Fatalf("wallet after downgrade = %d, want %d", after, before+quote.CreditToWalletMinor)
	}
}

// Two change orders priced against the same remainder: the one that settles
// first posts it, and the other cannot spend it again. A provider payment for
// the second finds its wallet part unfunded and does not settle.
func TestARemainderIsHandedBackOnceAcrossTwoChangeOrders(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	customerID := harness.customer(ctx, t)
	basic := harness.catalog(ctx, t, "race-basic", 30000)
	premium := harness.catalog(ctx, t, "race-premium", 60000)
	if _, err := harness.pool.Exec(ctx, `UPDATE plan_versions SET upgrade_policy = 'replace' WHERE id = $1::uuid`, premium); err != nil {
		t.Fatalf("policy: %v", err)
	}
	if _, err := harness.pool.Exec(ctx, `UPDATE subscription_policy SET prorate_plan_changes = true`); err != nil {
		t.Fatalf("prorate: %v", err)
	}
	harness.seedWallet(ctx, t, customerID, "race-wallet", 30000)
	bought, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: basic, Currency: "RUB", Operation: "purchase", IdempotencyKey: "race-buy",
	})
	if err != nil {
		t.Fatalf("buy: %v", err)
	}
	subscriptionID := uuidText(bought.SubscriptionID)
	harness.halfwayThrough(ctx, t, subscriptionID)
	// The first change is left waiting for a provider, its wallet part being
	// the credit alone.
	pending, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: premium, Currency: "RUB", Operation: "upgrade",
		SubscriptionID: subscriptionID, IdempotencyKey: "race-first", SkipWallet: true,
	})
	if err != nil || pending.State != "pending" || pending.WalletMinor < 14990 {
		t.Fatalf("pending change = %+v, %v", pending, err)
	}
	// The customer tops up and the second change settles from the wallet,
	// posting the remainder.
	harness.seedWallet(ctx, t, customerID, "race-topup", 60000)
	settled, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: premium, Currency: "RUB", Operation: "upgrade",
		SubscriptionID: subscriptionID, IdempotencyKey: "race-second",
	})
	if err != nil || settled.State != "paid" {
		t.Fatalf("second change = %+v, %v", settled, err)
	}
	balance := harness.walletBalance(ctx, t, customerID)
	// Drain what the wallet holds so nothing but the stale credit could fund
	// the first order's wallet part.
	if balance > 0 {
		harness.seedWallet(ctx, t, customerID, "race-drain", -balance)
	}
	classification := harness.settle(ctx, t, store, uuidText(pending.ID), pending.ExternalMinor, "race-event")
	if classification != "wallet_unavailable" {
		t.Fatalf("classification = %s, want wallet_unavailable", classification)
	}
	var posted int
	if err = harness.pool.QueryRow(ctx, `SELECT count(*) FROM order_credits WHERE credited_at IS NOT NULL`).Scan(&posted); err != nil || posted != 1 {
		t.Fatalf("posted credits = %d, %v", posted, err)
	}
}
