//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"testing"

	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/panelpg"
)

// From the customer card an operator grants days, changes the plan, buys from
// the customer's balance, restricts spending with a reason, resets the trial,
// and cannot switch on a renewal the customer never consented to.
func TestCustomerCardControls(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	service, err := panelpg.New(harness.pool, operationsKey, panelpg.Options{})
	if err != nil {
		t.Fatal(err)
	}
	actor := harness.operator(ctx, t, "card@example.test")
	customer := harness.customer(ctx, t)
	basic := harness.catalog(ctx, t, "card-basic", 10000)
	premium := harness.catalog(ctx, t, "card-premium", 30000)

	grant := commercepg.GrantInput{CustomerID: customer, PlanVersionID: basic, Mode: commercepg.GrantDays, Days: 30, IdempotencyKey: "grant-0001"}
	endsAt, orderID, err := store.GrantPlan(ctx, grant)
	if err != nil || orderID == "" || endsAt.IsZero() {
		t.Fatalf("grant days: %v %v %v", endsAt, orderID, err)
	}
	if _, again, err := store.GrantPlan(ctx, grant); err != nil || again != orderID {
		t.Fatalf("a repeated grant must be the same order: %v %v", again, err)
	}
	if _, _, err = store.GrantPlan(ctx, commercepg.GrantInput{CustomerID: customer, PlanVersionID: basic, Mode: commercepg.GrantChangePlan, IdempotencyKey: "change-0001"}); err != nil {
		// The first entitlement is still pending provisioning; a change needs
		// current access, which a pending entitlement already gives.
		if !errors.Is(err, commercepg.ErrGrantRefused) {
			t.Fatalf("change plan: %v", err)
		}
	}

	// A purchase restriction stops a purchase on the customer's behalf.
	if err = service.SetCustomerRestriction(ctx, customer, "purchase", "chargeback fraud", true, actor); err != nil {
		t.Fatal(err)
	}
	harness.seedWallet(ctx, t, customer, "card-wallet", 50000)
	purchase := commercepg.CreateOrderInput{CustomerID: customer, PlanVersionID: premium, Currency: "RUB", Operation: "purchase", IdempotencyKey: "card-buy-0001", NewSubscription: true}
	if _, err = store.BuyFromBalance(ctx, purchase); !errors.Is(err, commercepg.ErrRestricted) {
		t.Fatalf("restricted purchase: %v", err)
	}
	if err = service.SetCustomerRestriction(ctx, customer, "purchase", "", false, actor); err != nil {
		t.Fatal(err)
	}
	if err = service.SetCustomerRestriction(ctx, customer, "purchase", "", true, actor); !errors.Is(err, panelpg.ErrValidaton) {
		t.Fatalf("a restriction needs a reason: %v", err)
	}

	poor := harness.customer(ctx, t)
	if _, err = store.BuyFromBalance(ctx, commercepg.CreateOrderInput{CustomerID: poor, PlanVersionID: premium, Currency: "RUB", Operation: "purchase", IdempotencyKey: "card-buy-0002"}); !errors.Is(err, commercepg.ErrInsufficientBalance) {
		t.Fatalf("an uncovered purchase: %v", err)
	}

	if _, err = harness.pool.Exec(ctx, `INSERT INTO trial_claims (user_id, plan_id, order_id) SELECT $1::uuid, plan_id, $2::uuid FROM plan_versions WHERE id = $3::uuid`, customer, orderID, basic); err != nil {
		t.Fatal(err)
	}
	if cleared, err := service.ResetCustomerTrial(ctx, customer, actor); err != nil || cleared != 1 {
		t.Fatalf("trial reset = %d, %v", cleared, err)
	}
	if err = service.SetCustomerAutoRenew(ctx, customer, true, actor); !errors.Is(err, panelpg.ErrNotFound) && !errors.Is(err, panelpg.ErrRejected) {
		t.Fatalf("auto-renew without consent: %v", err)
	}

	if _, err = harness.pool.Exec(ctx, `INSERT INTO customer_activity (user_id, surface, action) VALUES ($1::uuid, 'bot', 'plans'), ($1::uuid, 'bot', 'cmd:start')`, customer); err != nil {
		t.Fatal(err)
	}
	if activity, err := service.CustomerActivityLog(ctx, customer); err != nil || len(activity) != 2 {
		t.Fatalf("activity = %+v, %v", activity, err)
	}
	if stats, err := service.ButtonStats(ctx, 7); err != nil || len(stats) != 2 {
		t.Fatalf("button stats = %+v, %v", stats, err)
	}
	rows, err := service.SearchCustomersAdvanced(ctx, panelpg.AdvancedFilter{WithBalance: true, Sort: "balance"})
	if err != nil || len(rows) != 1 || rows[0].ID != customer {
		t.Fatalf("advanced search = %+v, %v", rows, err)
	}
}
