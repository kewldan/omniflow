//go:build integration

package integrationtest

import (
	"context"
	"log/slog"
	"testing"

	"github.com/omniflow/omniflow/internal/app"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/paymentservice"
	"github.com/omniflow/omniflow/internal/renewal"
)

// renewalFixture is one customer holding one subscription that is about to
// run out, with automatic renewal agreed and funded from the wallet.
type renewalFixture struct {
	core           *app.CommerceCore
	worker         *renewal.Worker
	customerID     string
	subscriptionID string
	planVersionID  string
	entitlementID  string
}

const renewalPrice = 25000

// newRenewalFixture buys a first period from the wallet, moves its end inside
// the renewal lead time, and records the customer's consent to renew from the
// wallet. walletLeft is what the balance holds once the first period is paid.
func newRenewalFixture(ctx context.Context, t *testing.T, harness *harness, walletLeft int64) renewalFixture {
	t.Helper()
	core := harness.commerce(t, testOptions())
	customerID := harness.customer(ctx, t)
	planVersionID := harness.catalog(ctx, t, "renewal-plan", renewalPrice)

	if funding := renewalPrice + walletLeft; funding > 0 {
		topUp, err := core.Store.CreateTopUpOrder(ctx, commercepg.TopUpInput{
			CustomerID: customerID, Currency: "RUB", AmountMinor: funding, IdempotencyKey: "renewal-funding",
		})
		if err != nil {
			t.Fatalf("create top-up: %v", err)
		}
		harness.settle(ctx, t, core.Store, uuidText(topUp.ID), funding, "event-renewal-funding")
	}
	first, err := core.Store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: planVersionID, Currency: "RUB",
		Operation: "purchase", IdempotencyKey: "renewal-first-period",
	})
	if err != nil {
		t.Fatalf("buy the first period: %v", err)
	}
	if first.State != "paid" {
		t.Fatalf("the wallet must pay the first period, got %q", first.State)
	}

	fixture := renewalFixture{core: core, customerID: customerID, planVersionID: planVersionID}
	if err = harness.pool.QueryRow(ctx, `SELECT id::text, subscription_id::text FROM entitlements
		WHERE user_id = $1::uuid`, customerID).Scan(&fixture.entitlementID, &fixture.subscriptionID); err != nil {
		t.Fatalf("read the entitlement: %v", err)
	}
	// The first period was provisioned (the fulfillment worker would have made
	// it active), and it ends inside the default three-day lead time, so the
	// next pass finds it due.
	if _, err = harness.pool.Exec(ctx,
		`UPDATE entitlements SET status = 'active', ends_at = now() + interval '1 day' WHERE id = $1::uuid`,
		fixture.entitlementID); err != nil {
		t.Fatalf("bring the renewal due: %v", err)
	}
	if _, err = harness.pool.Exec(ctx, `INSERT INTO auto_renew_settings
		(user_id, subscription_id, enabled, plan_version_id, provider, currency,
		 funding, consent_at, state)
		VALUES ($1::uuid, $2::uuid, true, $3::uuid, 'manual', 'RUB', 'wallet', now(), 'scheduled')`,
		customerID, fixture.subscriptionID, planVersionID); err != nil {
		t.Fatalf("agree to renew automatically: %v", err)
	}
	// A Telegram identity is what the bot's dunning notifier delivers to.
	if _, err = harness.pool.Exec(ctx, `INSERT INTO remnawave_users (user_id, remnawave_id, telegram_id)
		VALUES ($1::uuid, 880001, 880001)`, customerID); err != nil {
		t.Fatalf("link Telegram: %v", err)
	}
	fixture.worker = renewal.New(
		harness.pool, core.Store, core.Payments, paymentservice.Index(core.Providers),
		slog.New(slog.DiscardHandler), renewal.Config{},
	)
	return fixture
}

type dunningRow struct {
	attempt      int
	outcome      string
	failureCode  string
	orderID      string
	notify       bool
	scheduledFor string
}

