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
