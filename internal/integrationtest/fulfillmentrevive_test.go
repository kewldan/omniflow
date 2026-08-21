//go:build integration

package integrationtest

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/fulfillment"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// recordingInserter stands in for the River client so the test can see which
// operations the scheduler queued without a River schema in the database.
type recordingInserter struct {
	operations []string
}

func (inserter *recordingInserter) InsertTx(_ context.Context, _ pgx.Tx, args river.JobArgs, _ *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	inserter.operations = append(inserter.operations, args.(fulfillment.JobArgs).OperationID)
	return &rivertype.JobInsertResult{}, nil
}

// TestAnOrphanedFulfillmentOperationIsRevived proves the safety net behind
// settlements that ran without a job inserter: once the operation has waited
// longer than the stale window, the scheduler inserts the job it never had.
func TestAnOrphanedFulfillmentOperationIsRevived(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	// A nil enqueue is exactly the configuration that used to orphan operations.
	store := commercepg.New(harness.pool, nil, testOptions())
	customerID := harness.customer(ctx, t)
	planVersionID := harness.catalog(ctx, t, "orphan-plan", 0)

	order, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: planVersionID, Currency: "RUB",
		Operation: "purchase", IdempotencyKey: "orphan-order",
	})
	if err != nil {
		t.Fatalf("create order: %v", err)
	}
	if order.State != "paid" {
		t.Fatalf("a zero-priced order settles immediately, got %q", order.State)
	}
	var operationID string
	if err := harness.pool.QueryRow(ctx,
		`SELECT id::text FROM fulfillment_operations WHERE status = 'pending'`).Scan(&operationID); err != nil {
		t.Fatalf("read operation: %v", err)
	}

	inserter := &recordingInserter{}
	scheduler := fulfillment.NewScheduler(harness.pool, inserter)

	// Fresh operations are left alone: their job was inserted with them and the
	// queue simply has not reached it yet.
	if err := scheduler.Revive(ctx); err != nil {
		t.Fatalf("revive: %v", err)
	}
	if len(inserter.operations) != 0 {
		t.Fatalf("a fresh operation was re-queued: %v", inserter.operations)
	}

	if _, err := harness.pool.Exec(ctx, `UPDATE fulfillment_operations
		SET created_at = now() - interval '11 minutes', next_attempt_at = now() - interval '11 minutes'`); err != nil {
		t.Fatalf("age the operation: %v", err)
	}
	if err := scheduler.Revive(ctx); err != nil {
		t.Fatalf("revive: %v", err)
	}
	if len(inserter.operations) != 1 || inserter.operations[0] != operationID {
		t.Fatalf("expected the orphaned operation %s to be queued, got %v", operationID, inserter.operations)
	}

	// A finished operation is never revived, however old it is.
	if _, err := harness.pool.Exec(ctx, `UPDATE fulfillment_operations SET status = 'succeeded', completed_at = now()`); err != nil {
		t.Fatalf("complete the operation: %v", err)
	}
	inserter.operations = nil
	if err := scheduler.Revive(ctx); err != nil {
		t.Fatalf("revive: %v", err)
	}
	if len(inserter.operations) != 0 {
		t.Fatalf("a succeeded operation was re-queued: %v", inserter.operations)
	}
}