func (harness *harness) dunning(ctx context.Context, t *testing.T, customerID string) []dunningRow {
	t.Helper()
	rows, err := harness.pool.Query(ctx, `SELECT attempt, outcome, COALESCE(failure_code, ''),
		COALESCE(order_id::text, ''), notify_required, scheduled_for::text
		FROM dunning_attempts WHERE user_id = $1::uuid ORDER BY attempt`, customerID)
	if err != nil {
		t.Fatalf("read dunning: %v", err)
	}
	defer rows.Close()
	var result []dunningRow
	for rows.Next() {
		var row dunningRow
		if err = rows.Scan(&row.attempt, &row.outcome, &row.failureCode, &row.orderID,
			&row.notify, &row.scheduledFor); err != nil {
			t.Fatalf("scan dunning: %v", err)
		}
		result = append(result, row)
	}
	return result
}

func (harness *harness) autoRenewState(ctx context.Context, t *testing.T, customerID string) (string, string) {
	t.Helper()
	var state, code string
	if err := harness.pool.QueryRow(ctx, `SELECT state, COALESCE(last_failure_code, '')
		FROM auto_renew_settings WHERE user_id = $1::uuid`, customerID).Scan(&state, &code); err != nil {
		t.Fatalf("read auto-renew state: %v", err)
	}
	return state, code
}

