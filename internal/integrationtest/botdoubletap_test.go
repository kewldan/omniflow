//go:build integration

package integrationtest

import (
	"context"
	"testing"
)

// TestBotDoubleTapOpensOneTopUp taps the same top-up button twice on the same
// message. The order key used to be minted per call, so every arrival that got
// past the callback claim was a new order — and the claim was keyed on the
// callback identifier, which differs for every tap. The key is now the tap's,
// so both arrivals reach the one order. A tap on another message is a new
// top-up.
func TestBotDoubleTapOpensOneTopUp(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	flow := newBotFlow(ctx, t, harness)
	const account = int64(705001)
	customerID := flow.customerOf(ctx, t, account)

	flow.tap(ctx, account, 60, "act:topup-pm:manual:50000")
	flow.tap(ctx, account, 60, "act:topup-pm:manual:50000")
	var orders int
	if err := harness.pool.QueryRow(ctx, `SELECT count(*)::integer FROM orders
		WHERE user_id = $1::uuid AND operation = 'topup'`, customerID).Scan(&orders); err != nil {
		t.Fatalf("count top-ups: %v", err)
	}
	if orders != 1 {
		t.Fatalf("a double tap opened %d top-up orders, want 1", orders)
	}

	flow.tap(ctx, account, 61, "act:topup-pm:manual:50000")
	if err := harness.pool.QueryRow(ctx, `SELECT count(*)::integer FROM orders
		WHERE user_id = $1::uuid AND operation = 'topup'`, customerID).Scan(&orders); err != nil {
		t.Fatalf("count top-ups: %v", err)
	}
	if orders != 2 {
		t.Fatalf("a tap on another message reached the first order: %d orders, want 2", orders)
	}
}
