//go:build integration

package integrationtest

import (
	"context"
	"testing"
	"time"
)

// Payment health against a real database.
//
// The property worth a container is the definition, not the arithmetic: two
// rates over three different denominators, computed by a query with FILTER
// clauses and an ordered-set aggregate. A mock would agree with whatever the Go
// code believed the query said.

func TestSettlementAndCompletionAreDifferentRates(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service := newOperations(t, harness)

	customer := seedCustomer(ctx, t, harness, "payment-health@example.test")
	created := time.Now().UTC().Add(-6 * time.Hour)
	order := seedPendingOrder(ctx, t, harness, customer, "USD", 1000)

	// Eight settled, one refused by the gateway, one abandoned by the customer,
	// and one still in flight.
	for index := 0; index < 8; index++ {
		seedIntent(ctx, t, harness, order, "yookassa", "succeeded", 1000, created)
	}
	seedIntent(ctx, t, harness, order, "yookassa", "failed", 1000, created)
	seedIntent(ctx, t, harness, order, "yookassa", "cancelled", 1000, created)
	seedIntent(ctx, t, harness, order, "yookassa", "pending", 1000, created)

	report, err := service.PaymentHealth(
		ctx, created.Add(-time.Hour), created.Add(time.Hour), "UTC")
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if len(report.Providers) != 1 {
		t.Fatalf("%d provider lines, want 1: %+v", len(report.Providers), report.Providers)
	}
	line := report.Providers[0]

	if line.Settled != 8 || line.Failed != 1 || line.Abandoned != 1 || line.StillOpen != 1 {
		t.Fatalf("outcomes are %+v", line)
	}

	// Settlement excludes the abandoned one — the customer walking away is not
	// the acquirer failing — and excludes the one still in flight.
	if line.SettlementRate == nil || *line.SettlementRate != 8.0/9.0 {
		t.Errorf("settlement rate is %v, want 8/9: it must exclude the abandoned "+
			"intent and the one still open", line.SettlementRate)
	}
	// Completion includes it, because the funnel is a different question.
	if line.CompletionRate == nil || *line.CompletionRate != 8.0/10.0 {
		t.Errorf("completion rate is %v, want 8/10", line.CompletionRate)
	}
	if line.SettledMinor != 8000 {
		t.Errorf("settled amount is %d, want 8000", line.SettledMinor)
	}
	// The stuck-payment queue as a number rather than as a list.
	if line.OldestOpenSeconds < 3600 {
		t.Errorf("the oldest open intent reads %ds, but one was created six hours ago",
			line.OldestOpenSeconds)
	}
}

// A provider nobody used must not read as a provider that failed.
func TestARateIsAbsentRatherThanZeroWhenNothingDecided(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service := newOperations(t, harness)

	customer := seedCustomer(ctx, t, harness, "payment-quiet@example.test")
	created := time.Now().UTC().Add(-2 * time.Hour)
	order := seedPendingOrder(ctx, t, harness, customer, "USD", 1000)
	seedIntent(ctx, t, harness, order, "cryptobot", "pending", 1000, created)

	report, err := service.PaymentHealth(
		ctx, created.Add(-time.Hour), created.Add(time.Hour), "UTC")
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	line := report.Providers[0]
	if line.SettlementRate != nil {
		t.Fatalf(
			"a provider whose only payment is still open reported a rate of %v; "+
				"nobody-paid and everybody-failed are opposite facts",
			*line.SettlementRate,
		)
	}
}

// Latency comes from the settlement event rather than from `updated_at`, so a
// later reconciliation cannot inflate it.
func TestSettlementLatencyIgnoresLaterWrites(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service := newOperations(t, harness)

	customer := seedCustomer(ctx, t, harness, "payment-latency@example.test")
	created := time.Now().UTC().Add(-4 * time.Hour)
	order := seedPendingOrder(ctx, t, harness, customer, "USD", 1000)
	intent := seedIntent(ctx, t, harness, order, "yookassa", "succeeded", 1000, created)

	// It settled two minutes after it was created.
	if _, err := harness.pool.Exec(ctx, `
		INSERT INTO payment_events (payment_intent_id, type, status, provider_event_id, occurred_at)
		VALUES ($1, 'status_changed', 'succeeded', 'evt-1', $2)`,
		intent, created.Add(2*time.Minute),
	); err != nil {
		t.Fatalf("seed event: %v", err)
	}
	// And something touched the row four hours later.
	if _, err := harness.pool.Exec(ctx,
		`UPDATE payment_intents SET updated_at = now() WHERE id = $1`, intent,
	); err != nil {
		t.Fatalf("touch: %v", err)
	}

	report, err := service.PaymentHealth(
		ctx, created.Add(-time.Hour), created.Add(time.Hour), "UTC")
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if got := report.Providers[0].MedianSettleSeconds; got != 120 {
		t.Fatalf("median settle time is %ds, want 120: a later write moved it", got)
	}
}

func seedPendingOrder(
	ctx context.Context, t *testing.T, harness *harness,
	customerID, currency string, amount int64,
) string {
	t.Helper()
	var id string
	if err := harness.pool.QueryRow(ctx, `
		INSERT INTO orders (
			user_id, state, operation, currency, subtotal_minor, external_minor,
			idempotency_key
		) VALUES ($1, 'pending', 'purchase', $2, $3, $3, 'seed-intents')
		RETURNING id`,
		customerID, currency, amount,
	).Scan(&id); err != nil {
		t.Fatalf("seed order: %v", err)
	}
	return id
}

// seedIntent writes one payment intent. The idempotency key is generated by the
// database so a test can create a dozen without inventing names for them.
func seedIntent(
	ctx context.Context, t *testing.T, harness *harness,
	orderID, provider, status string, amount int64, createdAt time.Time,
) string {
	t.Helper()
	var id string
	if err := harness.pool.QueryRow(ctx, `
		INSERT INTO payment_intents (
			order_id, provider, status, amount_minor, currency, idempotency_key, created_at
		) VALUES ($1, $2, $3, $4, 'USD', gen_random_uuid()::text, $5)
		RETURNING id`,
		orderID, provider, status, amount, createdAt,
	).Scan(&id); err != nil {
		t.Fatalf("seed intent: %v", err)
	}
	return id
}
