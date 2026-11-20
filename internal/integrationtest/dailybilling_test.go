//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/panelpg"
	"github.com/omniflow/omniflow/internal/renewal"
)

// A daily plan buys each next day from the wallet: once per day however many
// workers run the pass, with the money on the ledger; a day the wallet does
// not cover is not ordered, suspends access, and is announced once; a top-up
// resumes it; and the customer's pause stops the charges until a resume.
func TestDailyPlanChargesOncePerDayAndPauses(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	panel := newOperations(t, harness)
	actor := harness.operator(ctx, t, "daily@example.test")
	store := harness.commerceStore(t, testOptions())
	planID := harness.planID(ctx, t, harness.catalog(ctx, t, "daily-plan", 0))
	base := panelpg.PlanVersionInput{
		BillingPeriod: "day", DurationSeconds: 86_400, SquadSelection: "automatic",
		UpgradePolicy: "extend", DowngradePolicy: "at_expiry", CancellationPolicy: "at_expiry",
		TrialEligibility: "new_customer", Prices: map[string]int64{"RUB": 100},
		PlanVersionOptions: panelpg.PlanVersionOptions{DailyBilling: true},
	}
	monthly := base
	monthly.BillingPeriod, monthly.DurationSeconds = "month", 30*86_400
	if _, err := panel.CreatePlanVersion(ctx, planID, monthly, actor); !errors.Is(err, panelpg.ErrValidaton) {
		t.Fatalf("daily billing on a month: %v", err)
	}
	version, err := panel.CreatePlanVersion(ctx, planID, base, actor)
	if err != nil || !version.DailyBilling {
		t.Fatalf("publish a daily version: %+v, %v", version, err)
	}
	customerID := harness.customer(ctx, t)
	harness.seedWallet(ctx, t, customerID, "daily-wallet", 250)
	first, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: version.ID, Currency: "RUB", Operation: "purchase",
		IdempotencyKey: "daily-first",
	})
	if err != nil {
		t.Fatalf("buy the first day: %v", err)
	}
	subscriptionID := uuidText(first.SubscriptionID)
	worker := renewal.New(harness.pool, store, nil, nil, slog.New(slog.DiscardHandler), renewal.Config{})
	// Every day the subscription holds ends at `at` from now, as if the
	// clock had moved on to that point.
	endLatest := func(at string) {
		t.Helper()
		if _, execErr := harness.pool.Exec(ctx, `UPDATE entitlements SET ends_at = now() + $2::interval,
				starts_at = LEAST(starts_at, now() + $2::interval - interval '1 hour')
			WHERE subscription_id = $1::uuid AND status <> 'superseded'`,
			subscriptionID, at); execErr != nil {
			t.Fatalf("move the day's end: %v", execErr)
		}
	}
	count := func(query string, args ...any) int {
		t.Helper()
		var value int
		if scanErr := harness.pool.QueryRow(ctx, query, args...).Scan(&value); scanErr != nil {
			t.Fatalf("count: %v", scanErr)
		}
		return value
	}
	dailyOrders := func() int {
		return count(`SELECT count(*)::int FROM orders WHERE user_id = $1::uuid AND idempotency_key LIKE 'daily:%'`, customerID)
	}
	notices := func(kind string) int {
		return count(`SELECT count(*)::int FROM customer_event_notices WHERE user_id = $1::uuid AND kind = $2`, customerID, kind)
	}

	// The paid day ends in five minutes: several replicas buy the next one once.
	endLatest("5 minutes")
	var group sync.WaitGroup
	for range 4 {
		group.Add(1)
		go func() {
			defer group.Done()
			worker.RunDaily(ctx)
		}()
	}
	group.Wait()
	worker.RunDaily(ctx)
	if got := dailyOrders(); got != 1 {
		t.Fatalf("daily orders after the first day = %d, want 1", got)
	}
	if got := count(`SELECT count(*)::int FROM ledger_transactions t JOIN orders o ON t.reference_id = o.id::text
		WHERE o.user_id = $1::uuid AND o.idempotency_key LIKE 'daily:%' AND t.type = 'payment'`, customerID); got != 1 {
		t.Fatalf("ledger payments for the day = %d, want 1", got)
	}
	// 250 - 100 - 100 leaves 50, which will not cover tomorrow.
	if got := notices("daily_low_balance"); got != 1 {
		t.Fatalf("low-balance notices = %d, want 1", got)
	}

	// A day later the bought day has ended and the wallet holds 50: nothing
	// is ordered, and the suspension is announced once. The test cannot move
	// the database's clock, so the day already bought is dated yesterday,
	// which is when a real one would have started.
	if _, err = harness.pool.Exec(ctx, `UPDATE orders SET idempotency_key = 'daily:' || $2 || ':' || to_char(now() - interval '1 day', 'YYYY-MM-DD')
		WHERE user_id = $1::uuid AND idempotency_key LIKE 'daily:%'`, customerID, subscriptionID); err != nil {
		t.Fatalf("date the bought day: %v", err)
	}
	endLatest("-1 minute")
	worker.RunDaily(ctx)
	worker.RunDaily(ctx)
	if got := dailyOrders(); got != 1 {
		t.Fatalf("daily orders with a short wallet = %d, want 1", got)
	}
	if got := notices("daily_suspended"); got != 1 {
		t.Fatalf("suspension notices = %d, want 1", got)
	}
	if status, statusErr := store.DailyBillingStatus(ctx, customerID, subscriptionID); statusErr != nil || !status.Suspended || status.PriceMinor != 100 {
		t.Fatalf("status while suspended = %+v, %v", status, statusErr)
	}

	// A top-up resumes it on the next pass.
	harness.seedWallet(ctx, t, customerID, "daily-topup", 100)
	worker.RunDaily(ctx)
	if got := dailyOrders(); got != 2 {
		t.Fatalf("daily orders after the top-up = %d, want 2", got)
	}

	// The customer's pause stops the charges until a resume. Fulfillment,
	// which this test does not run, is what activates a bought day.
	if _, err = harness.pool.Exec(ctx, `UPDATE entitlements SET status = 'active'
		WHERE subscription_id = $1::uuid AND status = 'pending'`, subscriptionID); err != nil {
		t.Fatalf("activate: %v", err)
	}
	if err = store.SetDailyPause(ctx, customerID, subscriptionID, true, "pause-1"); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if err = store.SetDailyPause(ctx, customerID, subscriptionID, true, "pause-2"); err != nil {
		t.Fatalf("a second pause is the state already reached: %v", err)
	}
	if got := count(`SELECT count(*)::int FROM fulfillment_operations WHERE idempotency_key LIKE 'daily-pause:%'`); got != 1 {
		t.Fatalf("pause operations = %d, want 1", got)
	}
	harness.seedWallet(ctx, t, customerID, "daily-more", 1000)
	worker.RunDaily(ctx)
	if got := dailyOrders(); got != 2 {
		t.Fatalf("a paused subscription was charged: %d orders", got)
	}
	if err = store.SetDailyPause(ctx, customerID, subscriptionID, false, "resume-1"); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if status, statusErr := store.DailyBillingStatus(ctx, customerID, subscriptionID); statusErr != nil || status.Paused ||
		status.EndsAt.Before(time.Now().Add(time.Hour)) {
		t.Fatalf("status after the resume = %+v, %v", status, statusErr)
	}

	// Another plan's subscription has no customer pause.
	other := harness.catalog(ctx, t, "monthly-plan", 0)
	otherCustomer := harness.customer(ctx, t)
	order, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: otherCustomer, PlanVersionID: other, Currency: "RUB", Operation: "purchase",
		IdempotencyKey: "monthly-first",
	})
	if err != nil {
		t.Fatalf("buy a monthly plan: %v", err)
	}
	if err = store.SetDailyPause(ctx, otherCustomer, uuidText(order.SubscriptionID), true, "monthly-pause"); !errors.Is(err, commercepg.ErrNotDaily) {
		t.Fatalf("a monthly plan paused by its customer: %v", err)
	}
}
