//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"testing"

	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/panelpg"
)

// Codes created in the panel can add traffic, days plus balance, or a loyalty
// group; each grants once per customer, refuses what it cannot apply, and a
// code that already redeems another promotion is a conflict.
func TestPanelPromotionsGrantTrafficBundleAndGroup(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	panel := newOperations(t, harness)
	actor := harness.operator(ctx, t, "promokinds@omniflow.test")

	versionID := harness.catalog(ctx, t, "kinds-plan", 20000)
	customer := harness.customer(ctx, t)
	subscription := harness.seedSubscription(ctx, t, customer, 1)
	harness.seedSubscriptionEntitlement(ctx, t, customer, subscription, versionID, "active", "10 days")
	harness.loyaltyProgram(ctx, t, 1, 1000)
	// A plan with a traffic allowance; unlimited traffic has nothing to add to.
	if _, err := harness.pool.Exec(ctx, `UPDATE entitlements SET traffic_allowance_bytes = 107374182400 WHERE user_id = $1::uuid`, customer); err != nil {
		t.Fatal(err)
	}

	create := func(promotion panelpg.NewPromotion) error {
		promotion.PerCustomer, promotion.Active = 1, true
		_, err := panel.CreatePromotion(ctx, promotion, actor)
		return err
	}
	if err := create(panelpg.NewPromotion{Code: "traffic10", Kind: "traffic", Value: 10, Codes: []string{"GB10"}}); err != nil {
		t.Fatalf("traffic promotion: %v", err)
	}
	if err := create(panelpg.NewPromotion{Code: "bundle", Kind: "balance_days", Value: 5, Currency: "RUB", CreditMinor: 5000, Codes: []string{"BUNDLE"}}); err != nil {
		t.Fatalf("bundle promotion: %v", err)
	}
	if err := create(panelpg.NewPromotion{Code: "goldweek", Kind: "loyalty_tier", Value: 7, TierCode: "gold", Codes: []string{"GOLDWEEK"}}); err != nil {
		t.Fatalf("group promotion: %v", err)
	}
	if err := create(panelpg.NewPromotion{Code: "nocredit", Kind: "balance_days", Value: 5, Currency: "RUB", Codes: []string{"NOCREDIT"}}); !errors.Is(err, panelpg.ErrValidaton) {
		t.Fatalf("a bundle without credit was created: %v", err)
	}
	if err := create(panelpg.NewPromotion{Code: "copycat", Kind: "days", Value: 1, Codes: []string{"GB10"}}); !errors.Is(err, panelpg.ErrConflict) {
		t.Fatalf("a code of another promotion was reused: %v", err)
	}

	var before int64
	_ = harness.pool.QueryRow(ctx, `SELECT traffic_allowance_bytes FROM entitlements WHERE user_id = $1::uuid`, customer).Scan(&before)
	reward, err := store.RedeemRewardPromo(ctx, "gb10", customer)
	if err != nil || reward.TrafficBytes != 10<<30 {
		t.Fatalf("traffic code: %+v %v", reward, err)
	}
	var after int64
	_ = harness.pool.QueryRow(ctx, `SELECT traffic_allowance_bytes FROM entitlements WHERE user_id = $1::uuid`, customer).Scan(&after)
	if after-before != 10<<30 {
		t.Fatalf("traffic grew by %d, want 10 GiB", after-before)
	}
	var jobs int
	_ = harness.pool.QueryRow(ctx, `SELECT count(*) FROM fulfillment_operations WHERE operation = 'set_limits'`).Scan(&jobs)
	if jobs != 1 {
		t.Fatalf("set_limits operations = %d, want 1", jobs)
	}
	if _, err := store.RedeemRewardPromo(ctx, "GB10", customer); !errors.Is(err, commercepg.ErrPromoNotApplicable) {
		t.Fatalf("second use: %v", err)
	}

	bundle, err := store.RedeemRewardPromo(ctx, "BUNDLE", customer)
	if err != nil || bundle.Days != 5 || bundle.CreditMinor != 5000 || harness.walletBalance(ctx, t, customer) != 5000 {
		t.Fatalf("bundle: %+v %v, wallet %d", bundle, err, harness.walletBalance(ctx, t, customer))
	}

	group, err := store.RedeemRewardPromo(ctx, "GOLDWEEK", customer)
	if err != nil || group.TierCode != "gold" {
		t.Fatalf("group code: %+v %v", group, err)
	}
	quote, err := store.PreviewOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customer, PlanVersionID: versionID, Currency: "RUB", Operation: "purchase", IdempotencyKey: "kinds-1",
	})
	if err != nil || quote.LoyaltyTier != "gold" || quote.LoyaltyDiscountMinor != 2000 {
		t.Fatalf("quote after the group code: %+v %v", quote, err)
	}

	// A customer with no subscription has no traffic to add to.
	stranger := harness.customer(ctx, t)
	if err := create(panelpg.NewPromotion{Code: "traffic5", Kind: "traffic", Value: 5, Codes: []string{"GB5"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RedeemRewardPromo(ctx, "GB5", stranger); !errors.Is(err, commercepg.ErrPromoNotApplicable) {
		t.Fatalf("traffic without a subscription: %v", err)
	}
}
