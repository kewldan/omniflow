//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"testing"

	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/panelpg"
)

// The server catalogue prices a chosen server, refuses a full one to a new
// customer but not to one already on it, keeps a trial off servers closed to
// trials, and an automatic renewal keeps — and prices — the chosen server.
func TestServerCatalogLimitsAndPricesSales(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	panel := newOperations(t, harness)
	actor := harness.operator(ctx, t, "servers@example.test")
	store := harness.commerceStore(t, testOptions())
	const (
		germany = "9d2c5c34-0f5e-4c43-9d5c-5d8c1c4f3a10"
		finland = "5a1b2c3d-0f5e-4c43-9d5c-5d8c1c4f3a11"
	)
	planID := harness.planID(ctx, t, harness.catalog(ctx, t, "server-plan", 0))
	maximum := int32(2)
	version, err := panel.CreatePlanVersion(ctx, planID, panelpg.PlanVersionInput{
		BillingPeriod: "month", DurationSeconds: 30 * 86_400, SquadSelection: "optional",
		MaxSelectableSquads: &maximum,
		UpgradePolicy:       "extend", DowngradePolicy: "at_expiry", CancellationPolicy: "at_expiry",
		TrialEligibility: "new_customer", Prices: map[string]int64{"RUB": 1000},
		PlanVersionOptions: panelpg.PlanVersionOptions{OfferedSquads: []panelpg.OfferedSquad{
			{SquadID: germany, LabelRU: "Германия", LabelEN: "Germany"},
			{SquadID: finland, LabelRU: "Финляндия", LabelEN: "Finland"},
		}},
	}, actor)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	one := int32(1)
	if _, err = panel.SaveServer(ctx, panelpg.Server{
		SquadID: germany, NameRU: "Германия", NameEN: "Germany", CountryCode: "de", MaxUsers: &one,
		TrialEligible: true, Prices: map[string]int64{"RUB": 200},
	}, actor); err != nil {
		t.Fatalf("catalogue Germany: %v", err)
	}
	if _, err = panel.SaveServer(ctx, panelpg.Server{SquadID: finland, NameRU: "Финляндия", NameEN: "Finland", CountryCode: "FIN"}, actor); !errors.Is(err, panelpg.ErrValidaton) {
		t.Fatalf("a three-letter country: %v", err)
	}
	if _, err = panel.SaveServer(ctx, panelpg.Server{SquadID: finland, NameRU: "Финляндия", NameEN: "Finland", CountryCode: "FI"}, actor); err != nil {
		t.Fatalf("catalogue Finland: %v", err)
	}

	first := harness.customer(ctx, t)
	harness.seedWallet(ctx, t, first, "servers-wallet", 5000)
	order, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: first, PlanVersionID: version.ID, Currency: "RUB", Operation: "purchase",
		IdempotencyKey: "servers-first", SelectedSquadIDs: []string{germany},
	})
	if err != nil {
		t.Fatalf("buy Germany: %v", err)
	}
	if order.SubtotalMinor != 1200 {
		t.Fatalf("plan with Germany = %d, want 1200", order.SubtotalMinor)
	}
	if _, err = store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: first, PlanVersionID: version.ID, Currency: "RUB", Operation: "extension",
		IdempotencyKey: "servers-extend", SelectedSquadIDs: []string{germany}, SubscriptionID: uuidText(order.SubscriptionID),
	}); err != nil {
		t.Fatalf("a customer already on a full server extends: %v", err)
	}
	second := harness.customer(ctx, t)
	if _, err = store.PreviewOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: second, PlanVersionID: version.ID, Currency: "RUB", Operation: "purchase",
		IdempotencyKey: "servers-second", SelectedSquadIDs: []string{germany},
	}); !errors.Is(err, commercepg.ErrServerFull) {
		t.Fatalf("a new customer on a full server: %v", err)
	}
	if quote, quoteErr := store.PreviewOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: second, PlanVersionID: version.ID, Currency: "RUB", Operation: "purchase",
		IdempotencyKey: "servers-finland", SelectedSquadIDs: []string{finland},
	}); quoteErr != nil || quote.TotalMinor() != 1000 {
		t.Fatalf("Finland, which has no price = %d, %v", quote.TotalMinor(), quoteErr)
	}

	squads, err := store.RenewalSquads(ctx, uuidText(order.SubscriptionID), version.ID)
	if err != nil || len(squads) != 1 || squads[0] != germany {
		t.Fatalf("renewal squads = %v, %v", squads, err)
	}
	if quote, quoteErr := store.PreviewOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: first, PlanVersionID: version.ID, Currency: "RUB", Operation: "renewal",
		IdempotencyKey: "servers-renewal", SelectedSquadIDs: squads, SubscriptionID: uuidText(order.SubscriptionID),
	}); quoteErr != nil || quote.TotalMinor() != 1200 {
		t.Fatalf("the renewal = %d, %v; want 1200", quote.TotalMinor(), quoteErr)
	}

	servers, err := panel.Servers(ctx)
	if err != nil || len(servers) != 2 {
		t.Fatalf("servers = %+v, %v", servers, err)
	}
	for _, server := range servers {
		if server.SquadID == germany && (server.LiveCustomers != 1 || server.CountryCode != "DE" || server.Prices["RUB"] != 200) {
			t.Fatalf("Germany = %+v", server)
		}
	}

	// A trial keeps off a server closed to trials.
	if _, err = panel.SaveServer(ctx, panelpg.Server{SquadID: finland, NameRU: "Финляндия", NameEN: "Finland", TrialEligible: false}, actor); err != nil {
		t.Fatalf("close Finland to trials: %v", err)
	}
	if _, err = harness.pool.Exec(ctx, `UPDATE plans SET kind = 'trial' WHERE id = $1::uuid`, planID); err != nil {
		t.Fatalf("make the plan a trial: %v", err)
	}
	if _, err = store.PreviewOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: second, PlanVersionID: version.ID, Currency: "RUB", Operation: "purchase",
		IdempotencyKey: "servers-trial", SelectedSquadIDs: []string{finland},
	}); !errors.Is(err, commercepg.ErrSquadUnavailable) {
		t.Fatalf("a trial on a server closed to trials: %v", err)
	}
	if err = panel.DeleteServer(ctx, finland, actor); err != nil {
		t.Fatalf("remove Finland: %v", err)
	}
	if err = panel.DeleteServer(ctx, finland, actor); !errors.Is(err, panelpg.ErrNotFound) {
		t.Fatalf("remove it twice: %v", err)
	}
}
