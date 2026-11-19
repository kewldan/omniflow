//go:build integration

package integrationtest

import (
	"context"
	"log/slog"
	"sync"
	"testing"

	"github.com/omniflow/omniflow/internal/paymentservice"
	"github.com/omniflow/omniflow/internal/renewal"
)

// TestConcurrentRenewalPassesChargeOnce runs the renewal pass from several
// workers at the same instant, as several worker replicas do when their
// minute ticks line up. The sequential test proves a repeated pass is a no-op;
// this one proves the same of simultaneous passes, which is where a missing
// lock or uniqueness constraint would show up as a second charge.
func TestConcurrentRenewalPassesChargeOnce(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	// Enough in the wallet for three renewals, so a double charge would
	// succeed rather than be stopped by an empty balance.
	fixture := newRenewalFixture(ctx, t, harness, 3*renewalPrice)

	const replicas = 6
	workers := make([]*renewal.Worker, replicas)
	for index := range workers {
		workers[index] = renewal.New(
			harness.pool, fixture.core.Store, fixture.core.Payments, paymentservice.Index(fixture.core.Providers),
			slog.New(slog.DiscardHandler), renewal.Config{},
		)
	}

	var waitGroup sync.WaitGroup
	start := make(chan struct{})
	for _, worker := range workers {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			<-start
			worker.RunOnce(ctx)
		}()
	}
	close(start)
	waitGroup.Wait()
	// A pass that lost a race may have left its attempt for the next tick;
	// one more pass settles anything outstanding without adding a charge.
	fixture.worker.RunOnce(ctx)

	var renewals int
	if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM orders
		WHERE user_id = $1::uuid AND idempotency_key LIKE 'renewal:%'`, fixture.customerID).Scan(&renewals); err != nil {
		t.Fatalf("count renewal orders: %v", err)
	}
	if renewals != 1 {
		t.Fatalf("simultaneous passes opened %d renewal orders, want 1", renewals)
	}
	attempts := harness.dunning(ctx, t, fixture.customerID)
	succeeded := 0
	for _, attempt := range attempts {
		if attempt.outcome == "succeeded" {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("dunning = %+v, want exactly one succeeded attempt", attempts)
	}
	if balance := harness.walletBalance(ctx, t, fixture.customerID); balance != 2*renewalPrice {
		t.Fatalf("wallet after simultaneous passes = %d, want %d (charged exactly once)", balance, 2*renewalPrice)
	}
	// One debit paid the first period in the fixture; exactly one more may
	// have paid the renewal.
	var debits int
	if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM ledger_entries
		WHERE account_type = 'customer_wallet' AND user_id = $1::uuid AND amount_minor < 0`,
		fixture.customerID).Scan(&debits); err != nil {
		t.Fatalf("count wallet debits: %v", err)
	}
	if debits != 2 {
		t.Fatalf("the wallet was debited %d times, want 2 (first period + one renewal)", debits)
	}
	for _, attempt := range attempts {
		if attempt.orderID == "" {
			continue
		}
		if jobs := harness.queuedFulfillmentFor(ctx, t, attempt.orderID); jobs != 1 {
			t.Fatalf("the renewal order queued %d fulfillment jobs, want 1", jobs)
		}
	}
	if state, code := harness.autoRenewState(ctx, t, fixture.customerID); state != "scheduled" || code != "" {
		t.Fatalf("auto-renew state = %s (%s), want scheduled for the next period", state, code)
	}
}
