//go:build integration

package integrationtest

import (
	"context"
	"testing"
)

// A wallet-funded renewal the balance will not cover is announced once,
// ahead of its charge, with the real shortfall; one the balance covers is
// not announced at all.
func TestLowBalanceIsAnnouncedBeforeTheRenewal(t *testing.T) {
	ctx := context.Background()
	for _, scenario := range []struct {
		walletLeft int64
		want       int
	}{{0, 1}, {renewalPrice, 0}} {
		harness := newHarness(t)
		fixture := newRenewalFixture(ctx, t, harness, scenario.walletLeft)
		// The charge falls a day from now: inside the warning window, before
		// the lead time opens the first attempt.
		if _, err := harness.pool.Exec(ctx,
			`UPDATE entitlements SET ends_at = now() + interval '4 days' WHERE id = $1::uuid`, fixture.entitlementID); err != nil {
			t.Fatalf("move the renewal: %v", err)
		}
		fixture.worker.RunOnce(ctx)
		fixture.worker.RunOnce(ctx)
		var notices int
		var shortfall int64
		if err := harness.pool.QueryRow(ctx, `SELECT count(*), COALESCE(max((params->>'amountMinor')::bigint), 0)
			FROM customer_event_notices WHERE user_id = $1::uuid AND kind = 'renewal_low_balance'`,
			fixture.customerID).Scan(&notices, &shortfall); err != nil {
			t.Fatalf("read notices: %v", err)
		}
		if notices != scenario.want || (scenario.want == 1 && shortfall != renewalPrice) {
			t.Fatalf("wallet %d: %d notices (shortfall %d), want %d", scenario.walletLeft, notices, shortfall, scenario.want)
		}
		var attempts int
		if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM dunning_attempts WHERE user_id = $1::uuid`, fixture.customerID).Scan(&attempts); err != nil || attempts != 0 {
			t.Fatalf("the warning must come before any charge attempt: %d %v", attempts, err)
		}
	}
}
