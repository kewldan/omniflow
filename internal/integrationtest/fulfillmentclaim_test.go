//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"testing"

	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/fulfillment"
	"github.com/riverqueue/river/rivertype"
)

// freeOrder creates a zero-priced purchase, which settles at creation and
// leaves exactly one `create` operation behind. It returns the order and the
// operation identifiers.
func (harness *harness) freeOrder(ctx context.Context, t *testing.T, store *commercepg.Store, customerID, code string) (string, string) {
	t.Helper()
	planVersionID := harness.catalog(ctx, t, code, 0)
	order, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: planVersionID, Currency: "RUB",
		Operation: "purchase", IdempotencyKey: code + "-order",
	})
	if err != nil {
		t.Fatalf("create order: %v", err)
	}
	var operationID string
	if err := harness.pool.QueryRow(ctx, `SELECT f.id::text FROM fulfillment_operations f
		JOIN entitlements e ON e.id = f.entitlement_id WHERE e.order_id = $1`, order.ID).Scan(&operationID); err != nil {
		t.Fatalf("read operation: %v", err)
	}
	return uuidText(order.ID), operationID
}

// TestFulfillmentHoldsNothingWhileRemnawaveIsCalled is the point of the
// claim/call/record split: while the worker waits on Remnawave it holds no
// transaction, no lock on the operation, and no pool connection.
func TestFulfillmentHoldsNothingWhileRemnawaveIsCalled(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	store := harness.commerceStore(t, testOptions())
	customerID := harness.customer(ctx, t)
	orderID, operationID := harness.freeOrder(ctx, t, store, customerID, "claim-plan")

	panel := newMemoryRemnawave()
	observed := 0
	panel.onCall = func(call string) {
		observed++
		if acquired := harness.pool.Stat().AcquiredConns(); acquired != 0 {
			t.Errorf("%s: the worker held %d pool connections across a Remnawave call", call, acquired)
		}
		tx, err := harness.pool.Begin(ctx)
		if err != nil {
			t.Errorf("begin: %v", err)
			return
		}
		defer func() { _ = tx.Rollback(ctx) }()
		var status string
		if err := tx.QueryRow(ctx, `SELECT status FROM fulfillment_operations WHERE id = $1::uuid FOR UPDATE NOWAIT`, operationID).Scan(&status); err != nil {
			t.Errorf("%s: the operation row was locked across a Remnawave call: %v", call, err)
			return
		}
		if status != "running" {
			t.Errorf("%s: the operation must be claimed while Remnawave is asked, found %q", call, status)
		}
	}
	worker := fulfillment.NewWorker(harness.pool, panel)
	if err := runFulfillment(ctx, worker, operationID); err != nil {
		t.Fatalf("work: %v", err)
	}
	if observed == 0 {
		t.Fatal("Remnawave was never called")
	}

	var operationStatus, orderState, entitlementStatus string
	if err := harness.pool.QueryRow(ctx, `SELECT status FROM fulfillment_operations WHERE id = $1::uuid`, operationID).Scan(&operationStatus); err != nil {
		t.Fatalf("read operation: %v", err)
	}
	if err := harness.pool.QueryRow(ctx, `SELECT state FROM orders WHERE id = $1::uuid`, orderID).Scan(&orderState); err != nil {
		t.Fatalf("read order: %v", err)
	}
	if err := harness.pool.QueryRow(ctx, `SELECT status FROM entitlements WHERE order_id = $1::uuid`, orderID).Scan(&entitlementStatus); err != nil {
		t.Fatalf("read entitlement: %v", err)
	}
	if operationStatus != "succeeded" || orderState != "fulfilled" || entitlementStatus != "active" {
		t.Fatalf("after the record: operation %s, order %s, entitlement %s", operationStatus, orderState, entitlementStatus)
	}
}

