//go:build integration

package integrationtest

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/panelpg"
)

// savePolicy writes the installation's plan-change switches through the panel.
func (harness *harness) savePolicy(ctx context.Context, t *testing.T, mutate func(*panelpg.SubscriptionPolicy)) {
	t.Helper()
	panel := newOperations(t, harness)
	policy, err := panel.SubscriptionPolicy(ctx)
	if err != nil {
		t.Fatalf("read policy: %v", err)
	}
	mutate(&policy)
	if _, err = panel.SaveSubscriptionPolicy(ctx, policy, harness.operator(ctx, t, "policy-"+time.Now().Format("150405.000000000")+"@example.test")); err != nil {
		t.Fatalf("save policy: %v", err)
	}
}

// desiredFor reads the desired state of an order's provisioning operation.
func (harness *harness) desiredFor(ctx context.Context, t *testing.T, orderID string) map[string]any {
	t.Helper()
	var raw []byte
	if err := harness.pool.QueryRow(ctx, `SELECT o.desired_state FROM fulfillment_operations o
		JOIN entitlements e ON e.id = o.entitlement_id WHERE e.order_id = $1::uuid AND o.operation = 'create'`, orderID).Scan(&raw); err != nil {
		t.Fatalf("read desired state: %v", err)
	}
	desired := map[string]any{}
	if err := json.Unmarshal(raw, &desired); err != nil {
		t.Fatalf("decode desired state: %v", err)
	}
	return desired
}

// The installation can switch upgrades off for the whole catalogue, and the
// refusal comes from the same rule that refuses a version's own `forbid`.
// What a renewal resets follows the switches too.
func TestTheInstallationPolicyGovernsChangesAndResets(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	customerID := harness.customer(ctx, t)
	basic := harness.catalog(ctx, t, "policy-basic", 0)
	premium := harness.catalog(ctx, t, "policy-premium", 0)

	first, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: basic, Currency: "RUB",
		Operation: "purchase", IdempotencyKey: "policy-first",
	})
	if err != nil {
		t.Fatalf("buy the basic plan: %v", err)
	}
	harness.savePolicy(ctx, t, func(policy *panelpg.SubscriptionPolicy) {
		policy.UpgradesEnabled = false
		policy.ResetTrafficOnRenewal = false
		policy.ResetDevicesOnRenewal = true
	})
	if _, err = store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: premium, Currency: "RUB",
		Operation: "upgrade", IdempotencyKey: "policy-upgrade", SubscriptionID: uuidText(first.SubscriptionID),
	}); !errors.Is(err, commercepg.ErrOperationForbidden) {
		t.Fatalf("an upgrade with upgrades switched off: %v, want operation_forbidden", err)
	}
	renewal, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: basic, Currency: "RUB",
		Operation: "extension", IdempotencyKey: "policy-extension", SubscriptionID: uuidText(first.SubscriptionID),
	})
	if err != nil {
		t.Fatalf("extend: %v", err)
	}
	desired := harness.desiredFor(ctx, t, uuidText(renewal.ID))
	if desired["resetTraffic"] == true || desired["resetDevices"] != true {
		t.Fatalf("an extension under the saved policy carried %v", desired)
	}
}

// A trial's remaining time is carried onto the paid plan when the
// installation says so, whatever the paid version's own upgrade policy is.
func TestATrialsRemainderIsCarriedOrDroppedOnAMoveToAPaidPlan(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []string{"carry", "reset"} {
		t.Run(mode, func(t *testing.T) {
			harness := newHarness(t)
			store := harness.commerceStore(t, testOptions())
			customerID := harness.customer(ctx, t)
			trial := harness.catalog(ctx, t, "policy-trial", 0)
			paid := harness.catalog(ctx, t, "policy-paid", 0)
			if _, err := harness.pool.Exec(ctx, `UPDATE plans SET kind = 'trial' WHERE id = (SELECT plan_id FROM plan_versions WHERE id = $1::uuid)`, trial); err != nil {
				t.Fatalf("make a trial: %v", err)
			}
			if _, err := harness.pool.Exec(ctx, `UPDATE plan_versions SET upgrade_policy = 'replace' WHERE id = $1::uuid`, paid); err != nil {
				t.Fatalf("set the paid version's policy: %v", err)
			}
			if _, err := harness.pool.Exec(ctx, `UPDATE plan_prices SET amount_minor = 100 WHERE plan_version_id = $1::uuid`, paid); err != nil {
				t.Fatalf("price the paid plan: %v", err)
			}
			harness.seedWallet(ctx, t, customerID, "trial-move-wallet", 100)
			started, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
				CustomerID: customerID, PlanVersionID: trial, Currency: "RUB",
				Operation: "purchase", IdempotencyKey: "trial-start",
			})
			if err != nil {
				t.Fatalf("start the trial: %v", err)
			}
			var trialEnds time.Time
			if err = harness.pool.QueryRow(ctx, `SELECT ends_at FROM entitlements WHERE order_id = $1`, started.ID).Scan(&trialEnds); err != nil {
				t.Fatalf("read the trial: %v", err)
			}
			harness.savePolicy(ctx, t, func(policy *panelpg.SubscriptionPolicy) { policy.TrialRemainder = mode })
			moved, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
				CustomerID: customerID, PlanVersionID: paid, Currency: "RUB",
				Operation: "upgrade", IdempotencyKey: "trial-move", SubscriptionID: uuidText(started.SubscriptionID),
			})
			if err != nil {
				t.Fatalf("move to the paid plan: %v", err)
			}
			var paidEnds time.Time
			if err = harness.pool.QueryRow(ctx, `SELECT ends_at FROM entitlements WHERE order_id = $1`, moved.ID).Scan(&paidEnds); err != nil {
				t.Fatalf("read the paid entitlement: %v", err)
			}
			period := 30 * 24 * time.Hour
			want := time.Now().Add(period)
			if mode == "carry" {
				want = trialEnds.Add(period)
			}
			if paidEnds.Sub(want).Abs() > time.Minute {
				t.Fatalf("%s: the paid period ends %v, want about %v", mode, paidEnds, want)
			}
		})
	}
}
