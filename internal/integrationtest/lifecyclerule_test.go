//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"testing"

	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/commercepg"
)

// TestOrderCreationEnforcesTheLifecycleRuleForEveryCaller proves the rule the
// storefronts render is enforced where orders are created, so a caller that
// skips the storefront — the bot, the panel, the bearer-token API, a saved
// cart — meets the same refusal: no purchase on top of a live subscription,
// and an operation that names the change between the plans correctly.
func TestOrderCreationEnforcesTheLifecycleRuleForEveryCaller(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	store := commercepg.New(harness.pool, nil, testOptions())
	customerID := harness.customer(ctx, t)

	heldOrder, _, _ := harness.paidOrder(ctx, t, store, customerID, "rule-basic", 30000)
	var basic, subscriptionID string
	if err := harness.pool.QueryRow(ctx, `SELECT plan_version_id::text, subscription_id::text FROM entitlements
		WHERE order_id = $1::uuid`, heldOrder).Scan(&basic, &subscriptionID); err != nil {
		t.Fatalf("read held: %v", err)
	}
	pro := harness.catalog(ctx, t, "rule-pro", 60000)
	lite := harness.catalog(ctx, t, "rule-lite", 15000)

	attempt := func(key, planVersionID, operation, target string, newSubscription bool) error {
		_, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
			CustomerID: customerID, PlanVersionID: planVersionID, Currency: "RUB",
			Operation: operation, IdempotencyKey: key, SubscriptionID: target, NewSubscription: newSubscription,
		})
		return err
	}
	refusals := []struct {
		name, key, plan, operation, target string
		want                               error
	}{
		{"purchase aimed at the live subscription", "rule-1", pro, "purchase", subscriptionID, commerce.ErrPurchaseOnLiveSubscription},
		{"purchase defaulting onto the live primary", "rule-2", pro, "purchase", "", commerce.ErrPurchaseOnLiveSubscription},
		{"upgrade to a cheaper plan", "rule-3", lite, "upgrade", subscriptionID, commerce.ErrOperationMismatch},
		{"downgrade to a dearer plan", "rule-4", pro, "downgrade", subscriptionID, commerce.ErrOperationMismatch},
		{"extension onto another plan", "rule-5", pro, "extension", subscriptionID, commerce.ErrOperationMismatch},
	}
	for _, refusal := range refusals {
		err := attempt(refusal.key, refusal.plan, refusal.operation, refusal.target, false)
		if !errors.Is(err, commercepg.ErrOperationForbidden) || !errors.Is(err, refusal.want) {
			t.Fatalf("%s: got %v, want operation_forbidden (%v)", refusal.name, err, refusal.want)
		}
	}
	var orders int
	if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM orders WHERE user_id = $1::uuid`, customerID).Scan(&orders); err != nil {
		t.Fatalf("count orders: %v", err)
	}
	if orders != 1 {
		t.Fatalf("a refused operation left %d orders behind, want only the held one", orders-1)
	}

	for _, accepted := range []struct {
		name, key, plan, operation, target string
		newSubscription                    bool
	}{
		{"extension of the held plan", "rule-ok-1", basic, "extension", subscriptionID, false},
		{"upgrade to a dearer plan", "rule-ok-2", pro, "upgrade", subscriptionID, false},
		{"downgrade to a cheaper plan", "rule-ok-3", lite, "downgrade", subscriptionID, false},
		{"purchase of an additional subscription", "rule-ok-4", pro, "purchase", "", true},
	} {
		if err := attempt(accepted.key, accepted.plan, accepted.operation, accepted.target, accepted.newSubscription); err != nil {
			t.Fatalf("%s must be accepted: %v", accepted.name, err)
		}
	}
}
