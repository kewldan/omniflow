//go:build integration

package integrationtest

import (
	"context"
	"testing"

	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/fulfillment"
)

const limitedSquad = "33333333-3333-3333-3333-333333333333"

// runOperation works the one operation an idempotency key names.
func (harness *harness) runOperation(ctx context.Context, t *testing.T, worker *fulfillment.Worker, key string) {
	t.Helper()
	var id string
	if err := harness.pool.QueryRow(ctx, `SELECT id::text FROM fulfillment_operations WHERE idempotency_key = $1`, key).Scan(&id); err != nil {
		t.Fatalf("find operation %s: %v", key, err)
	}
	if err := runFulfillment(ctx, worker, id); err != nil {
		t.Fatalf("run %s: %v", key, err)
	}
}

// A subscription that runs out of traffic is kept alive on the limited squad
// with a fresh gigabyte counted from what it had used, once; a reconcile keeps
// that state rather than undoing it; and paying for more traffic restores the
// full plan through the fulfillment pipeline.
func TestLimitedAccessKeepsAnExhaustedSubscriptionAliveUntilPayment(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	panel := newMemoryRemnawave()
	worker := fulfillment.NewWorker(harness.pool, panel)
	scheduler := fulfillment.NewScheduler(harness.pool, &recordingInserter{})
	customerID := harness.customer(ctx, t)
	planVersionID := harness.catalog(ctx, t, "limited-plan", 30000)
	pack := harness.addon(ctx, t, planVersionID, "limited-pack", 1000, 10*gigabyte, "full_price")
	harness.seedWallet(ctx, t, customerID, "limited-wallet", 100000)
	if _, err := harness.pool.Exec(ctx, `UPDATE subscription_policy SET limited_access_on_traffic = true,
		limited_access_squad_id = $1::uuid, limited_access_traffic_gb = 1, limited_access_days = 3`, limitedSquad); err != nil {
		t.Fatalf("policy: %v", err)
	}
	bought, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: planVersionID, Currency: "RUB", Operation: "purchase", IdempotencyKey: "limited-buy",
	})
	if err != nil {
		t.Fatalf("buy: %v", err)
	}
	harness.fulfillAll(ctx, t, worker)
	var remoteID int64
	if err = harness.pool.QueryRow(ctx, `UPDATE entitlements SET status = 'limited' WHERE order_id = $1::uuid RETURNING remnawave_user_id`, uuidText(bought.ID)).Scan(&remoteID); err != nil {
		t.Fatalf("limit: %v", err)
	}
	panel.mu.Lock()
	user := panel.users[remoteID]
	user.Status, user.Traffic.UsedBytes = "LIMITED", 100*gigabyte
	panel.users[remoteID] = user
	panel.mu.Unlock()

	for range 2 {
		if err = scheduler.LimitedAccess(ctx); err != nil {
			t.Fatalf("limited access pass: %v", err)
		}
	}
	var sessionID string
	var sessions int
	if err = harness.pool.QueryRow(ctx, `SELECT count(*), min(id::text) FROM limited_access_sessions`).Scan(&sessions, &sessionID); err != nil || sessions != 1 {
		t.Fatalf("sessions = %d, %v", sessions, err)
	}
	harness.runOperation(ctx, t, worker, "limited:"+sessionID+":start")
	panel.mu.Lock()
	user = panel.users[remoteID]
	panel.mu.Unlock()
	if user.Status != "ACTIVE" || user.TrafficLimitBytes != 101*gigabyte || len(user.ActiveInternalSquads) != 1 || user.ActiveInternalSquads[0].UUID != limitedSquad {
		t.Fatalf("limited user = %+v", user)
	}
	var base int64
	if err = harness.pool.QueryRow(ctx, `SELECT base_used_bytes FROM limited_access_sessions WHERE id = $1::uuid`, sessionID).Scan(&base); err != nil || base != 100*gigabyte {
		t.Fatalf("base = %d, %v", base, err)
	}

	// A reconcile while the period lasts pushes the period, not the plan.
	if _, err = harness.pool.Exec(ctx, `UPDATE entitlements SET reconciled_at = NULL`); err != nil {
		t.Fatalf("make it due: %v", err)
	}
	if err = scheduler.Schedule(ctx); err != nil {
		t.Fatalf("schedule: %v", err)
	}
	harness.runOperation(ctx, t, worker, harness.latestKey(ctx, t, "reconcile:%"))
	panel.mu.Lock()
	user = panel.users[remoteID]
	panel.mu.Unlock()
	if user.TrafficLimitBytes != 101*gigabyte || len(user.ActiveInternalSquads) != 1 {
		t.Fatalf("a reconcile undid the period: %+v", user)
	}

	// Buying traffic restores the full plan.
	order, err := store.CreateAddonOrder(ctx, commercepg.AddonOrderInput{
		CustomerID: customerID, SubscriptionID: uuidText(bought.SubscriptionID), Currency: "RUB", IdempotencyKey: "limited-pack-buy",
		Addons: []commercepg.AddonSelection{{AddonVersionID: pack, Quantity: 1}},
	})
	if err != nil {
		t.Fatalf("buy traffic: %v", err)
	}
	var status string
	if err = harness.pool.QueryRow(ctx, `SELECT status FROM limited_access_sessions WHERE id = $1::uuid`, sessionID).Scan(&status); err != nil || status != "restored" {
		t.Fatalf("session status = %s, %v", status, err)
	}
	harness.runOperation(ctx, t, worker, "limited:"+sessionID+":restore")
	harness.runOperation(ctx, t, worker, "order:"+uuidText(order.ID)+":addon")
	panel.mu.Lock()
	user = panel.users[remoteID]
	panel.mu.Unlock()
	if user.TrafficLimitBytes != 110*gigabyte || len(user.ActiveInternalSquads) != 0 {
		t.Fatalf("restored user = %+v", user)
	}
}

// latestKey is the idempotency key of the newest operation matching a pattern.
func (harness *harness) latestKey(ctx context.Context, t *testing.T, pattern string) string {
	t.Helper()
	var key string
	if err := harness.pool.QueryRow(ctx, `SELECT idempotency_key FROM fulfillment_operations WHERE idempotency_key LIKE $1 ORDER BY created_at DESC LIMIT 1`, pattern).Scan(&key); err != nil {
		t.Fatalf("find %s: %v", pattern, err)
	}
	return key
}
