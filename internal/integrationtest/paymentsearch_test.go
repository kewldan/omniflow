//go:build integration

package integrationtest

import (
	"context"
	"testing"

	"github.com/omniflow/omniflow/internal/panelpg"
)

// The finance search narrows by provider and by what an operator has in hand
// — a provider's payment reference or an order ID prefix — and reports how
// many matching orders sit in each state.
func TestFinanceSearchByProviderTextAndStateCounts(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	panel, err := panelpg.New(harness.pool, operationsKey, panelpg.Options{})
	if err != nil {
		t.Fatalf("panel: %v", err)
	}
	customerID := harness.customer(ctx, t)
	seed := func(key, state, provider, reference string) string {
		var orderID string
		if err := harness.pool.QueryRow(ctx, `INSERT INTO orders
			(user_id, state, operation, currency, subtotal_minor, external_minor, paid_minor, idempotency_key)
			VALUES ($1::uuid, $2, 'topup', 'RUB', 100, 100, 0, $3) RETURNING id::text`, customerID, state, key).Scan(&orderID); err != nil {
			t.Fatalf("order: %v", err)
		}
		if _, err := harness.pool.Exec(ctx, `INSERT INTO payment_intents
			(order_id, provider, status, amount_minor, currency, provider_reference, idempotency_key)
			VALUES ($1::uuid, $2, 'pending', 100, 'RUB', $3, $4)`, orderID, provider, reference, key+"-intent"); err != nil {
			t.Fatalf("intent: %v", err)
		}
		return orderID
	}
	first := seed("search-a", "pending", "yookassa", "yk-777")
	seed("search-b", "expired", "yookassa", "yk-778")
	seed("search-c", "pending", "cryptobot", "cb-1")

	page, err := panel.SearchOrders(ctx, panelpg.OrderFilter{Provider: "yookassa"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(page.Items) != 2 || page.StateCounts["pending"] != 1 || page.StateCounts["expired"] != 1 {
		t.Fatalf("provider filter: %d items, counts %v", len(page.Items), page.StateCounts)
	}
	page, err = panel.SearchOrders(ctx, panelpg.OrderFilter{Provider: "yookassa", State: "pending"})
	if err != nil || len(page.Items) != 1 || page.StateCounts["expired"] != 1 {
		t.Fatalf("state filter keeps the other counters: %d items, %v, %v", len(page.Items), page.StateCounts, err)
	}
	if page, err = panel.SearchOrders(ctx, panelpg.OrderFilter{Search: "yk-777"}); err != nil || len(page.Items) != 1 || page.Items[0].ID != first {
		t.Fatalf("reference search: %+v %v", page.Items, err)
	}
	if page, err = panel.SearchOrders(ctx, panelpg.OrderFilter{Search: first[:8]}); err != nil || len(page.Items) != 1 {
		t.Fatalf("ID prefix search: %+v %v", page.Items, err)
	}
	if page, err = panel.SearchOrders(ctx, panelpg.OrderFilter{Search: "nothing-like-this"}); err != nil || len(page.Items) != 0 {
		t.Fatalf("a miss matched: %+v %v", page.Items, err)
	}
}
