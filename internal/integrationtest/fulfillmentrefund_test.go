//go:build integration

package integrationtest

import (
	"context"
	"testing"

	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/fulfillment"
)

// paidOrder settles a priced purchase through a manual intent and returns the
// order, its intent, and its `create` operation.
func (harness *harness) paidOrder(ctx context.Context, t *testing.T, store *commercepg.Store, customerID, code string, amount int64) (string, string, string) {
	t.Helper()
	planVersionID := harness.catalog(ctx, t, code, amount)
	order, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: planVersionID, Currency: "RUB",
		Operation: "purchase", IdempotencyKey: code + "-order",
	})
	if err != nil {
		t.Fatalf("create order: %v", err)
	}
	orderID := uuidText(order.ID)
	if classification := harness.settle(ctx, t, store, orderID, amount, code+"-payment"); classification != commerce.ClassificationPaid {
		t.Fatalf("settle: %q", classification)
	}
	var intentID, operationID string
	if err := harness.pool.QueryRow(ctx, `SELECT id::text FROM payment_intents WHERE order_id = $1::uuid`, orderID).Scan(&intentID); err != nil {
		t.Fatalf("read intent: %v", err)
	}
	if err := harness.pool.QueryRow(ctx, `SELECT f.id::text FROM fulfillment_operations f
		JOIN entitlements e ON e.id = f.entitlement_id
		WHERE e.order_id = $1::uuid AND f.operation = 'create'`, orderID).Scan(&operationID); err != nil {
		t.Fatalf("read operation: %v", err)
	}
	return orderID, intentID, operationID
}

// TestARefundBeforeProvisioningCancelsTheCreate proves the worker never hands
// out access for an order whose money was given back before provisioning
// ran: the operation is cancelled untouched and the entitlement retired.
func TestARefundBeforeProvisioningCancelsTheCreate(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	store := harness.commerceStore(t, testOptions())
	customerID := harness.customer(ctx, t)
	orderID, intentID, operationID := harness.paidOrder(ctx, t, store, customerID, "refund-early", 25000)

	if _, err := store.RecordRefund(ctx, intentID, "refund-early-1", "refund:refund-early-1", commerce.Money{Amount: 25000, Currency: "RUB"}); err != nil {
		t.Fatalf("refund: %v", err)
	}
	worker := fulfillment.NewWorker(harness.pool, untouchableProvisioner{t: t})
	if err := runFulfillment(ctx, worker, operationID); err != nil {
		t.Fatalf("work: %v", err)
	}
	var status, reason, entitlementStatus, orderState string
	if err := harness.pool.QueryRow(ctx, `SELECT status, COALESCE(last_error_code, '') FROM fulfillment_operations WHERE id = $1::uuid`, operationID).Scan(&status, &reason); err != nil {
		t.Fatalf("read operation: %v", err)
	}
	if status != "cancelled" || reason != "order_refunded" {
		t.Fatalf("operation ended %s/%s, want cancelled/order_refunded", status, reason)
	}
	if err := harness.pool.QueryRow(ctx, `SELECT o.state, e.status FROM orders o JOIN entitlements e ON e.order_id = o.id
		WHERE o.id = $1::uuid`, orderID).Scan(&orderState, &entitlementStatus); err != nil {
		t.Fatalf("read order: %v", err)
	}
	if orderState != "refunded" || entitlementStatus != "superseded" {
		t.Fatalf("order %s, entitlement %s; want refunded and a retired entitlement", orderState, entitlementStatus)
	}
}

// TestARefundDuringProvisioningIsNotOverwritten covers the race the guarded
// write exists for: the refund lands while Remnawave is being asked, and the
// worker's success must not turn the refunded order back into a fulfilled
// one.
func TestARefundDuringProvisioningIsNotOverwritten(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	store := harness.commerceStore(t, testOptions())
	customerID := harness.customer(ctx, t)
	orderID, intentID, operationID := harness.paidOrder(ctx, t, store, customerID, "refund-race", 25000)

	panel := newMemoryRemnawave()
	refunded := false
	panel.onCall = func(string) {
		if refunded {
			return
		}
		refunded = true
		if _, err := store.RecordRefund(ctx, intentID, "refund-race-1", "refund:refund-race-1", commerce.Money{Amount: 25000, Currency: "RUB"}); err != nil {
			t.Errorf("refund mid-flight: %v", err)
		}
	}
	worker := fulfillment.NewWorker(harness.pool, panel)
	if err := runFulfillment(ctx, worker, operationID); err != nil {
		t.Fatalf("work: %v", err)
	}
	var orderState string
	if err := harness.pool.QueryRow(ctx, `SELECT state FROM orders WHERE id = $1::uuid`, orderID).Scan(&orderState); err != nil {
		t.Fatalf("read order: %v", err)
	}
	if orderState != "refunded" {
		t.Fatalf("the worker overwrote a refund: order is %q", orderState)
	}
}