// TestAClaimIsRespectedUntilItsLeaseEndsAndThenTakenOver covers a worker that
// died between claiming an operation and recording the result: a second run
// waits while the lease holds, takes the operation over once it has lapsed —
// the revival scan finds it — and provisions exactly one Remnawave user.
func TestAClaimIsRespectedUntilItsLeaseEndsAndThenTakenOver(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	store := harness.commerceStore(t, testOptions())
	customerID := harness.customer(ctx, t)
	orderID, operationID := harness.freeOrder(ctx, t, store, customerID, "lease-plan")
	panel := newMemoryRemnawave()
	worker := fulfillment.NewWorker(harness.pool, panel)

	// A run claimed it a moment ago and is, as far as anyone knows, still
	// talking to Remnawave.
	if _, err := harness.pool.Exec(ctx, `UPDATE fulfillment_operations
		SET status = 'running', attempt_count = 1, next_attempt_at = now() + interval '1 minute'
		WHERE id = $1::uuid`, operationID); err != nil {
		t.Fatalf("claim: %v", err)
	}
	var snooze *rivertype.JobSnoozeError
	if err := runFulfillment(ctx, worker, operationID); !errors.As(err, &snooze) {
		t.Fatalf("a live claim must be waited out, got %v", err)
	}
	if panel.callCount() != 0 {
		t.Fatal("Remnawave was called beside a live claim")
	}

	// The claim's run died: its lease ended long ago and it never recorded.
	if _, err := harness.pool.Exec(ctx, `UPDATE fulfillment_operations
		SET next_attempt_at = now() - interval '20 minutes', created_at = now() - interval '30 minutes'
		WHERE id = $1::uuid`, operationID); err != nil {
		t.Fatalf("expire the lease: %v", err)
	}
	inserter := &recordingInserter{}
	if err := fulfillment.NewScheduler(harness.pool, inserter).Revive(ctx); err != nil {
		t.Fatalf("revive: %v", err)
	}
	if len(inserter.operations) != 1 || inserter.operations[0] != operationID {
		t.Fatalf("the revival scan must re-queue the abandoned claim, queued %v", inserter.operations)
	}
	if err := runFulfillment(ctx, worker, operationID); err != nil {
		t.Fatalf("take over: %v", err)
	}
	var status string
	var attempts int
	if err := harness.pool.QueryRow(ctx, `SELECT status, attempt_count FROM fulfillment_operations WHERE id = $1::uuid`, operationID).Scan(&status, &attempts); err != nil {
		t.Fatalf("read operation: %v", err)
	}
	if status != "succeeded" || attempts != 2 {
		t.Fatalf("the taken-over operation ended %s after %d attempts, want succeeded after 2", status, attempts)
	}
	var orderState string
	if err := harness.pool.QueryRow(ctx, `SELECT state FROM orders WHERE id = $1::uuid`, orderID).Scan(&orderState); err != nil {
		t.Fatalf("read order: %v", err)
	}
	if orderState != "fulfilled" || panel.userCount() != 1 {
		t.Fatalf("order %s with %d Remnawave users, want fulfilled with one", orderState, panel.userCount())
	}
}

// TestAResultFromALapsedClaimIsDropped proves the record step's guard: when a
// run's claim was taken over while it was still waiting on Remnawave, the
// newer run owns the operation and the stale result writes nothing.
func TestAResultFromALapsedClaimIsDropped(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	store := harness.commerceStore(t, testOptions())
	customerID := harness.customer(ctx, t)
	orderID, operationID := harness.freeOrder(ctx, t, store, customerID, "lapsed-plan")
	panel := newMemoryRemnawave()
	taken := false
	panel.onCall = func(string) {
		if taken {
			return
		}
		taken = true
		// Another run takes the claim over while this one waits.
		if _, err := harness.pool.Exec(ctx, `UPDATE fulfillment_operations
			SET attempt_count = attempt_count + 1, next_attempt_at = now() + interval '2 minutes'
			WHERE id = $1::uuid`, operationID); err != nil {
			t.Errorf("take over: %v", err)
		}
	}
	worker := fulfillment.NewWorker(harness.pool, panel)
	if err := runFulfillment(ctx, worker, operationID); err != nil {
		t.Fatalf("work: %v", err)
	}
	var status, orderState, entitlementStatus string
	if err := harness.pool.QueryRow(ctx, `SELECT status FROM fulfillment_operations WHERE id = $1::uuid`, operationID).Scan(&status); err != nil {
		t.Fatalf("read operation: %v", err)
	}
	if err := harness.pool.QueryRow(ctx, `SELECT o.state, e.status FROM orders o JOIN entitlements e ON e.order_id = o.id
		WHERE o.id = $1::uuid`, orderID).Scan(&orderState, &entitlementStatus); err != nil {
		t.Fatalf("read order: %v", err)
	}
	if status != "running" || orderState != "paid" || entitlementStatus != "pending" {
		t.Fatalf("a stale result was recorded: operation %s, order %s, entitlement %s", status, orderState, entitlementStatus)
	}
}
