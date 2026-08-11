//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// The load tests below are correctness-under-concurrency tests rather than
// throughput benchmarks: the failure they are looking for is a duplicated
// charge or a duplicated credit, which only appears when the same work arrives
// from several directions at once.

const burst = 24

// TestWebhookBurstCreditsOnce replays one settlement from many goroutines, the
// way a provider that retries aggressively behaves.
func TestWebhookBurstCreditsOnce(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	store := commercepg.New(harness.pool, nil, testOptions())
	customerID := harness.customer(ctx, t)

	order, err := store.CreateTopUpOrder(ctx, commercepg.TopUpInput{
		CustomerID: customerID, Currency: "RUB", AmountMinor: 40000, IdempotencyKey: "burst-topup",
	})
	if err != nil {
		t.Fatalf("create top-up: %v", err)
	}
	var intentID string
	if err := harness.pool.QueryRow(ctx, `INSERT INTO payment_intents
		(order_id, provider, status, amount_minor, currency, provider_reference, idempotency_key)
		VALUES ($1, 'manual', 'processing', 40000, 'RUB', 'burst-ref', 'burst-key')
		RETURNING id::text`, order.ID).Scan(&intentID); err != nil {
		t.Fatalf("create intent: %v", err)
	}

	group := &sync.WaitGroup{}
	for index := range burst {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			// Half the burst replays one event ID and half uses distinct ones,
			// so both the event-level and the order-level guards are exercised.
			eventID := "burst-event"
			if index%2 == 1 {
				eventID = "burst-event-" + time.Duration(index).String()
			}
			// Serialisation failures are expected under this much contention and
			// are the caller's cue to retry, not a lost payment.
			_, _, _ = store.RecordProviderPayment(ctx, intentID, eventID,
				commerce.Money{Amount: 40000, Currency: "RUB"}, false)
		}(index)
	}
	group.Wait()

	if balance := harness.walletBalance(ctx, t, customerID); balance != 40000 {
		t.Fatalf("a settlement burst credited %d instead of 40000", balance)
	}
	var credits int
	if err := harness.pool.QueryRow(ctx,
		`SELECT count(*) FROM ledger_transactions WHERE type = 'credit' AND reference_type = 'wallet_topup'`).
		Scan(&credits); err != nil {
		t.Fatalf("count credits: %v", err)
	}
	if credits != 1 {
		t.Fatalf("expected exactly one credit transaction, got %d", credits)
	}
}

// TestConcurrentCartPurchaseChargesOnce runs the deferred purchase from many
// goroutines at once, which is what a sweep racing a customer tap looks like.
func TestConcurrentCartPurchaseChargesOnce(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	store := commercepg.New(harness.pool, nil, testOptions())
	customerID := harness.customer(ctx, t)
	planVersionID := harness.catalog(ctx, t, "burst-plan", 20000)

	if _, err := harness.pool.Exec(ctx, `WITH t AS (
			INSERT INTO ledger_transactions (type, reference_type, reference_id, idempotency_key)
			VALUES ('credit', 'test', 'seed', 'seed-credit') RETURNING id)
		INSERT INTO ledger_entries (transaction_id, account_type, user_id, currency, amount_minor)
		SELECT id, 'customer_wallet', $1::uuid, 'RUB', 100000 FROM t
		UNION ALL SELECT id, 'platform_clearing', NULL, 'RUB', -100000 FROM t`, customerID); err != nil {
		t.Fatalf("seed wallet: %v", err)
	}
	if _, err := store.SaveCart(ctx, commercepg.CartInput{
		CustomerID: customerID, PlanVersionID: planVersionID, Operation: "purchase",
		Currency: "RUB", AutoPurchase: true, TTL: time.Hour,
	}); err != nil {
		t.Fatalf("save cart: %v", err)
	}

	group := &sync.WaitGroup{}
	for range burst {
		group.Add(1)
		go func() {
			defer group.Done()
			_, _ = store.TryPurchaseCart(ctx, customerID)
		}()
	}
	group.Wait()

	var orders int
	if err := harness.pool.QueryRow(ctx,
		`SELECT count(*) FROM orders WHERE user_id = $1::uuid AND operation = 'purchase'`, customerID).Scan(&orders); err != nil {
		t.Fatalf("count orders: %v", err)
	}
	if orders != 1 {
		t.Fatalf("a concurrent cart purchase created %d orders", orders)
	}
	if balance := harness.walletBalance(ctx, t, customerID); balance != 80000 {
		t.Fatalf("expected 80000 left in the wallet, got %d", balance)
	}
}