// queuedFulfillmentFor counts River fulfillment jobs for the operations an
// order produced. The job is what provisions the renewed period; an order
// that settles without one is a customer who paid for nothing.
func (harness *harness) queuedFulfillmentFor(ctx context.Context, t *testing.T, orderID string) int {
	t.Helper()
	var jobs int
	if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM river_job j
		JOIN fulfillment_operations f ON f.id::text = j.args->>'operationId'
		WHERE j.kind = 'fulfillment' AND f.idempotency_key LIKE 'order:' || $1 || ':%'`, orderID).Scan(&jobs); err != nil {
		t.Fatalf("count fulfillment jobs: %v", err)
	}
	return jobs
}

// TestAWalletFundedRenewalSettlesAndQueuesProvisioning runs one renewal cycle
// end to end through the wiring the worker uses: a due entitlement becomes a
// scheduled attempt, the attempt opens the cycle's order, the wallet settles
// it, and the settlement queues the fulfillment job that extends access — all
// before the period runs out, and exactly once however often the pass runs.
func TestAWalletFundedRenewalSettlesAndQueuesProvisioning(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	fixture := newRenewalFixture(ctx, t, harness, renewalPrice)

	fixture.worker.RunOnce(ctx)

	attempts := harness.dunning(ctx, t, fixture.customerID)
	if len(attempts) != 1 || attempts[0].outcome != "succeeded" || attempts[0].orderID == "" {
		t.Fatalf("dunning after one pass = %+v, want one succeeded attempt with its order", attempts)
	}
	order, err := dbgen.New(harness.pool).GetOrder(ctx, mustUUID(t, attempts[0].orderID))
	if err != nil {
		t.Fatalf("read the renewal order: %v", err)
	}
	if order.State != "paid" && order.State != "fulfilled" {
		t.Fatalf("renewal order is %q, want settled", order.State)
	}
	if order.WalletMinor != renewalPrice || order.ExternalMinor != 0 {
		t.Fatalf("renewal order wallet/external = %d/%d, want the wallet to cover it",
			order.WalletMinor, order.ExternalMinor)
	}
	if uuidText(order.SubscriptionID) != fixture.subscriptionID {
		t.Fatal("the renewal order targets a different subscription")
	}
	if balance := harness.walletBalance(ctx, t, fixture.customerID); balance != 0 {
		t.Fatalf("wallet after renewal = %d, want 0", balance)
	}
	if jobs := harness.queuedFulfillmentFor(ctx, t, attempts[0].orderID); jobs != 1 {
		t.Fatalf("the renewal queued %d fulfillment jobs, want 1", jobs)
	}
	if state, code := harness.autoRenewState(ctx, t, fixture.customerID); state != "scheduled" || code != "" {
		t.Fatalf("auto-renew state = %s (%s), want scheduled for the next period", state, code)
	}

	// The worker runs this pass every minute, and a second replica may run it
	// too. Neither may open another order or charge the wallet again.
	fixture.worker.RunOnce(ctx)
	fixture.worker.RunOnce(ctx)
	if again := harness.dunning(ctx, t, fixture.customerID); len(again) != 1 {
		t.Fatalf("repeated passes left %d attempts, want 1", len(again))
	}
	var renewals int
	if err = harness.pool.QueryRow(ctx, `SELECT count(*) FROM orders
		WHERE user_id = $1::uuid AND idempotency_key LIKE 'renewal:%'`, fixture.customerID).Scan(&renewals); err != nil {
		t.Fatalf("count renewal orders: %v", err)
	}
	if renewals != 1 {
		t.Fatalf("repeated passes opened %d renewal orders, want 1", renewals)
	}
	if jobs := harness.queuedFulfillmentFor(ctx, t, attempts[0].orderID); jobs != 1 {
		t.Fatalf("repeated passes queued %d fulfillment jobs, want 1", jobs)
	}
}

// TestAFailedRenewalChargeRunsDunningAndTellsTheCustomer walks the failure
// path: a wallet that cannot cover the renewal declines the first attempt,
// the schedule retries without charging anything, the customer is queued a
// message once the retries reach the point the rules say they must hear, and
// a top-up made in the meantime lets the next attempt settle the same cycle
// with its one order.
func TestAFailedRenewalChargeRunsDunningAndTellsTheCustomer(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	fixture := newRenewalFixture(ctx, t, harness, 0)
	queries := dbgen.New(harness.pool)

	fixture.worker.RunOnce(ctx)
	attempts := harness.dunning(ctx, t, fixture.customerID)
	if len(attempts) != 2 {
		t.Fatalf("after a decline the schedule holds %d attempts, want the failed one and its retry", len(attempts))
	}
	if attempts[0].outcome != "failed" || attempts[0].failureCode != "insufficient_wallet_balance" {
		t.Fatalf("first attempt = %+v, want failed for the wallet balance", attempts[0])
	}
	if attempts[0].notify {
		t.Fatal("the first decline is retried quietly; it must not message the customer yet")
	}
	if attempts[1].outcome != "scheduled" {
		t.Fatalf("second attempt = %+v, want scheduled", attempts[1])
	}
	if state, code := harness.autoRenewState(ctx, t, fixture.customerID); state != "dunning" || code != "insufficient_wallet_balance" {
		t.Fatalf("auto-renew state = %s (%s), want dunning", state, code)
	}
	// A short wallet opens no order: reserving what the customer has for the
	// whole dunning window would fix the shortfall into the order.
	var orders int
	if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM orders
		WHERE user_id = $1::uuid AND idempotency_key LIKE 'renewal:%'`, fixture.customerID).Scan(&orders); err != nil {
		t.Fatalf("count renewal orders: %v", err)
	}
	if orders != 0 {
		t.Fatalf("a declined wallet renewal opened %d orders", orders)
	}

	// The retry is hours away; a pass before then changes nothing.
	fixture.worker.RunOnce(ctx)
	if again := harness.dunning(ctx, t, fixture.customerID); len(again) != 2 || again[1].outcome != "scheduled" {
		t.Fatalf("a pass before the retry was due changed the schedule: %+v", again)
	}

	// Bring the retry due. It declines too, and from here on the rules say
	// the customer must be told, so the attempt carries the notice.
	bringDue := func(attempt int) {
		t.Helper()
		if _, err := harness.pool.Exec(ctx, `UPDATE dunning_attempts SET scheduled_for = now() - interval '1 second'
			WHERE user_id = $1::uuid AND attempt = $2`, fixture.customerID, attempt); err != nil {
			t.Fatalf("bring attempt %d due: %v", attempt, err)
		}
	}
	bringDue(2)
	fixture.worker.RunOnce(ctx)
	attempts = harness.dunning(ctx, t, fixture.customerID)
	if len(attempts) != 3 || attempts[1].outcome != "failed" || !attempts[1].notify {
		t.Fatalf("after the second decline = %+v, want it failed with a notice and a third scheduled", attempts)
	}
	pending, err := queries.ListPendingDunningNotifications(ctx, 10)
	if err != nil {
		t.Fatalf("list pending notices: %v", err)
	}
	if len(pending) != 1 || pending[0].DunningAttempt.Attempt != 2 || pending[0].TelegramID.Int64 != 880001 {
		t.Fatalf("pending dunning notices = %+v, want the second attempt for the customer's chat", pending)
	}

	// The customer tops up. The next attempt settles the cycle: one order,
	// paid from the wallet, with its provisioning queued.
	topUp, err := fixture.core.Store.CreateTopUpOrder(ctx, commercepg.TopUpInput{
		CustomerID: fixture.customerID, Currency: "RUB", AmountMinor: renewalPrice, IdempotencyKey: "renewal-rescue",
	})
	if err != nil {
		t.Fatalf("top up: %v", err)
	}
	harness.settle(ctx, t, fixture.core.Store, uuidText(topUp.ID), renewalPrice, "event-renewal-rescue")
	bringDue(3)
	fixture.worker.RunOnce(ctx)
	attempts = harness.dunning(ctx, t, fixture.customerID)
	if len(attempts) != 3 || attempts[2].outcome != "succeeded" || attempts[2].orderID == "" {
		t.Fatalf("after the top-up = %+v, want the third attempt to succeed", attempts)
	}
	if jobs := harness.queuedFulfillmentFor(ctx, t, attempts[2].orderID); jobs != 1 {
		t.Fatalf("the rescued renewal queued %d fulfillment jobs, want 1", jobs)
	}
	if balance := harness.walletBalance(ctx, t, fixture.customerID); balance != 0 {
		t.Fatalf("wallet after the rescued renewal = %d, want 0", balance)
	}
	if state, code := harness.autoRenewState(ctx, t, fixture.customerID); state != "scheduled" || code != "" {
		t.Fatalf("auto-renew state = %s (%s), want scheduled again", state, code)
	}
}

