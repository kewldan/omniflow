//go:build integration

package integrationtest

import (
	"context"
	"testing"

	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/commercepg"
)

// TestAPaymentOnACancelledOrderSettlesLateAndTellsTheOperator proves the
// honest outcome of a checkout that outlived its order: the customer paid, so
// the product is delivered, and the operator hears about it because the
// customer had been told nothing would be charged.
func TestAPaymentOnACancelledOrderSettlesLateAndTellsTheOperator(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	store := commercepg.New(harness.pool, nil, testOptions())
	customerID := harness.customer(ctx, t)
	planVersionID := harness.catalog(ctx, t, "cancelled-plan", 25000)

	order, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: planVersionID, Currency: "RUB",
		Operation: "purchase", IdempotencyKey: "cancelled-order",
	})
	if err != nil {
		t.Fatalf("create order: %v", err)
	}
	if _, err := harness.pool.Exec(ctx, `UPDATE orders SET state = 'cancelled' WHERE id = $1`, order.ID); err != nil {
		t.Fatalf("cancel order: %v", err)
	}

	classification := harness.settle(ctx, t, store, uuidText(order.ID), 25000, "event-after-cancel")
	if classification != commerce.ClassificationPaidAfterCancellation {
		t.Fatalf("expected %q, got %q", commerce.ClassificationPaidAfterCancellation, classification)
	}
	var state string
	if err := harness.pool.QueryRow(ctx, `SELECT state FROM orders WHERE id = $1`, order.ID).Scan(&state); err != nil {
		t.Fatalf("read order: %v", err)
	}
	if state != "paid" {
		t.Fatalf("a matching payment on a cancelled order must settle, got %q", state)
	}
	var entitlements, incidents, lateEvents int
	if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM entitlements WHERE order_id = $1`, order.ID).Scan(&entitlements); err != nil {
		t.Fatalf("count entitlements: %v", err)
	}
	if entitlements != 1 {
		t.Fatalf("the customer paid and must get the product, got %d entitlements", entitlements)
	}
	if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM payment_events
		WHERE type = 'late' AND details ->> 'classification' = 'paid_after_cancellation'`).Scan(&lateEvents); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if lateEvents != 1 {
		t.Fatalf("expected one named late event, got %d", lateEvents)
	}
	if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM operator_notifications
		WHERE kind = 'incident' AND payload ->> 'classification' = 'paid_after_cancellation'`).Scan(&incidents); err != nil {
		t.Fatalf("count incidents: %v", err)
	}
	if incidents != 1 {
		t.Fatalf("expected one operator incident, got %d", incidents)
	}
	// A replayed event is a duplicate against a settled order, and duplicates
	// reach the operator too — but the incident for this intent and
	// classification is queued once.
	harness.settle(ctx, t, store, uuidText(order.ID), 25000, "event-after-cancel-replay")
	harness.settle(ctx, t, store, uuidText(order.ID), 25000, "event-after-cancel-replay-2")
	if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM operator_notifications
		WHERE kind = 'incident' AND payload ->> 'classification' = 'duplicate'`).Scan(&incidents); err != nil {
		t.Fatalf("count duplicate incidents: %v", err)
	}
	if incidents != 1 {
		t.Fatalf("duplicate payments must produce one incident per intent, got %d", incidents)
	}
}

// TestNonSettlingPaymentsReachTheOperator covers the classifications that leave
// money at the provider with the order still open: each queues an incident in
// the same commit as the payment event, so nothing waits for a complaint.
func TestNonSettlingPaymentsReachTheOperator(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	store := commercepg.New(harness.pool, nil, testOptions())
	customerID := harness.customer(ctx, t)
	planVersionID := harness.catalog(ctx, t, "attention-plan", 25000)

	order, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: planVersionID, Currency: "RUB",
		Operation: "purchase", IdempotencyKey: "attention-order",
	})
	if err != nil {
		t.Fatalf("create order: %v", err)
	}
	if classification := harness.settle(ctx, t, store, uuidText(order.ID), 20000, "event-under"); classification != commerce.ClassificationUnderpayment {
		t.Fatalf("expected an underpayment, got %q", classification)
	}
	if classification := harness.settle(ctx, t, store, uuidText(order.ID), 30000, "event-over"); classification != commerce.ClassificationOverpayment {
		t.Fatalf("expected an overpayment, got %q", classification)
	}
	var state string
	if err := harness.pool.QueryRow(ctx, `SELECT state FROM orders WHERE id = $1`, order.ID).Scan(&state); err != nil {
		t.Fatalf("read order: %v", err)
	}
	if state != "pending" {
		t.Fatalf("a mismatched payment must not settle the order, got %q", state)
	}
	rows, err := harness.pool.Query(ctx, `SELECT payload ->> 'classification' FROM operator_notifications WHERE kind = 'incident' ORDER BY created_at`)
	if err != nil {
		t.Fatalf("list incidents: %v", err)
	}
	defer rows.Close()
	seen := map[string]int{}
	for rows.Next() {
		var classification string
		if err := rows.Scan(&classification); err != nil {
			t.Fatalf("scan: %v", err)
		}
		seen[classification]++
	}
	if seen[commerce.ClassificationUnderpayment] != 1 || seen[commerce.ClassificationOverpayment] != 1 {
		t.Fatalf("expected one incident per classification, got %v", seen)
	}
}
