//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"testing"

	"github.com/omniflow/omniflow/internal/commercepg"
)

// TestAPromoCodeIsNotBurnedByAnOrderThatClosesUnpaid proves the redemption
// accounting: a one-per-customer code survives "pressed Pay, changed my mind",
// but is still held by a live pending order so two parallel checkouts cannot
// both use it.
func TestAPromoCodeIsNotBurnedByAnOrderThatClosesUnpaid(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	store := commercepg.New(harness.pool, nil, testOptions())
	customerID := harness.customer(ctx, t)
	planVersionID := harness.catalog(ctx, t, "promo-plan", 25000)
	harness.promotion(ctx, t, "ONCE", harness.planIDOf(ctx, t, planVersionID), 1)

	first, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: planVersionID, Currency: "RUB",
		Operation: "purchase", IdempotencyKey: "promo-first", PromoCode: "ONCE",
	})
	if err != nil {
		t.Fatalf("first order: %v", err)
	}
	if first.DiscountMinor != 2500 {
		t.Fatalf("the code was not applied: discount %d", first.DiscountMinor)
	}
	// While the first order is pending the code is held.
	if _, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: planVersionID, Currency: "RUB",
		Operation: "purchase", IdempotencyKey: "promo-parallel", PromoCode: "ONCE",
	}); !errors.Is(err, commercepg.ErrPromoExhausted) {
		t.Fatalf("a pending order must hold the code, got %v", err)
	}
	// The customer changes their mind.
	if _, err := harness.pool.Exec(ctx, `UPDATE orders SET state = 'cancelled' WHERE id = $1`, first.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	second, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: planVersionID, Currency: "RUB",
		Operation: "purchase", IdempotencyKey: "promo-second", PromoCode: "ONCE",
	})
	if err != nil {
		t.Fatalf("a cancelled order must give the code back: %v", err)
	}
	if second.DiscountMinor != 2500 {
		t.Fatalf("the code was not applied the second time: discount %d", second.DiscountMinor)
	}
	// Once an order settles, the redemption is spent for good.
	harness.settle(ctx, t, store, uuidText(second.ID), second.ExternalMinor, "event-promo")
	if _, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: planVersionID, Currency: "RUB",
		Operation: "purchase", IdempotencyKey: "promo-third", PromoCode: "ONCE",
	}); !errors.Is(err, commercepg.ErrPromoExhausted) {
		t.Fatalf("a settled redemption must count, got %v", err)
	}
}

// TestATrialClaimBehindAClosedOrderIsReclaimed is the same rule for the one
// trial a customer gets: an order that closed unpaid does not spend it, while a
// live or settled one does.
func TestATrialClaimBehindAClosedOrderIsReclaimed(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	store := commercepg.New(harness.pool, nil, testOptions())
	customerID := harness.customer(ctx, t)
	var planID, versionID string
	if err := harness.pool.QueryRow(ctx,
		`INSERT INTO plans (code, kind, visible) VALUES ('trial-plan', 'trial', true) RETURNING id::text`).Scan(&planID); err != nil {
		t.Fatalf("create plan: %v", err)
	}
	if _, err := harness.pool.Exec(ctx,
		`INSERT INTO plan_localizations (plan_id, locale, name) VALUES ($1::uuid, 'en', 'Trial'), ($1::uuid, 'ru', 'Trial')`, planID); err != nil {
		t.Fatalf("localize plan: %v", err)
	}
	if err := harness.pool.QueryRow(ctx,
		`INSERT INTO plan_versions (plan_id, version, billing_period, duration_seconds)
		 VALUES ($1::uuid, 1, 'week', 604800) RETURNING id::text`, planID).Scan(&versionID); err != nil {
		t.Fatalf("create plan version: %v", err)
	}
	// A priced trial, so the order waits for a payment and can close unpaid.
	if _, err := harness.pool.Exec(ctx,
		`INSERT INTO plan_prices (plan_version_id, currency, amount_minor) VALUES ($1::uuid, 'RUB', 1000)`, versionID); err != nil {
		t.Fatalf("price plan: %v", err)
	}

	first, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: versionID, Currency: "RUB",
		Operation: "purchase", IdempotencyKey: "trial-first",
	})
	if err != nil {
		t.Fatalf("first trial order: %v", err)
	}
	if _, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: versionID, Currency: "RUB",
		Operation: "purchase", IdempotencyKey: "trial-parallel",
	}); !errors.Is(err, commercepg.ErrTrialAlreadyClaimed) {
		t.Fatalf("a pending trial order must hold the claim, got %v", err)
	}
	if _, err := harness.pool.Exec(ctx, `UPDATE orders SET state = 'expired' WHERE id = $1`, first.ID); err != nil {
		t.Fatalf("expire: %v", err)
	}
	second, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: versionID, Currency: "RUB",
		Operation: "purchase", IdempotencyKey: "trial-second",
	})
	if err != nil {
		t.Fatalf("an expired trial order must give the claim back: %v", err)
	}
	var claimedOrder string
	if err := harness.pool.QueryRow(ctx, `SELECT order_id::text FROM trial_claims WHERE user_id = $1::uuid`, customerID).Scan(&claimedOrder); err != nil {
		t.Fatalf("read claim: %v", err)
	}
	if claimedOrder != uuidText(second.ID) {
		t.Fatalf("the claim still points at the closed order")
	}
}
