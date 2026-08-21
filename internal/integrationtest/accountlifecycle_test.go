//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"testing"

	"github.com/omniflow/omniflow/internal/accountcheckout"
	"github.com/omniflow/omniflow/internal/accountpg"
	"github.com/omniflow/omniflow/internal/commercepg"
)

// seedSubscriptionEntitlement attaches an entitlement in the given status to a
// subscription, paid for by a settled order, ending at the given interval.
func (harness *harness) seedSubscriptionEntitlement(
	ctx context.Context, t *testing.T, customerID, subscriptionID, planVersionID, status, ends string,
) {
	t.Helper()
	var order string
	if err := harness.pool.QueryRow(ctx, `INSERT INTO orders (
			user_id, state, operation, currency, subtotal_minor, external_minor,
			paid_minor, idempotency_key, paid_at, subscription_id
		) VALUES ($1::uuid, 'paid', 'purchase', 'RUB', 1000, 1000, 1000, $2, now(), $3::uuid)
		RETURNING id::text`, customerID, "seed-"+subscriptionID+status, subscriptionID).Scan(&order); err != nil {
		t.Fatalf("seed order: %v", err)
	}
	if _, err := harness.pool.Exec(ctx, `INSERT INTO entitlements (
			user_id, order_id, plan_version_id, subscription_id, status, starts_at, ends_at, remnawave_user_id
		) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5, now() - interval '40 days', now() + $6::interval, 4242)`,
		customerID, order, planVersionID, subscriptionID, status, ends); err != nil {
		t.Fatalf("seed entitlement: %v", err)
	}
}

func (harness *harness) seedSubscription(ctx context.Context, t *testing.T, customerID string, slot int) string {
	t.Helper()
	var id string
	if err := harness.pool.QueryRow(ctx,
		`INSERT INTO subscriptions (user_id, slot, label) VALUES ($1::uuid, $2, 'Subscription')
		 RETURNING id::text`, customerID, slot).Scan(&id); err != nil {
		t.Fatalf("create subscription: %v", err)
	}
	return id
}

// An unpaid purchase leaves a subscription row with no entitlement. The
// dashboard must show it as waiting for its order, not as "not active"; the
// catalogue must offer a purchase to fill it, not an extension of nothing.
func TestAnUnpaidPurchaseIsReportedAsPaymentPendingAndStillPurchasable(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	store := commercepg.New(harness.pool, nil, testOptions())
	service := newAccountCheckout(t, harness, store)
	account, err := accountpg.New(harness.pool, nil, accountpg.Options{Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatalf("build account service: %v", err)
	}
	customerID := harness.customer(ctx, t)
	planVersionID := harness.catalog(ctx, t, "ghost-plan", 34900)

	// Confirm a checkout and walk away: the order is pending, the subscription
	// exists, nothing is provisioned.
	session := openSession(ctx, t, service, customerID, planVersionID, "purchase")
	orderID, err := service.Confirm(ctx, session)
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}

	overview, err := account.Overview(ctx, customerID, "en")
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	if len(overview.Subscriptions) != 1 {
		t.Fatalf("%d subscriptions on the dashboard, want 1", len(overview.Subscriptions))
	}
	ghost := overview.Subscriptions[0]
	if ghost.PendingOrderID != orderID {
		t.Fatalf("pending order = %q, want %s", ghost.PendingOrderID, orderID)
	}
	if ghost.Provisioned {
		t.Fatal("an unpaid subscription reports as provisioned")
	}

	plans, err := service.Plans(ctx, customerID, "en")
	if err != nil {
		t.Fatalf("plans: %v", err)
	}
	for _, plan := range plans {
		if !slices.Contains(plan.Operations, "purchase") || slices.Contains(plan.Operations, "extension") {
			t.Fatalf("catalogue offers %v for %s with only an unpaid slot; want purchase and no extension",
				plan.Operations, plan.Name)
		}
	}
}

// The one lifecycle rule, against the database: an expired entitlement is not
// held, a live one is extended or moved from, and a purchase never lands on a
// subscription with time left.
func TestCatalogueAndOpenFollowTheHeldEntitlement(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	store := commercepg.New(harness.pool, nil, testOptions())
	service := newAccountCheckout(t, harness, store)
	basic := harness.catalog(ctx, t, "basic", 30000)
	twin := harness.catalog(ctx, t, "twin", 30000)
	pro := harness.catalog(ctx, t, "pro", 60000)

	operationsFor := func(customerID, planVersionID string) []string {
		t.Helper()
		plans, err := service.Plans(ctx, customerID, "en")
		if err != nil {
			t.Fatalf("plans: %v", err)
		}
		for _, plan := range plans {
			if plan.PlanVersionID == planVersionID {
				return plan.Operations
			}
		}
		t.Fatalf("plan %s is not in the catalogue", planVersionID)
		return nil
	}

	// Lapsed: the entitlement expired. Every plan, including the one they had,
	// is a purchase again.
	lapsed := harness.customer(ctx, t)
	lapsedSubscription := harness.seedSubscription(ctx, t, lapsed, 1)
	harness.seedSubscriptionEntitlement(ctx, t, lapsed, lapsedSubscription, basic, "expired", "-1 day")
	for _, plan := range []string{basic, twin, pro} {
		if got := operationsFor(lapsed, plan); !slices.Equal(got, []string{"purchase"}) {
			t.Fatalf("lapsed customer is offered %v for a plan, want purchase only", got)
		}
	}
	// And the purchase fills the lapsed slot rather than being refused.
	view, err := service.Open(ctx, lapsed, "en", accountcheckout.OpenRequest{PlanVersionID: basic, Operation: "purchase"})
	if err != nil {
		t.Fatalf("purchase after lapse: %v", err)
	}
	if view.SubscriptionID != lapsedSubscription {
		t.Fatalf("the purchase targeted %q, want the lapsed slot %s", view.SubscriptionID, lapsedSubscription)
	}

	// Live: the held plan extends, an equal price upgrades, a dearer one
	// upgrades, and a purchase aimed at the live subscription is refused.
	live := harness.customer(ctx, t)
	liveSubscription := harness.seedSubscription(ctx, t, live, 1)
	harness.seedSubscriptionEntitlement(ctx, t, live, liveSubscription, basic, "active", "20 days")
	if got := operationsFor(live, basic); !slices.Contains(got, "extension") {
		t.Fatalf("the held plan offers %v, want extension", got)
	}
	if got := operationsFor(live, twin); !slices.Contains(got, "upgrade") {
		t.Fatalf("an equally priced plan offers %v, want upgrade", got)
	}
	if got := operationsFor(live, pro); !slices.Contains(got, "upgrade") {
		t.Fatalf("a dearer plan offers %v, want upgrade", got)
	}
	_, err = service.Open(ctx, live, "en", accountcheckout.OpenRequest{
		PlanVersionID: pro, Operation: "purchase", SubscriptionID: liveSubscription,
	})
	if !errors.Is(err, accountcheckout.ErrPurchaseOnLiveSubscription) {
		t.Fatalf("a purchase onto a live subscription was accepted: %v", err)
	}
	// With concurrent subscriptions enabled, an unaddressed purchase opens a
	// new slot rather than being guessed onto the live one.
	view, err = service.Open(ctx, live, "en", accountcheckout.OpenRequest{PlanVersionID: pro, Operation: "purchase"})
	if err != nil {
		t.Fatalf("purchase beside a live subscription: %v", err)
	}
	if view.SubscriptionID != "" {
		t.Fatalf("an unaddressed purchase was aimed at %q", view.SubscriptionID)
	}
}
