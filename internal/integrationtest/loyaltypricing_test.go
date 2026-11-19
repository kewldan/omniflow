//go:build integration

package integrationtest

import (
	"context"
	"testing"

	"github.com/omniflow/omniflow/internal/commercepg"
)

// loyaltyProgram publishes an enabled spend programme with a free bronze floor
// and a gold tier worth goldBPS, and returns the gold tier's identifier.
func (harness *harness) loyaltyProgram(ctx context.Context, t *testing.T, version int, goldBPS int) string {
	t.Helper()
	var programID, goldID string
	if _, err := harness.pool.Exec(ctx, `UPDATE loyalty_programs SET enabled = false`); err != nil {
		t.Fatal(err)
	}
	if err := harness.pool.QueryRow(ctx, `INSERT INTO loyalty_programs (version, enabled, metric, published_at)
		VALUES ($1, true, 'spend', now()) RETURNING id::text`, version).Scan(&programID); err != nil {
		t.Fatalf("create programme: %v", err)
	}
	if _, err := harness.pool.Exec(ctx, `INSERT INTO loyalty_tiers (program_id, code, name_en, name_ru, threshold, discount_bps)
		VALUES ($1::uuid, 'bronze', 'Bronze', 'Бронза', 0, 0)`, programID); err != nil {
		t.Fatal(err)
	}
	if err := harness.pool.QueryRow(ctx, `INSERT INTO loyalty_tiers (program_id, code, name_en, name_ru, threshold, discount_bps)
		VALUES ($1::uuid, 'gold', 'Gold', 'Золото', 100000, $2) RETURNING id::text`, programID, goldBPS).Scan(&goldID); err != nil {
		t.Fatal(err)
	}
	return goldID
}

func (harness *harness) standIn(ctx context.Context, t *testing.T, customerID, tierID string) {
	t.Helper()
	if _, err := harness.pool.Exec(ctx, `INSERT INTO loyalty_standings (user_id, program_id, tier_id, evaluated_metric)
		SELECT $1::uuid, program_id, id, 100000 FROM loyalty_tiers WHERE id = $2::uuid
		ON CONFLICT (user_id) DO UPDATE SET program_id = EXCLUDED.program_id, tier_id = EXCLUDED.tier_id`,
		customerID, tierID); err != nil {
		t.Fatalf("place customer: %v", err)
	}
}

// A customer's loyalty tier discounts the plan they buy, a promo code discounts
// what is left after it, and the order records which part came from the tier.
// A customer with no tier pays the plain price, and a programme that is turned
// off stops discounting.
func TestLoyaltyTierDiscountsTheCheckoutPrice(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	planVersionID := harness.catalog(ctx, t, "loyal-plan", 10000)
	harness.promotion(ctx, t, "LOYALTEN", harness.planIDOf(ctx, t, planVersionID), 1)
	goldID := harness.loyaltyProgram(ctx, t, 1, 1000)

	loyal := harness.customer(ctx, t)
	harness.standIn(ctx, t, loyal, goldID)
	input := commercepg.CreateOrderInput{
		CustomerID: loyal, PlanVersionID: planVersionID, Currency: "RUB",
		Operation: "purchase", IdempotencyKey: "loyal-1",
	}
	quote, err := store.PreviewOrder(ctx, input)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if quote.DiscountMinor != 1000 || quote.LoyaltyDiscountMinor != 1000 || quote.LoyaltyTier != "gold" || quote.ExternalMinor != 9000 {
		t.Fatalf("quote = %+v, want 10%% off from gold", quote)
	}

	input.PromoCode = "LOYALTEN"
	quote, err = store.PreviewOrder(ctx, input)
	if err != nil {
		t.Fatalf("preview with code: %v", err)
	}
	// 10 000 − 1 000 (gold) = 9 000; the code takes 10 % of that.
	if quote.DiscountMinor != 1900 || quote.PromoDiscountMinor != 900 || quote.ExternalMinor != 8100 {
		t.Fatalf("stacked quote = %+v, want 1000 + 900 off", quote)
	}
	order, err := store.CreateOrder(ctx, input)
	if err != nil {
		t.Fatalf("create order: %v", err)
	}
	if order.DiscountMinor != 1900 || order.LoyaltyDiscountMinor != 1000 || order.LoyaltyTierCode.String != "gold" || order.ExternalMinor != 8100 {
		t.Fatalf("order = %+v, want the tier's part recorded", order)
	}
	var redeemed int64
	if err := harness.pool.QueryRow(ctx, `SELECT discount_minor FROM promo_redemptions WHERE order_id = $1`, order.ID).Scan(&redeemed); err != nil || redeemed != 900 {
		t.Fatalf("redemption discount = %d (%v), want only the code's 900", redeemed, err)
	}
	// A replayed confirmation is the same order, not a second discount.
	again, err := store.CreateOrder(ctx, input)
	if err != nil || again.ID != order.ID {
		t.Fatalf("replay created another order: %v", err)
	}

	stranger := harness.customer(ctx, t)
	plain, err := store.PreviewOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: stranger, PlanVersionID: planVersionID, Currency: "RUB", Operation: "purchase", IdempotencyKey: "plain-1",
	})
	if err != nil || plain.DiscountMinor != 0 || plain.ExternalMinor != 10000 {
		t.Fatalf("customer without a tier: %+v (%v), want the plain price", plain, err)
	}

	// The next programme version keeps the gold code: the customer keeps the
	// discount until they are re-evaluated, at the new version's rate.
	harness.loyaltyProgram(ctx, t, 2, 1500)
	input.PromoCode, input.IdempotencyKey = "", "loyal-2"
	quote, err = store.PreviewOrder(ctx, input)
	if err != nil || quote.LoyaltyDiscountMinor != 1500 {
		t.Fatalf("after republishing: %+v (%v), want 15%% from the new gold", quote, err)
	}

	if _, err := harness.pool.Exec(ctx, `UPDATE loyalty_programs SET enabled = false`); err != nil {
		t.Fatal(err)
	}
	quote, err = store.PreviewOrder(ctx, input)
	if err != nil || quote.DiscountMinor != 0 {
		t.Fatalf("with loyalty off: %+v (%v), want no discount", quote, err)
	}
}
