//go:build integration

package integrationtest

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/panelpg"
)

// "A discount at most once in sixty days": a promotion created with a cooldown
// refuses a customer whose paid order carried a discount inside it, whichever
// promotion gave it, and admits them again once the discount is older. An
// unpaid order's discount and a reward that takes nothing off do not count.
func TestDiscountCooldownEligibility(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	panel := newOperations(t, harness)
	actor := harness.operator(ctx, t, "cooldown@omniflow.test")

	created, err := panel.CreatePromotion(ctx, panelpg.NewPromotion{
		Code: "spring", Kind: "percent", Value: 2000, PerCustomer: 1, Active: true,
		Codes: []string{"SPRING"}, DiscountCooldownDays: 60,
	}, actor)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err = panel.CreatePromotion(ctx, panelpg.NewPromotion{
		Code: "toolong", Kind: "percent", Value: 1000, PerCustomer: 1, Active: true,
		Codes: []string{"TOOLONG"}, DiscountCooldownDays: 5000,
	}, actor); !errors.Is(err, panelpg.ErrValidaton) {
		t.Fatalf("a ten-year-plus cooldown was accepted: %v", err)
	}
	var eligibility []byte
	if err = harness.pool.QueryRow(ctx, `SELECT eligibility FROM promotions WHERE id = $1::uuid`, created.ID).Scan(&eligibility); err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err = json.Unmarshal(eligibility, &document); err != nil || document["discountCooldownDays"] != float64(60) {
		t.Fatalf("stored eligibility: %s %v", eligibility, err)
	}
	if err = panel.UpdatePromotion(ctx, created.ID, panelpg.PromotionUpdate{
		Value: 2000, PerCustomer: 1, Active: true, Eligibility: json.RawMessage(`{"discountCooldownDays": "soon"}`),
	}, actor); !errors.Is(err, panelpg.ErrValidaton) {
		t.Fatalf("an unreadable cooldown was stored: %v", err)
	}

	// Another promotion's code, used on an order.
	var otherCode string
	if err = harness.pool.QueryRow(ctx, `WITH p AS (
			INSERT INTO promotions (code, kind, value) VALUES ('flash', 'percent', 1500) RETURNING id
		) INSERT INTO promo_codes (promotion_id, normalized_code) SELECT id, 'FLASH' FROM p RETURNING id::text`).Scan(&otherCode); err != nil {
		t.Fatal(err)
	}
	queries := dbgen.New(harness.pool)
	eligible := func(customer string) bool {
		t.Helper()
		var id pgtype.UUID
		if err := id.Scan(customer); err != nil {
			t.Fatal(err)
		}
		row, err := queries.CheckPromotionCustomerEligibility(ctx, dbgen.CheckPromotionCustomerEligibilityParams{Eligibility: eligibility, UserID: id})
		if err != nil {
			t.Fatalf("eligibility: %v", err)
		}
		return row.Valid && row.Bool
	}
	discounted := func(state, age string, amount int64) string {
		t.Helper()
		customer := harness.customer(ctx, t)
		if _, err := harness.pool.Exec(ctx, `WITH o AS (
				INSERT INTO orders (user_id, state, operation, currency, subtotal_minor, discount_minor, wallet_minor,
					external_minor, paid_minor, idempotency_key)
				VALUES ($1::uuid, $2, 'purchase', 'RUB', 20000, $4::bigint, 0, 20000 - $4::bigint, 20000 - $4::bigint, gen_random_uuid()::text)
				RETURNING id)
			INSERT INTO promo_redemptions (promo_code_id, promotion_id, user_id, order_id, discount_minor, redeemed_at)
			SELECT $5::uuid, c.promotion_id, $1::uuid, o.id, $4::bigint, now() - $3::interval
			FROM o, promo_codes c WHERE c.id = $5::uuid`, customer, state, age, amount, otherCode); err != nil {
			t.Fatalf("seed redemption: %v", err)
		}
		return customer
	}
	if eligible(discounted("fulfilled", "10 days", 3000)) {
		t.Fatal("a paid discount ten days ago blocks a sixty-day cooldown")
	}
	if !eligible(discounted("fulfilled", "90 days", 3000)) {
		t.Fatal("a discount ninety days ago is outside the cooldown")
	}
	if !eligible(discounted("cancelled", "1 day", 3000)) {
		t.Fatal("an abandoned checkout's discount does not count")
	}
	if !eligible(discounted("fulfilled", "1 day", 0)) {
		t.Fatal("a code that took nothing off is not a discount")
	}
	if !eligible(harness.customer(ctx, t)) {
		t.Fatal("a customer with no discount at all is eligible")
	}
}
