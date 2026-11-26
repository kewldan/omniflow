//go:build integration

package integrationtest

import (
	"context"
	"slices"
	"testing"

	"github.com/omniflow/omniflow/internal/accountcheckout"
)

// A plan's page lists the discounts running against it. A promotion issued as
// personal offers (a win-back code, a game prize) is listed only to a customer
// whose offer of it is still open: it was listed to everybody, so each
// customer's trial win-back read as one more "20% off" on every plan page.
func TestPlanPageListsAPersonalPromotionOnlyToItsHolder(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	planID := harness.planID(ctx, t, harness.catalog(ctx, t, "plan-promotions", 0))
	holder, redeemer, stranger := harness.customer(ctx, t), harness.customer(ctx, t), harness.customer(ctx, t)

	promotion := func(code string) string {
		t.Helper()
		var id string
		if err := harness.pool.QueryRow(ctx, `INSERT INTO promotions
			(code, kind, value, per_customer_limit, applies_to)
			VALUES ($1, 'percent', 2000, 1, 'plans') RETURNING id::text`, code).Scan(&id); err != nil {
			t.Fatalf("create promotion %s: %v", code, err)
		}
		return id
	}
	promotion("everyone-20")
	personal := promotion("winback-holder")
	if _, err := harness.pool.Exec(ctx, `INSERT INTO personal_offers
		(user_id, promotion_id, title_ru, title_en, expires_at)
		VALUES ($1::uuid, $2::uuid, 'Скидка', 'Discount', now() + interval '1 day')`,
		holder, personal); err != nil {
		t.Fatalf("issue offer: %v", err)
	}
	// An offer that was dismissed no longer puts its promotion on the page.
	if _, err := harness.pool.Exec(ctx, `INSERT INTO personal_offers
		(user_id, promotion_id, title_ru, title_en, status, expires_at)
		VALUES ($1::uuid, $2::uuid, 'Скидка', 'Discount', 'dismissed', now() + interval '1 day')`,
		redeemer, personal); err != nil {
		t.Fatalf("issue dismissed offer: %v", err)
	}

	store := accountcheckout.NewStore(harness.pool)
	listed := func(customerID string) []string {
		t.Helper()
		offers, err := store.Promotions(ctx, customerID, planID)
		if err != nil {
			t.Fatalf("promotions: %v", err)
		}
		codes := make([]string, 0, len(offers))
		for _, offer := range offers {
			codes = append(codes, offer.Code)
		}
		return codes
	}
	for customer, want := range map[string][]string{
		holder:   {"everyone-20", "winback-holder"},
		redeemer: {"everyone-20"},
		stranger: {"everyone-20"},
	} {
		if got := listed(customer); !slices.Equal(got, want) {
			t.Fatalf("customer %s sees %v, want %v", customer, got, want)
		}
	}
}
