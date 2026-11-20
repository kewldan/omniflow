//go:build integration

package integrationtest

import (
	"context"
	"testing"

	"github.com/omniflow/omniflow/internal/panelpg"
)

// A /start campaign link credits the campaign with a new customer once and
// grants its bonus once; a customer who existed before the link, or an
// unknown code, changes nothing; the panel counts what the campaign produced.
func TestAdCampaignAttributesNewCustomersOnce(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	panel, err := panelpg.New(harness.pool, operationsKey, panelpg.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := panel.SaveAdCampaign(ctx, panelpg.AdCampaign{Code: "Stars_Elite", Name: "StarElite broadcast",
		BonusKind: "wallet", BonusValue: 5000, Currency: "RUB", Active: true}, panelpg.Actor{Type: "admin"}); err != nil {
		t.Fatalf("save campaign: %v", err)
	}

	newcomer := harness.customer(ctx, t)
	first, err := store.AttributeCampaign(ctx, newcomer, "stars_elite")
	if err != nil || !first.Attributed || first.BonusKind != "wallet" {
		t.Fatalf("first attribution: %+v %v", first, err)
	}
	second, err := store.AttributeCampaign(ctx, newcomer, "stars_elite")
	if err != nil || second.Attributed {
		t.Fatalf("second tap: %+v %v, want nothing", second, err)
	}
	if balance := harness.walletBalance(ctx, t, newcomer); balance != 5000 {
		t.Fatalf("bonus balance = %d, want 5000 once", balance)
	}

	veteran := harness.customer(ctx, t)
	if _, err := harness.pool.Exec(ctx, `UPDATE users SET created_at = now() - interval '30 days' WHERE id = $1::uuid`, veteran); err != nil {
		t.Fatal(err)
	}
	if result, err := store.AttributeCampaign(ctx, veteran, "stars_elite"); err != nil || result.Attributed {
		t.Fatalf("existing customer: %+v %v, want no attribution", result, err)
	}
	if _, err := store.AttributeCampaign(ctx, newcomer, "no_such_campaign"); err == nil {
		t.Fatal("an unknown code was accepted")
	}

	campaigns, err := panel.AdCampaigns(ctx)
	if err != nil || len(campaigns) != 1 || campaigns[0].Registrations != 1 || campaigns[0].Code != "stars_elite" {
		t.Fatalf("campaigns = %+v (%v)", campaigns, err)
	}
}

// A campaign can grant the trial or days of a chosen plan. The trial is the
// customer's one trial: a customer who already claimed it keeps the
// attribution without a bonus and without a stray order. A plan bonus lands on
// the plan's newest live version, once, however often /start is repeated.
func TestAdCampaignGrantsTrialOrPlanOnce(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	panel, err := panelpg.New(harness.pool, operationsKey, panelpg.Options{})
	if err != nil {
		t.Fatal(err)
	}
	trialPlan := seedCampaignPlan(ctx, t, harness, "campaign-trial", "trial", 0)
	paidPlan := seedCampaignPlan(ctx, t, harness, "campaign-pro", "one_time", 30000)
	actor := panelpg.Actor{Type: "admin"}
	if _, err := panel.SaveAdCampaign(ctx, panelpg.AdCampaign{Code: "trial_post", Name: "Trial post",
		BonusKind: "trial", BonusValue: 5, Active: true}, actor); err != nil {
		t.Fatalf("save trial campaign: %v", err)
	}
	if _, err := panel.SaveAdCampaign(ctx, panelpg.AdCampaign{Code: "plan_post", Name: "Plan post",
		BonusKind: "plan", BonusValue: 14, Active: true}, actor); err == nil {
		t.Fatal("a plan bonus without a plan was saved")
	}
	if _, err := panel.SaveAdCampaign(ctx, panelpg.AdCampaign{Code: "plan_post", Name: "Plan post",
		BonusKind: "plan", BonusValue: 14, BonusPlanID: paidPlan, Active: true}, actor); err != nil {
		t.Fatalf("save plan campaign: %v", err)
	}

	countOrders := func(customer string) int {
		var orders int
		if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM orders WHERE user_id = $1::uuid`, customer).Scan(&orders); err != nil {
			t.Fatal(err)
		}
		return orders
	}

	trialist := harness.customer(ctx, t)
	result, err := store.AttributeCampaign(ctx, trialist, "trial_post")
	if err != nil || !result.Attributed || result.BonusKind != "trial" || result.BonusEndsAt.IsZero() {
		t.Fatalf("trial bonus = %+v (%v)", result, err)
	}
	var claims int
	if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM trial_claims WHERE user_id = $1::uuid`, trialist).Scan(&claims); err != nil || claims != 1 {
		t.Fatalf("trial claims = %d (%v)", claims, err)
	}

	// A customer who already took the trial is attributed without a bonus.
	repeat := harness.customer(ctx, t)
	if _, err := harness.pool.Exec(ctx, `WITH o AS (
			INSERT INTO orders (user_id, state, operation, currency, subtotal_minor, external_minor, idempotency_key, selected_squad_ids)
			VALUES ($1::uuid, 'draft', 'code', 'RUB', 0, 0, 'earlier-trial', '{}') RETURNING id
		) INSERT INTO trial_claims (user_id, plan_id, order_id) SELECT $1::uuid, $2::uuid, id FROM o`, repeat, trialPlan); err != nil {
		t.Fatalf("seed an earlier trial: %v", err)
	}
	// The earlier trial's order is still open: a settled one would make the
	// customer ineligible for any campaign, which is not what is tested here.
	before := countOrders(repeat)
	result, err = store.AttributeCampaign(ctx, repeat, "trial_post")
	if err != nil || !result.Attributed || result.BonusKind != "none" {
		t.Fatalf("second trial = %+v (%v), want attribution without bonus", result, err)
	}
	if after := countOrders(repeat); after != before {
		t.Fatalf("a refused trial left %d orders behind", after-before)
	}

	planner := harness.customer(ctx, t)
	result, err = store.AttributeCampaign(ctx, planner, "plan_post")
	if err != nil || !result.Attributed || result.BonusKind != "plan" {
		t.Fatalf("plan bonus = %+v (%v)", result, err)
	}
	if again, err := store.AttributeCampaign(ctx, planner, "plan_post"); err != nil || again.Attributed {
		t.Fatalf("repeat /start = %+v (%v)", again, err)
	}
	var entitlements int
	if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM entitlements e
		JOIN plan_versions v ON v.id = e.plan_version_id
		WHERE e.user_id = $1::uuid AND v.plan_id = $2::uuid`, planner, paidPlan).Scan(&entitlements); err != nil || entitlements != 1 {
		t.Fatalf("plan entitlements = %d (%v), want 1", entitlements, err)
	}
}

// seedCampaignPlan creates a plan with one priced version and returns the
// plan's ID.
func seedCampaignPlan(ctx context.Context, t *testing.T, harness *harness, code, kind string, price int64) string {
	t.Helper()
	var planID string
	if err := harness.pool.QueryRow(ctx, `WITH plan AS (
			INSERT INTO plans (code, kind, visible) VALUES ($1, $2, true) RETURNING id
		), version AS (
			INSERT INTO plan_versions (plan_id, version, billing_period, duration_seconds)
			SELECT id, 1, 'month', 2592000 FROM plan RETURNING id, plan_id
		), price AS (
			INSERT INTO plan_prices (plan_version_id, currency, amount_minor) SELECT id, 'RUB', $3 FROM version
		)
		SELECT plan_id::text FROM version`, code, kind, price).Scan(&planID); err != nil {
		t.Fatalf("seed plan %s: %v", code, err)
	}
	return planID
}
