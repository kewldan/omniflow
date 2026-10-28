//go:build integration

package integrationtest

import (
	"context"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/fulfillment"
)

// TestADeferredDowngradeBoughtWhilePausedResumesFirst proves an at_expiry
// downgrade never eats the days a pause is preserving. Scheduled behind a
// paused entitlement, it used to start at now plus the frozen remainder while
// the subscription stayed disabled, so those days ran out with the customer
// locked out. A paid change resumes a paused subscription, so this one resumes
// it now and the downgrade takes effect when the resumed period ends.
func TestADeferredDowngradeBoughtWhilePausedResumesFirst(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	store := harness.commerceStore(t, testOptions())
	customerID := harness.customer(ctx, t)
	panel := newMemoryRemnawave()
	worker := fulfillment.NewWorker(harness.pool, panel)

	currentOrder, _, _ := harness.paidOrder(ctx, t, store, customerID, "paused-premium", 50000)
	harness.fulfillAll(ctx, t, worker)
	var currentID, subscriptionID string
	var remoteID int64
	// Paused five days ago with eleven days left.
	if err := harness.pool.QueryRow(ctx, `UPDATE entitlements
		SET status = 'paused', paused_at = now() - interval '5 days', ends_at = now() + interval '6 days'
		WHERE order_id = $1::uuid RETURNING id::text, subscription_id::text, remnawave_user_id`, currentOrder).
		Scan(&currentID, &subscriptionID, &remoteID); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if err := panel.DisableUser(ctx, remoteID); err != nil {
		t.Fatalf("disable remote: %v", err)
	}

	basic := harness.catalog(ctx, t, "paused-basic", 25000)
	if _, err := harness.pool.Exec(ctx, `UPDATE plan_versions SET downgrade_policy = 'at_expiry' WHERE id = $1::uuid`, basic); err != nil {
		t.Fatalf("policy: %v", err)
	}
	downgrade, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: basic, Currency: "RUB",
		Operation: "downgrade", IdempotencyKey: "paused-downgrade", SubscriptionID: subscriptionID,
	})
	if err != nil {
		t.Fatalf("downgrade order: %v", err)
	}
	harness.settle(ctx, t, store, uuidText(downgrade.ID), 25000, "paused-downgrade-payment")

	var status string
	var paused bool
	var resumedEnd time.Time
	if err := harness.pool.QueryRow(ctx, `SELECT status, paused_at IS NOT NULL, ends_at FROM entitlements WHERE id = $1::uuid`, currentID).
		Scan(&status, &paused, &resumedEnd); err != nil {
		t.Fatalf("read current: %v", err)
	}
	wantResumed := time.Now().Add(11 * 24 * time.Hour)
	if status != "active" || paused || resumedEnd.Sub(wantResumed).Abs() > time.Minute {
		t.Fatalf("the paused subscription must resume with its eleven days: %s paused=%v ends %s", status, paused, resumedEnd)
	}
	var startsAt, endsAt time.Time
	if err := harness.pool.QueryRow(ctx, `SELECT starts_at, ends_at FROM entitlements WHERE order_id = $1`, downgrade.ID).Scan(&startsAt, &endsAt); err != nil {
		t.Fatalf("read downgrade: %v", err)
	}
	if !startsAt.Equal(resumedEnd) || endsAt.Sub(resumedEnd) != 30*24*time.Hour {
		t.Fatalf("the downgrade must start when the resumed period ends: %s–%s, resumed end %s", startsAt, endsAt, resumedEnd)
	}

	// The resume reaches Remnawave: the user is enabled with the resumed end.
	var resumeOperation string
	if err := harness.pool.QueryRow(ctx, `SELECT id::text FROM fulfillment_operations
		WHERE entitlement_id = $1::uuid AND operation = 'resume'`, currentID).Scan(&resumeOperation); err != nil {
		t.Fatalf("the resume must be queued with the settlement: %v", err)
	}
	if err := runFulfillment(ctx, worker, resumeOperation); err != nil {
		t.Fatalf("resume: %v", err)
	}
	user, err := panel.User(ctx, remoteID)
	if err != nil {
		t.Fatalf("read remote: %v", err)
	}
	if user.Status != "ACTIVE" || !user.ExpireAt.Equal(resumedEnd) {
		t.Fatalf("Remnawave holds %s until %s, want ACTIVE until %s", user.Status, user.ExpireAt, resumedEnd)
	}
}
