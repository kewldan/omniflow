//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"testing"

	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/panelpg"
)

// An operator attaches a friend whose link was lost after they already paid;
// diagnostics list the pair as lost, back-crediting grants the inviter's
// reward once, the pair leaves the list, the inviter tops the leaderboard,
// and a pair that paid out cannot be detached.
func TestReferralAdministrationRecoversALostReferral(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	panel := newOperations(t, harness)
	actor := harness.operator(ctx, t, "refadmin@omniflow.test")
	if _, err := panel.SaveReferralProgram(ctx, panelpg.ReferralProgram{
		Enabled: true, Currency: "RUB", Qualification: "first_paid_order", AttributionValidityDays: 90,
		InviterRewardMinor: 15000, CommissionTriggers: []string{"subscription"},
	}, actor); err != nil {
		t.Fatal(err)
	}
	inviter := harness.customer(ctx, t)
	friend := harness.customer(ctx, t)
	if _, err := harness.pool.Exec(ctx, `INSERT INTO referral_codes (user_id, code) VALUES ($1::uuid, 'ADMINCODE1')`, inviter); err != nil {
		t.Fatal(err)
	}
	versionID := harness.catalog(ctx, t, "admin-ref-plan", 20000)
	order, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: friend, PlanVersionID: versionID, Currency: "RUB", Operation: "purchase", IdempotencyKey: "lost-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	harness.settle(ctx, t, store, uuidText(order.ID), 20000, "lost-event")

	if err := panel.AttachReferrer(ctx, friend, "admincode1", actor); err != nil {
		t.Fatalf("attach: %v", err)
	}
	if err := panel.AttachReferrer(ctx, friend, "ADMINCODE1", actor); !errors.Is(err, panelpg.ErrConflict) {
		t.Fatalf("second attach: %v", err)
	}
	lost, err := panel.LostReferrals(ctx, 10)
	if err != nil || len(lost) != 1 || lost[0].ReferredID != friend {
		t.Fatalf("lost = %+v (%v)", lost, err)
	}
	if err := store.BackCreditReferral(ctx, friend); err != nil {
		t.Fatalf("back-credit: %v", err)
	}
	if err := store.BackCreditReferral(ctx, friend); !errors.Is(err, commercepg.ErrReferralNotLost) {
		t.Fatalf("second back-credit: %v", err)
	}
	if balance := harness.walletBalance(ctx, t, inviter); balance != 15000 {
		t.Fatalf("inviter balance %d, want the reward once", balance)
	}
	if lost, _ := panel.LostReferrals(ctx, 10); len(lost) != 0 {
		t.Fatalf("still lost: %+v", lost)
	}
	top, err := panel.TopReferrers(ctx, 30, 10)
	if err != nil || len(top) != 1 || top[0].CustomerID != inviter || top[0].EarnedMinor != 15000 {
		t.Fatalf("top = %+v (%v)", top, err)
	}
	if err := panel.DetachReferrer(ctx, friend, actor); !errors.Is(err, panelpg.ErrReferralLocked) {
		t.Fatalf("detaching a paid-out pair: %v", err)
	}
	rate := int32(2500)
	if err := panel.SaveReferralInviterTerms(ctx, inviter, panelpg.InviterTerms{CommissionBPS: &rate, Note: "blogger"}, actor); err != nil {
		t.Fatalf("terms: %v", err)
	}
	if terms, err := panel.ReferralInviterTerms(ctx, inviter); err != nil || terms.CommissionBPS == nil || *terms.CommissionBPS != 2500 {
		t.Fatalf("terms = %+v (%v)", terms, err)
	}
}
