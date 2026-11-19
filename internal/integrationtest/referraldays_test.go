//go:build integration

package integrationtest

import (
	"context"
	"testing"

	"github.com/omniflow/omniflow/internal/commercepg"
)

// A programme paying in days (Bedolaga's "+7 to the friend, +14 to the
// inviter") extends both subscriptions when the friend's first subscription
// order settles, moves no money, and does so exactly once.
func TestReferralRewardsInDays(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	store := harness.commerceStore(t, testOptions())
	if _, err := harness.pool.Exec(ctx, `INSERT INTO referral_programs
		(singleton, enabled, currency, inviter_reward_minor, invitee_reward_minor, inviter_reward_days, invitee_reward_days)
		VALUES (true, true, 'RUB', 0, 0, 14, 7)`); err != nil {
		t.Fatalf("configure the programme: %v", err)
	}
	planVersionID := harness.catalog(ctx, t, "days-plan", 22900)
	inviter := harness.customer(ctx, t)
	inviterSubscription := harness.seedSubscription(ctx, t, inviter, 1)
	harness.seedSubscriptionEntitlement(ctx, t, inviter, inviterSubscription, planVersionID, "active", "5 days")
	invitee := harness.customer(ctx, t)
	if _, err := harness.pool.Exec(ctx, `INSERT INTO referral_attributions (referred_user_id, referrer_user_id, code)
		VALUES ($1::uuid, $2::uuid, 'DAYS')`, invitee, inviter); err != nil {
		t.Fatal(err)
	}
	order, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: invitee, PlanVersionID: planVersionID, Currency: "RUB", Operation: "purchase", IdempotencyKey: "days-purchase",
	})
	if err != nil {
		t.Fatal(err)
	}
	harness.settle(ctx, t, store, uuidText(order.ID), 22900, "event-days-purchase")
	harness.settle(ctx, t, store, uuidText(order.ID), 22900, "event-days-purchase")

	type reward struct {
		role string
		days int
	}
	rows, err := harness.pool.Query(ctx, `SELECT role, days FROM referral_rewards ORDER BY role`)
	if err != nil {
		t.Fatal(err)
	}
	var got []reward
	for rows.Next() {
		var item reward
		_ = rows.Scan(&item.role, &item.days)
		got = append(got, item)
	}
	rows.Close()
	if len(got) != 2 || got[0] != (reward{"invitee", 7}) || got[1] != (reward{"inviter", 14}) {
		t.Fatalf("rewards = %+v, want invitee 7 and inviter 14 days", got)
	}
	if harness.walletBalance(ctx, t, inviter) != 0 || harness.walletBalance(ctx, t, invitee) != 0 {
		t.Fatal("a day reward moved money")
	}
	var inviterDays float64
	if err := harness.pool.QueryRow(ctx, `SELECT extract(epoch FROM max(ends_at) - now()) / 86400 FROM entitlements WHERE user_id = $1::uuid`, inviter).Scan(&inviterDays); err != nil {
		t.Fatal(err)
	}
	if inviterDays < 18.5 || inviterDays > 19.5 {
		t.Fatalf("inviter's subscription ends in %.1f days, want 5 + 14", inviterDays)
	}
}
