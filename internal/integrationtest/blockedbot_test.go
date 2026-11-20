//go:build integration

package integrationtest

import (
	"context"
	"log/slog"
	"testing"

	"github.com/omniflow/omniflow/internal/blockedbot"
	"github.com/omniflow/omniflow/internal/fulfillment"
)

type recordingEnqueuer struct{ operations []string }

func (recorder *recordingEnqueuer) Enqueue(_ context.Context, entitlementID string, input fulfillment.OperationInput) error {
	recorder.operations = append(recorder.operations, input.Operation+":"+entitlementID)
	return nil
}

// With the option on, a customer who blocked the bot past the grace period has
// their live subscription disabled once; when they come back it is enabled
// once; with the option off nothing is disabled.
func TestBlockedCustomersLoseAndRegainTheirSubscription(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	customer := harness.customer(ctx, t)
	versionID := harness.catalog(ctx, t, "blocked-plan", 10000)
	subscription := harness.seedSubscription(ctx, t, customer, 1)
	harness.seedSubscriptionEntitlement(ctx, t, customer, subscription, versionID, "active", "10 days")
	setDeliveryStatus(ctx, t, harness, customer, "blocked")
	if _, err := harness.pool.Exec(ctx, `UPDATE bot_delivery_state SET updated_at = now() - interval '2 days'`); err != nil {
		t.Fatal(err)
	}
	recorder := &recordingEnqueuer{}
	sweeper := blockedbot.New(harness.pool, recorder, slog.New(slog.DiscardHandler))

	if err := sweeper.Run(ctx); err != nil || len(recorder.operations) != 0 {
		t.Fatalf("option off: %v %v", recorder.operations, err)
	}
	if _, err := harness.pool.Exec(ctx, `UPDATE installation_settings SET document = '{"disableBlockedSubscriptions": true}' WHERE section = 'marketing'`); err != nil {
		t.Fatal(err)
	}
	_ = sweeper.Run(ctx)
	_ = sweeper.Run(ctx)
	if len(recorder.operations) != 1 || recorder.operations[0][:8] != "disable:" {
		t.Fatalf("operations = %v, want one disable", recorder.operations)
	}
	setDeliveryStatus(ctx, t, harness, customer, "active")
	_ = sweeper.Run(ctx)
	_ = sweeper.Run(ctx)
	if len(recorder.operations) != 2 || recorder.operations[1][:7] != "enable:" {
		t.Fatalf("operations = %v, want one enable after returning", recorder.operations)
	}
}
