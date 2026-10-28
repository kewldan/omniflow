//go:build integration

package integrationtest

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/panelpg"
)

// TestACodeRedeemedDuringARenewalLosesNoPeriod races a code redemption against
// the settlement of an extension for the same subscription. Both build on the
// customer's current end, so whichever commits second must build on the
// first one's result: the customer ends up with both periods. At Read
// Committed the redemption read the entitlement the settlement was about to
// supersede, and one period was silently lost.
func TestACodeRedeemedDuringARenewalLosesNoPeriod(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service := newOperations(t, harness)
	store := harness.commerceStore(t, testOptions())
	actor := harness.operator(ctx, t, "codes-race@example.test")
	planVersion := seedPlanVersion(ctx, t, harness)
	const rounds = 6
	const period = 30 * 24 * time.Hour
	generated, err := service.CreateCodeBatch(ctx, panelpg.CodeBatch{
		Reference: "reseller-race", PlanVersionID: planVersion, Quantity: rounds,
		UnitPriceMinor: 400, Currency: "USD",
	}, actor)
	if err != nil {
		t.Fatalf("create batch: %v", err)
	}

	for round := range rounds {
		customerID := harness.customer(ctx, t)
		// A live subscription to extend.
		redeemed, err := store.RedeemAccessCode(ctx, generated.Codes[round], customerID)
		if err != nil {
			t.Fatalf("round %d: first redemption: %v", round, err)
		}
		base := redeemed.EndsAt
		// Two more periods arrive at once: an extension order settling and a
		// second code being redeemed.
		extension, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
			CustomerID: customerID, PlanVersionID: planVersion, Currency: "USD",
			Operation: "extension", IdempotencyKey: fmt.Sprintf("race-extension-%d", round),
			SubscriptionID: redeemed.SubscriptionID,
		})
		if err != nil {
			t.Fatalf("round %d: extension order: %v", round, err)
		}
		var intentID string
		if err := harness.pool.QueryRow(ctx, `INSERT INTO payment_intents
			(order_id, provider, status, amount_minor, currency, provider_reference, idempotency_key)
			VALUES ($1, 'manual', 'processing', $2, 'USD', $3, $3) RETURNING id::text`,
			extension.ID, extension.ExternalMinor, fmt.Sprintf("race-intent-%d", round)).Scan(&intentID); err != nil {
			t.Fatalf("round %d: intent: %v", round, err)
		}
		second, err := service.CreateCodeBatch(ctx, panelpg.CodeBatch{
			Reference: fmt.Sprintf("reseller-race-%d", round), PlanVersionID: planVersion, Quantity: 1,
			UnitPriceMinor: 400, Currency: "USD",
		}, actor)
		if err != nil {
			t.Fatalf("round %d: second batch: %v", round, err)
		}

		var wait sync.WaitGroup
		var settleErr, redeemErr error
		start := make(chan struct{})
		wait.Add(2)
		go func() {
			defer wait.Done()
			<-start
			// A webhook that fails is delivered again; so does this.
			for range 5 {
				if _, _, settleErr = store.RecordProviderPayment(ctx, intentID, fmt.Sprintf("race-event-%d", round),
					commerce.Money{Amount: extension.ExternalMinor, Currency: "USD"}, false); settleErr == nil {
					return
				}
			}
		}()
		go func() {
			defer wait.Done()
			<-start
			_, redeemErr = store.RedeemAccessCode(ctx, second.Codes[0], customerID)
		}()
		close(start)
		wait.Wait()
		if settleErr != nil || redeemErr != nil {
			t.Fatalf("round %d: settle %v, redeem %v", round, settleErr, redeemErr)
		}
		var latest time.Time
		if err := harness.pool.QueryRow(ctx, `SELECT max(ends_at) FROM entitlements
			WHERE user_id = $1::uuid AND status <> 'superseded'`, customerID).Scan(&latest); err != nil {
			t.Fatalf("round %d: read end: %v", round, err)
		}
		if want := base.Add(2 * period); latest.Before(want.Add(-time.Minute)) {
			t.Fatalf("round %d: a period was lost: the subscription ends %s, want %s", round, latest, want)
		}
	}
}
