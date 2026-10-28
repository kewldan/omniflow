//go:build integration

package integrationtest

import (
	"context"
	"testing"

	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/commercepg"
)

// orderOutcome reads what a payment left behind on an order.
func (harness *harness) orderOutcome(ctx context.Context, t *testing.T, orderID string) (string, int) {
	t.Helper()
	var state string
	var entitlements int
	if err := harness.pool.QueryRow(ctx, `SELECT o.state, (SELECT count(*) FROM entitlements e WHERE e.order_id = o.id)
		FROM orders o WHERE o.id = $1::uuid`, orderID).Scan(&state, &entitlements); err != nil {
		t.Fatalf("read order: %v", err)
	}
	return state, entitlements
}

// TestALatePaymentCannotSpendAPromotionTwice proves a one-per-customer code
// freed by a cancelled order and then spent by the next one is not spent a
// second time when the cancelled order is paid after all: the late payment
// does not settle and an operator is told.
func TestALatePaymentCannotSpendAPromotionTwice(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	store := commercepg.New(harness.pool, nil, testOptions())
	customerID := harness.customer(ctx, t)
	planVersionID := harness.catalog(ctx, t, "late-promo", 25000)
	harness.promotion(ctx, t, "LATEONCE", harness.planIDOf(ctx, t, planVersionID), 1)

	first, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: planVersionID, Currency: "RUB",
		Operation: "purchase", IdempotencyKey: "late-promo-first", PromoCode: "LATEONCE",
	})
	if err != nil {
		t.Fatalf("first order: %v", err)
	}
	if _, err := store.CancelOrder(ctx, uuidText(first.ID), "late-promo-cancel", "changed my mind"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	second, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: planVersionID, Currency: "RUB",
		Operation: "purchase", IdempotencyKey: "late-promo-second", PromoCode: "LATEONCE",
	})
	if err != nil {
		t.Fatalf("the cancelled order must have freed the code: %v", err)
	}
	harness.settle(ctx, t, store, uuidText(second.ID), second.ExternalMinor, "late-promo-second-payment")

	// The checkout of the cancelled order is paid in another tab.
	if classification := harness.settle(ctx, t, store, uuidText(first.ID), first.ExternalMinor, "late-promo-first-payment"); classification != commerce.ClassificationLimitExceeded {
		t.Fatalf("expected %q, got %q", commerce.ClassificationLimitExceeded, classification)
	}
	if state, entitlements := harness.orderOutcome(ctx, t, uuidText(first.ID)); state != "cancelled" || entitlements != 0 {
		t.Fatalf("the late payment settled: order %s with %d entitlements", state, entitlements)
	}
	var incidents int
	if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM operator_notifications
		WHERE kind = 'incident' AND payload ->> 'classification' = 'limit_exceeded'`).Scan(&incidents); err != nil {
		t.Fatalf("count incidents: %v", err)
	}
	if incidents != 1 {
		t.Fatalf("expected one operator incident, got %d", incidents)
	}
}

// TestALatePaymentCannotSpendATrialTwice is the same rule for the trial: a
// trial claim taken over by a live order is not handed back to the closed
// order its late payment arrives for — but one whose new holder closed too is.
func TestALatePaymentCannotSpendATrialTwice(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	store := commercepg.New(harness.pool, nil, testOptions())
	customerID := harness.customer(ctx, t)
	var planID, versionID string
	if err := harness.pool.QueryRow(ctx,
		`INSERT INTO plans (code, kind, visible) VALUES ('late-trial', 'trial', true) RETURNING id::text`).Scan(&planID); err != nil {
		t.Fatalf("create plan: %v", err)
	}
	if err := harness.pool.QueryRow(ctx,
		`INSERT INTO plan_versions (plan_id, version, billing_period, duration_seconds)
		 VALUES ($1::uuid, 1, 'week', 604800) RETURNING id::text`, planID).Scan(&versionID); err != nil {
		t.Fatalf("create plan version: %v", err)
	}
	if _, err := harness.pool.Exec(ctx,
		`INSERT INTO plan_prices (plan_version_id, currency, amount_minor) VALUES ($1::uuid, 'RUB', 1000)`, versionID); err != nil {
		t.Fatalf("price plan: %v", err)
	}
	trialOrder := func(key string) string {
		order, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
			CustomerID: customerID, PlanVersionID: versionID, Currency: "RUB",
			Operation: "purchase", IdempotencyKey: key,
		})
		if err != nil {
			t.Fatalf("trial order %s: %v", key, err)
		}
		return uuidText(order.ID)
	}

	first := trialOrder("late-trial-first")
	if _, err := harness.pool.Exec(ctx, `UPDATE orders SET state = 'expired' WHERE id = $1::uuid`, first); err != nil {
		t.Fatalf("expire: %v", err)
	}
	second := trialOrder("late-trial-second")

	if classification := harness.settle(ctx, t, store, first, 1000, "late-trial-first-payment"); classification != commerce.ClassificationLimitExceeded {
		t.Fatalf("a claim held by a live order must not be spent twice, got %q", classification)
	}
	if state, entitlements := harness.orderOutcome(ctx, t, first); state != "expired" || entitlements != 0 {
		t.Fatalf("the late trial settled: order %s with %d entitlements", state, entitlements)
	}

	// The second order closes unpaid too. The first one's payment, reported
	// again, now settles and takes the claim back.
	if _, err := harness.pool.Exec(ctx, `UPDATE orders SET state = 'cancelled' WHERE id = $1::uuid`, second); err != nil {
		t.Fatalf("cancel second: %v", err)
	}
	if classification := harness.settle(ctx, t, store, first, 1000, "late-trial-first-payment-again"); classification != commerce.ClassificationLate {
		t.Fatalf("with the claim free the late trial must settle, got %q", classification)
	}
	var claimed string
	if err := harness.pool.QueryRow(ctx, `SELECT order_id::text FROM trial_claims WHERE user_id = $1::uuid`, customerID).Scan(&claimed); err != nil {
		t.Fatalf("read claim: %v", err)
	}
	if claimed != first {
		t.Fatal("the settled trial order must hold the claim")
	}
}