// TestRenewalStopsAfterTheLastAttemptAndSuspends covers the end of dunning:
// once every attempt has declined, the cycle is abandoned, the customer is
// told, and automatic renewal is suspended rather than retried forever.
func TestRenewalStopsAfterTheLastAttemptAndSuspends(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	fixture := newRenewalFixture(ctx, t, harness, 0)

	fixture.worker.RunOnce(ctx)
	for attempt := 2; attempt <= 4; attempt++ {
		if _, err := harness.pool.Exec(ctx, `UPDATE dunning_attempts SET scheduled_for = now() - interval '1 second'
			WHERE user_id = $1::uuid AND attempt = $2`, fixture.customerID, attempt); err != nil {
			t.Fatalf("bring attempt %d due: %v", attempt, err)
		}
		fixture.worker.RunOnce(ctx)
	}
	attempts := harness.dunning(ctx, t, fixture.customerID)
	if len(attempts) != 4 {
		t.Fatalf("dunning holds %d attempts, want 4", len(attempts))
	}
	last := attempts[3]
	if last.outcome != "abandoned" || !last.notify {
		t.Fatalf("the last attempt = %+v, want abandoned with a notice", last)
	}
	if state, _ := harness.autoRenewState(ctx, t, fixture.customerID); state != "suspended" {
		t.Fatalf("auto-renew state = %s, want suspended", state)
	}
	// Suspended means the next pass schedules nothing new for this cycle.
	fixture.worker.RunOnce(ctx)
	if again := harness.dunning(ctx, t, fixture.customerID); len(again) != 4 {
		t.Fatalf("a suspended renewal was scheduled again: %d attempts", len(again))
	}
}
