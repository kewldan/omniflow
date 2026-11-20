//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/referralplus"
)

// A second-level reward is a share of a first-level money reward, paid once
// and taken back when the first-level reward is reversed; a contest pays its
// places once, and only after it has ended.
func TestSecondLevelRewardsAndContests(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service := referralplus.New(harness.pool)
	actor := harness.operator(ctx, t, "levels@omniflow.test")

	grandparent := harness.customer(ctx, t)
	parent := harness.customer(ctx, t)
	if _, err := harness.pool.Exec(ctx, `INSERT INTO referral_attributions (referred_user_id, referrer_user_id, code, qualified_at)
		VALUES ($1::uuid, $2::uuid, 'GRAND', now())`, parent, grandparent); err != nil {
		t.Fatal(err)
	}
	if err := service.SaveLevelSettings(ctx, referralplus.LevelSettings{Enabled: true, Percent: 90}, actor); !errors.Is(err, referralplus.ErrInvalid) {
		t.Fatalf("a 90%% share: %v", err)
	}
	if err := service.SaveLevelSettings(ctx, referralplus.LevelSettings{Enabled: true, Percent: 10}, actor); err != nil {
		t.Fatal(err)
	}
	child := harness.seedReferralEarnings(ctx, t, parent, 80000)
	for range 2 {
		if err := service.Pass(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if balance := harness.walletBalance(ctx, t, grandparent); balance != 8000 {
		t.Fatalf("second-level balance = %d, want 10%% once", balance)
	}

	var reversal string
	if err := harness.pool.QueryRow(ctx, `INSERT INTO ledger_transactions (type, reference_type, reference_id, idempotency_key)
		VALUES ('correction', 'test', 'rev', 'test-reversal') RETURNING id::text`).Scan(&reversal); err != nil {
		t.Fatal(err)
	}
	if _, err := harness.pool.Exec(ctx, `UPDATE referral_rewards SET reversed_at = now(), reversal_reason = 'abuse',
		reversal_ledger_transaction_id = $2::uuid WHERE referred_user_id = $1::uuid`, child, reversal); err != nil {
		t.Fatal(err)
	}
	if err := service.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	if balance := harness.walletBalance(ctx, t, grandparent); balance != 0 {
		t.Fatalf("after reversal = %d", balance)
	}

	contest, err := service.CreateContest(ctx, referralplus.Contest{
		TitleRU: "Конкурс", TitleEN: "Contest", StartsAt: time.Now().Add(-time.Hour), EndsAt: time.Now().Add(time.Hour),
		Prizes: []int64{50000, 20000},
	}, actor)
	if err != nil {
		t.Fatal(err)
	}
	view, err := service.CustomerView(ctx, parent, contest.ID)
	if err != nil || len(view) != 2 || view[0].CustomerID != "" {
		t.Fatalf("customer view = %+v (%v)", view, err)
	}
	if _, err = service.FinishContest(ctx, contest.ID, actor); !errors.Is(err, referralplus.ErrState) {
		t.Fatalf("finishing a running contest: %v", err)
	}
	if _, err = harness.pool.Exec(ctx, `UPDATE referral_contests SET ends_at = now() - interval '1 second',
		starts_at = now() - interval '2 hours' WHERE id = $1::uuid`, contest.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = harness.pool.Exec(ctx, `UPDATE referral_attributions SET qualified_at = now() - interval '10 minutes'`); err != nil {
		t.Fatal(err)
	}
	winners, err := service.FinishContest(ctx, contest.ID, actor)
	if err != nil || len(winners) != 2 {
		t.Fatalf("winners = %+v (%v)", winners, err)
	}
	if _, err = service.FinishContest(ctx, contest.ID, actor); !errors.Is(err, referralplus.ErrState) {
		t.Fatalf("a second finish: %v", err)
	}
	total := harness.walletBalance(ctx, t, grandparent) + harness.walletBalance(ctx, t, parent)
	if total != 50000+20000+80000 {
		t.Fatalf("prizes paid = %d", total)
	}
}
