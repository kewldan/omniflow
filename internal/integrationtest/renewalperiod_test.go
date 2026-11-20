//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"testing"

	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/recurringpg"
)

// A customer chooses the period their automatic renewal buys among the
// plan's renewal periods: a period the plan does not offer is refused, the
// chosen one prices and lengthens the cycle's order, and switching renewal
// off forgets the choice.
func TestTheCustomerChoosesTheAutomaticRenewalPeriod(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	fixture := newRenewalFixture(ctx, t, harness, 60000)
	if _, err := harness.pool.Exec(ctx, `INSERT INTO plan_version_periods
		(plan_version_id, days, currency, amount_minor, for_purchase, for_renewal)
		VALUES ($1::uuid, 90, 'RUB', 60000, false, true), ($1::uuid, 180, 'RUB', 100000, true, false)`,
		fixture.planVersionID); err != nil {
		t.Fatalf("offer more periods: %v", err)
	}
	store := recurringpg.New(harness.pool)

	periods, err := store.RenewalPeriods(ctx, fixture.planVersionID, "RUB")
	if err != nil || len(periods) != 2 || !periods[0].Own || periods[0].Days != 30 || periods[1].Days != 90 {
		t.Fatalf("renewal periods = %+v, %v; want the own 30 days and the renewal-only 90", periods, err)
	}
	if err = store.SetRenewalPeriod(ctx, fixture.customerID, fixture.subscriptionID, 180); !errors.Is(err, recurringpg.ErrPeriodUnavailable) {
		t.Fatalf("a purchase-only period as the renewal period: %v", err)
	}
	if err = store.SetRenewalPeriod(ctx, fixture.customerID, fixture.subscriptionID, 90); err != nil {
		t.Fatalf("choose 90 days: %v", err)
	}
	settings, err := store.RenewalSettings(ctx, fixture.customerID, fixture.subscriptionID)
	if err != nil || settings.PeriodDays != 90 {
		t.Fatalf("stored period = %d, %v", settings.PeriodDays, err)
	}

	fixture.worker.RunOnce(ctx)
	attempts := harness.dunning(ctx, t, fixture.customerID)
	if len(attempts) != 1 || attempts[0].outcome != "succeeded" {
		t.Fatalf("dunning = %+v, want one succeeded attempt", attempts)
	}
	order, err := dbgen.New(harness.pool).GetOrder(ctx, mustUUID(t, attempts[0].orderID))
	if err != nil {
		t.Fatalf("read the renewal order: %v", err)
	}
	if order.SubtotalMinor != 60000 || !order.PeriodSeconds.Valid || order.PeriodSeconds.Int64 != 90*86_400 {
		t.Fatalf("renewal order = %d for %v, want 60000 for 90 days", order.SubtotalMinor, order.PeriodSeconds)
	}

	if err = store.SetAutoRenew(ctx, fixture.customerID, recurringpg.AutoRenew{SubscriptionID: fixture.subscriptionID}); err != nil {
		t.Fatalf("switch renewal off: %v", err)
	}
	if settings, err = store.RenewalSettings(ctx, fixture.customerID, fixture.subscriptionID); err != nil || settings.PeriodDays != 0 {
		t.Fatalf("period after switching off = %d, %v; want it forgotten", settings.PeriodDays, err)
	}
	if err = store.SetRenewalPeriod(ctx, fixture.customerID, fixture.subscriptionID, 90); !errors.Is(err, recurringpg.ErrPeriodUnavailable) {
		t.Fatalf("a period while renewal is off: %v", err)
	}
}