// TestConcurrentSubscriptionPurchasesRespectTheLimit is the concurrency-limit
// race: without the advisory lock, several simultaneous purchases would each see
// a stale count and all pass the check.
func TestConcurrentSubscriptionPurchasesRespectTheLimit(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	store := commercepg.New(harness.pool, nil, testOptions())
	customerID := harness.customer(ctx, t)
	planVersionID := harness.catalog(ctx, t, "race-plan", 0)

	group := &sync.WaitGroup{}
	rejected := make([]error, burst)
	for index := range burst {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			_, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
				CustomerID: customerID, PlanVersionID: planVersionID, Currency: "RUB",
				Operation: "purchase", NewSubscription: true,
				IdempotencyKey: "race-order-" + time.Duration(index).String(),
			})
			rejected[index] = err
		}(index)
	}
	group.Wait()

	var subscriptions int
	if err := harness.pool.QueryRow(ctx,
		`SELECT count(*) FROM subscriptions WHERE user_id = $1::uuid AND status = 'active'`, customerID).Scan(&subscriptions); err != nil {
		t.Fatalf("count subscriptions: %v", err)
	}
	if subscriptions > testOptions().Subscriptions.EffectiveMax() {
		t.Fatalf("the concurrency limit was breached: %d subscriptions", subscriptions)
	}
	// Every attempt beyond the limit must have been refused by something. Which
	// something is not the property worth asserting: under Serializable, most
	// racers are aborted by PostgreSQL before they ever reach the domain check,
	// and a serialization abort is the stronger outcome — it means the conflict
	// was caught by the database rather than by an application read that could
	// have gone stale. Demanding a specific error made this test assert which
	// guard won a race rather than that the limit held.
	succeeded := 0
	for _, err := range rejected {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, commerce.ErrSubscriptionRejected):
		case isSerializationFailure(err):
		default:
			t.Fatalf("a purchase failed for an unexpected reason: %v", err)
		}
	}
	if succeeded != subscriptions {
		t.Fatalf("%d purchases reported success but %d subscriptions exist", succeeded, subscriptions)
	}
	if succeeded == burst {
		t.Fatal("every concurrent purchase succeeded, so the limit was never exercised")
	}
}

// TestReconciliationSweepIsIdempotentUnderLoad runs the order-expiry and cart
// sweep repeatedly and concurrently, which is what two API replicas do.
func TestReconciliationSweepIsIdempotentUnderLoad(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	store := commercepg.New(harness.pool, nil, testOptions())
	customerID := harness.customer(ctx, t)
	planVersionID := harness.catalog(ctx, t, "sweep-plan", 15000)

	order, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: planVersionID, Currency: "RUB",
		Operation: "purchase", IdempotencyKey: "sweep-order", ExpiresAt: time.Now().Add(-time.Hour),
	})
	if err != nil {
		t.Fatalf("create order: %v", err)
	}

	group := &sync.WaitGroup{}
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			_, _ = dbgen.New(harness.pool).ExpirePendingOrders(ctx)
		}()
	}
	group.Wait()

	var mutations int
	if err := harness.pool.QueryRow(ctx,
		`SELECT count(*) FROM order_mutations WHERE order_id = $1 AND action = 'expire'`, order.ID).Scan(&mutations); err != nil {
		t.Fatalf("count mutations: %v", err)
	}
	if mutations != 1 {
		t.Fatalf("expected one expiry mutation, got %d", mutations)
	}
	var state string
	if err := harness.pool.QueryRow(ctx, `SELECT state FROM orders WHERE id = $1`, order.ID).Scan(&state); err != nil {
		t.Fatalf("read order: %v", err)
	}
	if state != "expired" {
		t.Fatalf("expected the order to expire, got %q", state)
	}
}

// isSerializationFailure reports a transaction PostgreSQL aborted rather than
// let breach an invariant. It is a normal outcome of contention, and the caller
// is expected to retry; here it is proof the conflict was caught.
func isSerializationFailure(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "40001"
}
