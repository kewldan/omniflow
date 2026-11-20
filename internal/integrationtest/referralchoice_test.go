//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/omniflow/omniflow/internal/accountreferral"
	"github.com/omniflow/omniflow/internal/commercepg"
)

// With both days and balance configured and the choice allowed, an inviter
// who picked balance is paid in money while days still win for whoever did not
// pick; the insights report the earnings and the friend without naming them.
func TestReferralRewardKindChoice(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	service, err := accountreferral.New(harness.pool, accountreferral.Options{Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	inviter := harness.customer(ctx, t)
	if err := service.ChooseRewardKind(ctx, inviter, "wallet"); !errors.Is(err, accountreferral.ErrRewardChoiceUnavailable) {
		t.Fatalf("a choice the programme does not offer: %v", err)
	}
	if _, err := harness.pool.Exec(ctx, `INSERT INTO referral_programs (singleton, enabled, currency,
		inviter_reward_minor, inviter_reward_days, invitee_reward_minor, invitee_reward_days, allow_reward_choice)
		VALUES (true, true, 'RUB', 7000, 14, 3000, 7, true)`); err != nil {
		t.Fatal(err)
	}
	if err := service.ChooseRewardKind(ctx, inviter, "wallet"); err != nil {
		t.Fatalf("choose: %v", err)
	}
	friend := harness.customer(ctx, t)
	if _, err := harness.pool.Exec(ctx, `INSERT INTO referral_attributions (referred_user_id, referrer_user_id, code)
		VALUES ($1::uuid, $2::uuid, 'CHOICECODE')`, friend, inviter); err != nil {
		t.Fatal(err)
	}
	versionID := harness.catalog(ctx, t, "choice-plan", 20000)
	order, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: friend, PlanVersionID: versionID, Currency: "RUB", Operation: "purchase", IdempotencyKey: "choice-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	harness.settle(ctx, t, store, uuidText(order.ID), 20000, "choice-event")
	if balance := harness.walletBalance(ctx, t, inviter); balance != 7000 {
		t.Fatalf("inviter balance %d, want the money reward they chose", balance)
	}
	var friendDays int
	if err := harness.pool.QueryRow(ctx, `SELECT COALESCE(days, 0) FROM referral_rewards WHERE role = 'invitee'`).Scan(&friendDays); err != nil || friendDays != 7 {
		t.Fatalf("friend reward days = %d (%v), want days by default", friendDays, err)
	}
	insights, err := service.Insights(ctx, inviter)
	if err != nil || !insights.ChoiceAllowed || insights.Kind != "wallet" || insights.AllMinor != 7000 ||
		len(insights.Invitees) != 1 || !insights.Invitees[0].Paid || insights.Invitees[0].EarnedMinor != 7000 {
		t.Fatalf("insights = %+v (%v)", insights, err)
	}
}
