//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/commercepg"
)

func (harness *harness) rewardPromo(ctx context.Context, t *testing.T, code, kind string, value int64, currency string, perCustomer int) {
	t.Helper()
	var promotionID string
	var currencyArg any
	if currency != "" {
		currencyArg = currency
	}
	if err := harness.pool.QueryRow(ctx,
		`INSERT INTO promotions (code, kind, value, currency, per_customer_limit) VALUES ($1, $2, $3, $4, $5) RETURNING id::text`,
		strings.ToLower(code), kind, value, currencyArg, perCustomer).Scan(&promotionID); err != nil {
		t.Fatalf("create promotion: %v", err)
	}
	if _, err := harness.pool.Exec(ctx, `INSERT INTO promo_codes (promotion_id, normalized_code) VALUES ($1::uuid, $2)`,
		promotionID, strings.ToUpper(code)); err != nil {
		t.Fatalf("create code: %v", err)
	}
}

// Bedolaga-style promo codes reward on their own: COMEBACK adds days to the
// subscription, a trial code grants a trial once, a balance code credits the
// wallet. Each respects its per-customer limit, and a discount code is sent to
// checkout instead.
func TestRewardPromoCodesGrantDaysTrialAndCredit(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())

	versionID := harness.catalog(ctx, t, "monthly", 22900)
	subscriber := harness.customer(ctx, t)
	subscriptionID := harness.seedSubscription(ctx, t, subscriber, 1)
	harness.seedSubscriptionEntitlement(ctx, t, subscriber, subscriptionID, versionID, "active", "10 days")

	harness.rewardPromo(ctx, t, "COMEBACK", "days", 3, "", 1)
	reward, err := store.RedeemRewardPromo(ctx, "comeback", subscriber)
	if err != nil || reward.Kind != "days" || reward.Days != 3 {
		t.Fatalf("days code: %+v %v", reward, err)
	}
	if left := time.Until(reward.EndsAt); left < 12*24*time.Hour || left > 14*24*time.Hour {
		t.Fatalf("subscription now ends in %v, want about 13 days", left)
	}
	if _, err := store.RedeemRewardPromo(ctx, "COMEBACK", subscriber); !errors.Is(err, commercepg.ErrPromoNotApplicable) {
		t.Fatalf("second use of a once-per-customer code: %v", err)
	}
	newcomer := harness.customer(ctx, t)
	if _, err := store.RedeemRewardPromo(ctx, "COMEBACK", newcomer); !errors.Is(err, commercepg.ErrPromoNotApplicable) {
		t.Fatalf("days for a customer who never subscribed: %v", err)
	}

	harness.rewardPromo(ctx, t, "BONUS100", "wallet_credit", 10000, "RUB", 1)
	credit, err := store.RedeemRewardPromo(ctx, "BONUS100", newcomer)
	if err != nil || credit.CreditMinor != 10000 || harness.walletBalance(ctx, t, newcomer) != 10000 {
		t.Fatalf("balance code: %+v %v, wallet %d", credit, err, harness.walletBalance(ctx, t, newcomer))
	}

	harness.promotion(ctx, t, "SALE10", "", 1)
	if _, err := store.RedeemRewardPromo(ctx, "SALE10", newcomer); !errors.Is(err, commercepg.ErrPromoNotReward) {
		t.Fatalf("discount code: %v, want ErrPromoNotReward", err)
	}

	var orders int
	_ = harness.pool.QueryRow(ctx, `SELECT count(*) FROM orders WHERE operation = 'code' AND state = 'paid'`).Scan(&orders)
	var redemptions int
	_ = harness.pool.QueryRow(ctx, `SELECT count(*) FROM promo_redemptions`).Scan(&redemptions)
	if orders != 2 || redemptions != 2 {
		t.Fatalf("orders %d, redemptions %d; want one of each per applied code", orders, redemptions)
	}
}
