//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/panelpg"
)

// The reward codes Bedolaga issues: wallet credit with days in one code,
// traffic added to the live period, and a loyalty group held for some days.
// Each is refused where it would grant nothing, and only once per customer.
func TestBalanceDaysTrafficAndLoyaltyCodes(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	versionID := harness.catalog(ctx, t, "monthly", 22900)
	subscriber := harness.customer(ctx, t)
	subscriptionID := harness.seedSubscription(ctx, t, subscriber, 1)
	harness.seedSubscriptionEntitlement(ctx, t, subscriber, subscriptionID, versionID, "active", "10 days")
	if _, err := harness.pool.Exec(ctx, `UPDATE entitlements SET traffic_allowance_bytes = 10737418240 WHERE user_id = $1::uuid`, subscriber); err != nil {
		t.Fatalf("limit traffic: %v", err)
	}

	if _, err := harness.pool.Exec(ctx, `WITH p AS (
		INSERT INTO promotions (code, kind, value, currency, credit_minor, per_customer_limit)
		VALUES ('welcomeback', 'balance_days', 5, 'RUB', 15000, 1) RETURNING id)
		INSERT INTO promo_codes (promotion_id, normalized_code) SELECT id, 'WELCOMEBACK' FROM p`); err != nil {
		t.Fatalf("balance+days promotion: %v", err)
	}
	reward, err := store.RedeemRewardPromo(ctx, "welcomeback", subscriber)
	if err != nil || reward.Days != 5 || reward.CreditMinor != 15000 {
		t.Fatalf("balance+days: %+v %v", reward, err)
	}
	if left := time.Until(reward.EndsAt); left < 14*24*time.Hour || left > 16*24*time.Hour {
		t.Fatalf("subscription now ends in %v, want about 15 days", left)
	}
	var balance int64
	if err = harness.pool.QueryRow(ctx, `SELECT COALESCE(sum(amount_minor), 0)::bigint FROM ledger_entries
		WHERE account_type = 'customer_wallet' AND user_id = $1::uuid`, subscriber).Scan(&balance); err != nil || balance != 15000 {
		t.Fatalf("wallet = %d %v", balance, err)
	}
	newcomer := harness.customer(ctx, t)
	if _, err = store.RedeemRewardPromo(ctx, "welcomeback", newcomer); !errors.Is(err, commercepg.ErrPromoNotApplicable) {
		t.Fatalf("balance+days for a customer with no subscription: %v", err)
	}
	if err = harness.pool.QueryRow(ctx, `SELECT COALESCE(sum(amount_minor), 0)::bigint FROM ledger_entries
		WHERE account_type = 'customer_wallet' AND user_id = $1::uuid`, newcomer).Scan(&balance); err != nil || balance != 0 {
		t.Fatalf("a refused balance+days code credited %d (%v)", balance, err)
	}

	harness.rewardPromo(ctx, t, "MOREGB", "traffic", 5, "", 1)
	reward, err = store.RedeemRewardPromo(ctx, "moregb", subscriber)
	if err != nil || reward.TrafficBytes != 5*1024*1024*1024 {
		t.Fatalf("traffic: %+v %v", reward, err)
	}
	var allowance int64
	var operations int
	if err = harness.pool.QueryRow(ctx, `SELECT max(traffic_allowance_bytes) FROM entitlements WHERE user_id = $1::uuid AND status = 'active'`, subscriber).Scan(&allowance); err != nil {
		t.Fatalf("allowance: %v", err)
	}
	if err = harness.pool.QueryRow(ctx, `SELECT count(*) FROM fulfillment_operations WHERE operation = 'set_limits' AND idempotency_key LIKE 'promo:%:traffic'`).Scan(&operations); err != nil {
		t.Fatalf("operations: %v", err)
	}
	if allowance != 15*1024*1024*1024 || operations != 1 {
		t.Fatalf("allowance=%d set_limits=%d", allowance, operations)
	}
	if _, err = store.RedeemRewardPromo(ctx, "moregb", newcomer); !errors.Is(err, commercepg.ErrPromoNotApplicable) {
		t.Fatalf("traffic with no live period: %v", err)
	}

	if _, err = harness.pool.Exec(ctx, `WITH p AS (
		INSERT INTO promotions (code, kind, value, tier_code, per_customer_limit)
		VALUES ('vipcode', 'loyalty_tier', 30, 'vip', 1) RETURNING id)
		INSERT INTO promo_codes (promotion_id, normalized_code) SELECT id, 'VIPCODE' FROM p`); err != nil {
		t.Fatalf("tier promotion: %v", err)
	}
	if reward, err = store.RedeemRewardPromo(ctx, "vipcode", newcomer); err != nil || reward.TierCode != "vip" {
		t.Fatalf("tier: %+v %v", reward, err)
	}
	var source string
	var expires time.Time
	if err = harness.pool.QueryRow(ctx, `SELECT source, expires_at FROM loyalty_memberships WHERE user_id = $1::uuid AND tier_code = 'vip'`, newcomer).Scan(&source, &expires); err != nil {
		t.Fatalf("membership: %v", err)
	}
	if source != "promo_code" || time.Until(expires) < 29*24*time.Hour {
		t.Fatalf("membership %s until %v", source, expires)
	}
}

// A wholesale batch may cap how many of its codes one customer redeems.
func TestCodeBatchPerCustomerLimit(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	service, err := panelpg.New(harness.pool, operationsKey, panelpg.Options{})
	if err != nil {
		t.Fatalf("panel: %v", err)
	}
	versionID := harness.catalog(ctx, t, "wholesale", 10000)
	generated, err := service.CreateCodeBatch(ctx, panelpg.CodeBatch{
		Reference: "partner-limit", PlanVersionID: versionID, Quantity: 3, Currency: "RUB", MaxPerCustomer: 1,
	}, panelpg.Actor{Reason: "test"})
	if err != nil {
		t.Fatalf("batch: %v", err)
	}
	customer := harness.customer(ctx, t)
	if _, err = store.RedeemAccessCode(ctx, generated.Codes[0], customer); err != nil {
		t.Fatalf("first code: %v", err)
	}
	if _, err = store.RedeemAccessCode(ctx, generated.Codes[1], customer); !errors.Is(err, commercepg.ErrCodeNotRedeemable) {
		t.Fatalf("a second code from the same batch: %v", err)
	}
	if _, err = store.RedeemAccessCode(ctx, generated.Codes[1], harness.customer(ctx, t)); err != nil {
		t.Fatalf("the refused code stays usable by somebody else: %v", err)
	}
}
