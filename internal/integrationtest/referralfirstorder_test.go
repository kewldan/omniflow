//go:build integration

package integrationtest

import (
	"context"
	"testing"

	"github.com/omniflow/omniflow/internal/commercepg"
)

// TestATopUpNeitherQualifiesNorBlocksAReferral pins the "first paid order"
// predicate to subscription orders. A top-up settled first used to count as the
// first order, which both skipped the reward (top-ups never qualify) and then
// blocked it for good when the subscription was bought.
func TestATopUpNeitherQualifiesNorBlocksAReferral(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	store := commercepg.New(harness.pool, nil, testOptions())
	if _, err := harness.pool.Exec(ctx, `INSERT INTO referral_programs
		(singleton, enabled, currency, inviter_reward_minor, invitee_reward_minor, terms_url)
		VALUES (true, true, 'RUB', 20000, 10000, 'https://example.test/referrals')`); err != nil {
		t.Fatalf("configure the referral programme: %v", err)
	}
	inviter := harness.customer(ctx, t)
	invitee := harness.customer(ctx, t)
	if _, err := harness.pool.Exec(ctx, `INSERT INTO referral_attributions
		(referred_user_id, referrer_user_id, code) VALUES ($1::uuid, $2::uuid, 'FIRSTORDER')`,
		invitee, inviter); err != nil {
		t.Fatalf("seed attribution: %v", err)
	}
	planVersionID := harness.catalog(ctx, t, "referral-plan", 25000)

	// The invitee tops up first.
	topUp, err := store.CreateTopUpOrder(ctx, commercepg.TopUpInput{
		CustomerID: invitee, Currency: "RUB", AmountMinor: 5000, IdempotencyKey: "invitee-topup",
	})
	if err != nil {
		t.Fatalf("create top-up: %v", err)
	}
	harness.settle(ctx, t, store, uuidText(topUp.ID), 5000, "event-invitee-topup")
	var rewards int
	if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM referral_rewards`).Scan(&rewards); err != nil {
		t.Fatalf("count rewards: %v", err)
	}
	if rewards != 0 {
		t.Fatalf("a top-up must not qualify a referral, got %d rewards", rewards)
	}

	// Then buys a subscription, partly from the wallet. That is the first paid
	// subscription order and it qualifies the referral.
	order, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: invitee, PlanVersionID: planVersionID, Currency: "RUB",
		Operation: "purchase", IdempotencyKey: "invitee-purchase",
	})
	if err != nil {
		t.Fatalf("create order: %v", err)
	}
	if order.WalletMinor != 5000 || order.ExternalMinor != 20000 {
		t.Fatalf("unexpected split: wallet=%d external=%d", order.WalletMinor, order.ExternalMinor)
	}
	harness.settle(ctx, t, store, uuidText(order.ID), 20000, "event-invitee-purchase")
	if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM referral_rewards`).Scan(&rewards); err != nil {
		t.Fatalf("count rewards: %v", err)
	}
	if rewards != 2 {
		t.Fatalf("the first subscription order must qualify both roles, got %d rewards", rewards)
	}
	if balance := harness.walletBalance(ctx, t, inviter); balance != 20000 {
		t.Fatalf("inviter reward = %d", balance)
	}
	var qualifyingOrder string
	if err := harness.pool.QueryRow(ctx, `SELECT qualifying_order_id::text FROM referral_attributions WHERE referred_user_id = $1::uuid`, invitee).Scan(&qualifyingOrder); err != nil {
		t.Fatalf("read attribution: %v", err)
	}
	if qualifyingOrder != uuidText(order.ID) {
		t.Fatalf("the attribution names %s as qualifying, want the subscription order", qualifyingOrder)
	}
}
