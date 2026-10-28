//go:build integration

package integrationtest

import (
	"context"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/commercepg"
)

// TestAGoodsOrderPaidAfterItsQuoteLapsedIsNotDelivered proves a stale gateway
// quote is never honoured by a late payment: the order stays expired, nothing
// is queued for delivery, and an operator is told.
func TestAGoodsOrderPaidAfterItsQuoteLapsedIsNotDelivered(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	store := harness.commerceStore(t, testOptions())
	customerID := harness.customer(ctx, t)
	productID := harness.quotedProduct(ctx, t)

	order, err := store.CreateGoodsOrder(ctx, commercepg.GoodsOrderInput{
		CustomerID: customerID, ProductID: productID, Quantity: 1, Recipient: "somebody",
		CostMinor: 30000, PriceMinor: 34500, CostKnown: true, Currency: "RUB",
		QuoteExpiresAt: time.Now().Add(time.Minute), IdempotencyKey: "late-goods", SkipWallet: true,
	})
	if err != nil {
		t.Fatalf("goods order: %v", err)
	}
	orderID := uuidText(order.ID)
	// The quote lapses and the sweep closes the order.
	if _, err := harness.pool.Exec(ctx, `UPDATE orders SET state = 'expired', expires_at = now() - interval '1 minute'
		WHERE id = $1::uuid`, orderID); err != nil {
		t.Fatalf("expire: %v", err)
	}

	if classification := harness.settle(ctx, t, store, orderID, order.ExternalMinor, "late-goods-payment"); classification != commerce.ClassificationStaleQuote {
		t.Fatalf("expected %q, got %q", commerce.ClassificationStaleQuote, classification)
	}
	var state, deliveryStatus string
	if err := harness.pool.QueryRow(ctx, `SELECT o.state, g.status FROM orders o JOIN goods_orders g ON g.order_id = o.id
		WHERE o.id = $1::uuid`, orderID).Scan(&state, &deliveryStatus); err != nil {
		t.Fatalf("read order: %v", err)
	}
	if state != "expired" || deliveryStatus != "quoted" {
		t.Fatalf("a stale quote was settled: order %s, delivery %s", state, deliveryStatus)
	}
	var incidents int
	if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM operator_notifications
		WHERE kind = 'incident' AND payload ->> 'classification' = 'stale_quote'`).Scan(&incidents); err != nil {
		t.Fatalf("count incidents: %v", err)
	}
	if incidents != 1 {
		t.Fatalf("expected one operator incident, got %d", incidents)
	}
}
