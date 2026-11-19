//go:build integration

package integrationtest

import (
	"context"
	"testing"

	"github.com/omniflow/omniflow/internal/catalogpg"
)

// Creating a plan and then a second version through the catalogue service is
// what an operator does to set up a shop, and what a migration from Bedolaga
// needs for every tariff. The version-number query once compared a uuid
// column with a text parameter, so both calls failed.
func TestCatalogCreatesPlansAndNumbersTheirVersions(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service := catalogpg.New(harness.pool)
	version := catalogpg.VersionInput{
		BillingPeriod: "month", DurationSeconds: 30 * 24 * 3600,
		UpgradePolicy: "extend", DowngradePolicy: "at_expiry", CancellationPolicy: "at_expiry",
		Prices: []catalogpg.Price{{Currency: "RUB", AmountMinor: 19900}},
	}
	plan, first, err := service.CreatePlan(ctx, catalogpg.PlanInput{
		Code: "vpn-month", Kind: "one_time", Visible: true,
		Localizations: map[string]catalogpg.Localization{"ru": {Name: "Месяц"}, "en": {Name: "Month"}},
		Version:       version,
	})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	if first.Version != 1 {
		t.Fatalf("first version = %d, want 1", first.Version)
	}
	version.Prices = []catalogpg.Price{{Currency: "RUB", AmountMinor: 24900}}
	second, err := service.CreateVersion(ctx, uuidText(plan.ID), version)
	if err != nil {
		t.Fatalf("create second version: %v", err)
	}
	if second.Version != 2 {
		t.Fatalf("second version = %d, want 2", second.Version)
	}
}
