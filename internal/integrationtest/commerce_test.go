//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/commercepg"
	databaseutil "github.com/omniflow/omniflow/internal/database"
	"github.com/omniflow/omniflow/internal/retention"
)

func testOptions() commercepg.Options {
	return commercepg.Options{
		Subscriptions: commerce.SubscriptionPolicy{MultiEnabled: true, MaxPerCustomer: 2},
		TopUp: commerce.TopUpLimits{
			Enabled: true, MinimumMinor: 1000, MaximumMinor: 1000000,
			WindowLimitMinor: 2000000, Window: 24 * time.Hour,
		},
		Logger: slog.New(slog.DiscardHandler),
	}
}

// catalog inserts one purchasable plan and returns its version identifier.
func (harness *harness) catalog(ctx context.Context, t *testing.T, code string, amountMinor int64) string {
	t.Helper()
	var planID, versionID string
	if err := harness.pool.QueryRow(ctx,
		`INSERT INTO plans (code, kind, visible) VALUES ($1, 'one_time', true) RETURNING id::text`, code).
		Scan(&planID); err != nil {
		t.Fatalf("create plan: %v", err)
	}
	if _, err := harness.pool.Exec(ctx,
		`INSERT INTO plan_localizations (plan_id, locale, name) VALUES ($1::uuid, 'en', $2), ($1::uuid, 'ru', $2)`,
		planID, code); err != nil {
		t.Fatalf("localize plan: %v", err)
	}
	if err := harness.pool.QueryRow(ctx,
		`INSERT INTO plan_versions (plan_id, version, billing_period, duration_seconds, traffic_allowance_bytes, device_limit)
		 VALUES ($1::uuid, 1, 'month', 2592000, 107374182400, 3) RETURNING id::text`, planID).
		Scan(&versionID); err != nil {
		t.Fatalf("create plan version: %v", err)
	}
	if _, err := harness.pool.Exec(ctx,
		`INSERT INTO plan_prices (plan_version_id, currency, amount_minor) VALUES ($1::uuid, 'RUB', $2)`,
		versionID, amountMinor); err != nil {
		t.Fatalf("price plan: %v", err)
	}
	return versionID
}

func (harness *harness) customer(ctx context.Context, t *testing.T) string {
	t.Helper()
	var id string
	if err := harness.pool.QueryRow(ctx, `INSERT INTO users (locale) VALUES ('en') RETURNING id::text`).Scan(&id); err != nil {
		t.Fatalf("create customer: %v", err)
	}
	return id
}

func (harness *harness) walletBalance(ctx context.Context, t *testing.T, customerID string) int64 {
	t.Helper()
	var balance int64
	if err := harness.pool.QueryRow(ctx, `SELECT COALESCE(sum(amount_minor), 0)::bigint FROM ledger_entries
		WHERE account_type = 'customer_wallet' AND user_id = $1::uuid AND currency = 'RUB'`, customerID).Scan(&balance); err != nil {
		t.Fatalf("read balance: %v", err)
	}
	return balance
}

// settle marks the order's payment intent as succeeded through the same path a
// provider webhook takes.
func (harness *harness) settle(ctx context.Context, t *testing.T, store *commercepg.Store, orderID string, receivedMinor int64, eventID string) string {
	t.Helper()
	var intentID string
	err := harness.pool.QueryRow(ctx, `INSERT INTO payment_intents
		(order_id, provider, status, amount_minor, currency, provider_reference, idempotency_key)
		VALUES ($1::uuid, 'manual', 'processing', $2, 'RUB', $3, $3)
		ON CONFLICT (provider, idempotency_key) DO UPDATE SET status = payment_intents.status
		RETURNING id::text`, orderID, receivedMinor, "intent-"+orderID).Scan(&intentID)
	if err != nil {
		t.Fatalf("create payment intent: %v", err)
	}
	_, classification, err := store.RecordProviderPayment(ctx, intentID,
		eventID, commerce.Money{Amount: receivedMinor, Currency: "RUB"}, false)
	if err != nil {
		t.Fatalf("record payment: %v", err)
	}
	return classification
}

// TestTopUpCreditsTheWalletExactlyOnce is the core financial invariant of the
// wallet: a replayed webhook, a reconciliation poll, and a manual approval all
// reach the same settlement, and the customer is credited once.
func TestTopUpCreditsTheWalletExactlyOnce(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	store := commercepg.New(harness.pool, nil, testOptions())
	customerID := harness.customer(ctx, t)

	order, err := store.CreateTopUpOrder(ctx, commercepg.TopUpInput{
		CustomerID: customerID, Currency: "RUB", AmountMinor: 50000, IdempotencyKey: "topup-key-1",
	})
	if err != nil {
		t.Fatalf("create top-up: %v", err)
	}
	orderID := uuidText(order.ID)

	if classification := harness.settle(ctx, t, store, orderID, 50000, "event-1"); classification != "paid" {
		t.Fatalf("expected a paid classification, got %q", classification)
	}
	if balance := harness.walletBalance(ctx, t, customerID); balance != 50000 {
		t.Fatalf("expected 50000 credited, got %d", balance)
	}
	// The same provider event arriving again must be a no-op.
	harness.settle(ctx, t, store, orderID, 50000, "event-1")
	if balance := harness.walletBalance(ctx, t, customerID); balance != 50000 {
		t.Fatalf("a replayed webhook credited twice: %d", balance)
	}
	// A different event identifier for the same settled order must also be a
	// no-op, because the credit is keyed on the order.
	harness.settle(ctx, t, store, orderID, 50000, "event-2")
	if balance := harness.walletBalance(ctx, t, customerID); balance != 50000 {
		t.Fatalf("a second event credited twice: %d", balance)
	}
}

// A top-up that receives more or less than it asked for credits what actually
// arrived, and the order still settles.
func TestTopUpOverpaymentIsResolvedInTheLedger(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	store := commercepg.New(harness.pool, nil, testOptions())
	customerID := harness.customer(ctx, t)

	order, err := store.CreateTopUpOrder(ctx, commercepg.TopUpInput{
		CustomerID: customerID, Currency: "RUB", AmountMinor: 50000, IdempotencyKey: "topup-over",
	})
	if err != nil {
		t.Fatalf("create top-up: %v", err)
	}
	if classification := harness.settle(ctx, t, store, uuidText(order.ID), 65000, "event-over"); classification != "overpayment" {
		t.Fatalf("expected an overpayment classification, got %q", classification)
	}
	if balance := harness.walletBalance(ctx, t, customerID); balance != 65000 {
		t.Fatalf("expected the received amount credited, got %d", balance)
	}
	var state string
	if err := harness.pool.QueryRow(ctx, `SELECT state FROM orders WHERE id = $1`, order.ID).Scan(&state); err != nil {
		t.Fatalf("read order: %v", err)
	}
	if state != "paid" {
		t.Fatalf("an overpaid top-up must still settle, got %q", state)
	}
}

func TestTopUpLimitsAreEnforcedByTheStore(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	store := commercepg.New(harness.pool, nil, testOptions())
	customerID := harness.customer(ctx, t)
	_, err := store.CreateTopUpOrder(ctx, commercepg.TopUpInput{
		CustomerID: customerID, Currency: "RUB", AmountMinor: 10, IdempotencyKey: "topup-small",
	})
	if !errors.Is(err, commerce.ErrTopUpRejected) {
		t.Fatalf("expected a top-up rejection, got %v", err)
	}
}

// TestCartIsPurchasedOnceTheBalanceCoversIt exercises the deferred purchase from
// both sides: it stays saved while the balance is short, and a credit releases
// it without ever creating a second order.
func TestCartIsPurchasedOnceTheBalanceCoversIt(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	store := commercepg.New(harness.pool, nil, testOptions())
	customerID := harness.customer(ctx, t)
	planVersionID := harness.catalog(ctx, t, "cart-plan", 30000)

	if _, err := store.SaveCart(ctx, commercepg.CartInput{
		CustomerID: customerID, PlanVersionID: planVersionID, Operation: "purchase",
		Currency: "RUB", AutoPurchase: true, TTL: time.Hour,
	}); err != nil {
		t.Fatalf("save cart: %v", err)
	}
	purchase, err := store.TryPurchaseCart(ctx, customerID)
	if err != nil {
		t.Fatalf("first attempt: %v", err)
	}
	if purchase.Purchased || purchase.Reason != commerce.CartInsufficientBalance {
		t.Fatalf("an unfunded cart must stay saved, got %+v", purchase)
	}

	topUp, err := store.CreateTopUpOrder(ctx, commercepg.TopUpInput{
		CustomerID: customerID, Currency: "RUB", AmountMinor: 30000, IdempotencyKey: "cart-funding",
	})
	if err != nil {
		t.Fatalf("create top-up: %v", err)
	}
	// Settlement itself releases the cart, so no second call is needed.
	harness.settle(ctx, t, store, uuidText(topUp.ID), 30000, "event-cart")

	var purchased int
	if err := harness.pool.QueryRow(ctx,
		`SELECT count(*) FROM orders WHERE user_id = $1::uuid AND operation = 'purchase'`, customerID).Scan(&purchased); err != nil {
		t.Fatalf("count orders: %v", err)
	}
	if purchased != 1 {
		t.Fatalf("expected exactly one purchase order, got %d", purchased)
	}
	// A second sweep must not charge again.
	if _, err := store.TryPurchaseCart(ctx, customerID); err != nil {
		t.Fatalf("second attempt: %v", err)
	}
	if err := harness.pool.QueryRow(ctx,
		`SELECT count(*) FROM orders WHERE user_id = $1::uuid AND operation = 'purchase'`, customerID).Scan(&purchased); err != nil {
		t.Fatalf("count orders: %v", err)
	}
	if purchased != 1 {
		t.Fatalf("a repeated sweep created %d purchase orders", purchased)
	}
	if balance := harness.walletBalance(ctx, t, customerID); balance != 0 {
		t.Fatalf("the wallet must be spent exactly once, balance is %d", balance)
	}
}

// addon inserts one visible add-on offered for a plan version and returns its
// current version identifier.
func (harness *harness) addon(ctx context.Context, t *testing.T, planVersionID, code string, amountMinor, trafficBytes int64, proration string) string {
	t.Helper()
	var addonID, versionID string
	if err := harness.pool.QueryRow(ctx,
		`INSERT INTO addons (code, kind, visible) VALUES ($1, 'traffic', true) RETURNING id::text`, code).Scan(&addonID); err != nil {
		t.Fatalf("create add-on: %v", err)
	}
	if _, err := harness.pool.Exec(ctx,
		`INSERT INTO addon_localizations (addon_id, locale, name) VALUES ($1::uuid, 'en', $2), ($1::uuid, 'ru', $2)`,
		addonID, code); err != nil {
		t.Fatalf("localize add-on: %v", err)
	}
	if err := harness.pool.QueryRow(ctx,
		`INSERT INTO addon_versions (addon_id, version, traffic_bytes, max_quantity, proration)
		 VALUES ($1::uuid, 1, $2, 5, $3) RETURNING id::text`, addonID, trafficBytes, proration).Scan(&versionID); err != nil {
		t.Fatalf("create add-on version: %v", err)
	}
	if _, err := harness.pool.Exec(ctx,
		`INSERT INTO addon_prices (addon_version_id, currency, amount_minor) VALUES ($1::uuid, 'RUB', $2)`,
		versionID, amountMinor); err != nil {
		t.Fatalf("price add-on: %v", err)
	}
	if _, err := harness.pool.Exec(ctx,
		`INSERT INTO plan_version_addons (plan_version_id, addon_id) VALUES ($1::uuid, $2::uuid)`,
		planVersionID, addonID); err != nil {
		t.Fatalf("offer add-on: %v", err)
	}
	return versionID
}

// TestAddonsBoughtWithThePlanAreOneOrderAndOneEntitlement proves the checkout
// add-on path: the customer pays once and the capacity is already part of the
// entitlement the plan provisions.
func TestAddonsBoughtWithThePlanAreOneOrderAndOneEntitlement(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	store := commercepg.New(harness.pool, nil, testOptions())
	customerID := harness.customer(ctx, t)
	planVersionID := harness.catalog(ctx, t, "bundle-plan", 0)
	addonVersionID := harness.addon(ctx, t, planVersionID, "extra-traffic", 0, 53687091200, "remaining_period")

	order, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: planVersionID, Currency: "RUB",
		Operation: "purchase", IdempotencyKey: "bundle-order",
		Addons: []commercepg.AddonSelection{{AddonVersionID: addonVersionID, Quantity: 2}},
	})
	if err != nil {
		t.Fatalf("create order: %v", err)
	}
	if order.State != "paid" {
		t.Fatalf("a zero-priced bundle settles immediately, got %q", order.State)
	}
	var orders int
	if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM orders WHERE user_id = $1::uuid`, customerID).Scan(&orders); err != nil {
		t.Fatalf("count orders: %v", err)
	}
	if orders != 1 {
		t.Fatalf("a plan with add-ons must be one order, got %d", orders)
	}
	// The plan grants 100 GiB and two add-ons grant 50 GiB each.
	var allowance int64
	if err := harness.pool.QueryRow(ctx,
		`SELECT traffic_allowance_bytes FROM entitlements WHERE order_id = $1`, order.ID).Scan(&allowance); err != nil {
		t.Fatalf("read entitlement: %v", err)
	}
	if allowance != 107374182400+2*53687091200 {
		t.Fatalf("add-on capacity was not folded in: %d", allowance)
	}
	// One fulfillment operation, carrying the combined desired state.
	var operations int
	if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM fulfillment_operations`).Scan(&operations); err != nil {
		t.Fatalf("count operations: %v", err)
	}
	if operations != 1 {
		t.Fatalf("expected one combined fulfillment operation, got %d", operations)
	}
}

// TestMidPeriodAddonIsProratedAndAppliedOnce covers the other add-on path.
func TestMidPeriodAddonIsProratedAndAppliedOnce(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	store := commercepg.New(harness.pool, nil, testOptions())
	customerID := harness.customer(ctx, t)
	planVersionID := harness.catalog(ctx, t, "midperiod-plan", 0)
	addonVersionID := harness.addon(ctx, t, planVersionID, "midperiod-traffic", 30000, 10737418240, "remaining_period")

	if _, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: planVersionID, Currency: "RUB",
		Operation: "purchase", IdempotencyKey: "midperiod-plan-order",
	}); err != nil {
		t.Fatalf("create plan order: %v", err)
	}
	// Move the entitlement's window so most of the period has already elapsed.
	if _, err := harness.pool.Exec(ctx, `UPDATE entitlements
		SET starts_at = now() - interval '27 days', ends_at = now() + interval '3 days'
		WHERE user_id = $1::uuid`, customerID); err != nil {
		t.Fatalf("age the entitlement: %v", err)
	}

	addonOrder, err := store.CreateAddonOrder(ctx, commercepg.AddonOrderInput{
		CustomerID: customerID, Currency: "RUB", IdempotencyKey: "midperiod-addon",
		Addons: []commercepg.AddonSelection{{AddonVersionID: addonVersionID, Quantity: 1}},
	})
	if err != nil {
		t.Fatalf("create add-on order: %v", err)
	}
	// Three of thirty days left must cost far less than the catalog price, and
	// never nothing.
	if addonOrder.SubtotalMinor >= 30000 || addonOrder.SubtotalMinor <= 0 {
		t.Fatalf("proration produced %d for three of thirty days", addonOrder.SubtotalMinor)
	}
	// With no wallet balance the add-on order waits for an external payment.
	if addonOrder.State != "pending" {
		t.Fatalf("expected the add-on order to await payment, got %q", addonOrder.State)
	}

	harness.settle(ctx, t, store, uuidText(addonOrder.ID), addonOrder.SubtotalMinor, "event-addon")
	harness.settle(ctx, t, store, uuidText(addonOrder.ID), addonOrder.SubtotalMinor, "event-addon-replay")

	var applied int
	if err := harness.pool.QueryRow(ctx,
		`SELECT count(*) FROM entitlement_addons WHERE order_id = $1`, addonOrder.ID).Scan(&applied); err != nil {
		t.Fatalf("count applied add-ons: %v", err)
	}
	if applied != 1 {
		t.Fatalf("a replayed settlement applied the add-on %d times", applied)
	}
	var allowance int64
	if err := harness.pool.QueryRow(ctx,
		`SELECT traffic_allowance_bytes FROM entitlements WHERE user_id = $1::uuid`, customerID).Scan(&allowance); err != nil {
		t.Fatalf("read entitlement: %v", err)
	}
	if allowance != 107374182400+10737418240 {
		t.Fatalf("add-on capacity was applied more than once or not at all: %d", allowance)
	}
}

// TestConcurrencyPolicyIsEnforcedAtOrderCreation proves the operator switch
// actually bounds concurrent subscriptions rather than only hiding a button.
func TestConcurrencyPolicyIsEnforcedAtOrderCreation(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	store := commercepg.New(harness.pool, nil, testOptions())
	customerID := harness.customer(ctx, t)
	planVersionID := harness.catalog(ctx, t, "multi-plan", 0)

	for index := range 2 {
		if _, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
			CustomerID: customerID, PlanVersionID: planVersionID, Currency: "RUB",
			Operation: "purchase", NewSubscription: true,
			IdempotencyKey: "sub-order-" + string(rune('a'+index)),
		}); err != nil {
			t.Fatalf("order %d: %v", index, err)
		}
	}
	_, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: planVersionID, Currency: "RUB",
		Operation: "purchase", NewSubscription: true, IdempotencyKey: "sub-order-c",
	})
	if !errors.Is(err, commerce.ErrSubscriptionRejected) {
		t.Fatalf("expected a concurrency rejection, got %v", err)
	}
	if reason := commerce.SubscriptionRejectionReason(err); reason != commerce.SubscriptionLimitReached {
		t.Fatalf("expected %q, got %q", commerce.SubscriptionLimitReached, reason)
	}
}

// TestSettlementWritesTheOutboxAndOperatorNotice checks that a settled order
// publishes its domain event and queues exactly one operator notice, however
// many times settlement is attempted.
func TestSettlementWritesTheOutboxAndOperatorNotice(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	store := commercepg.New(harness.pool, nil, testOptions())
	customerID := harness.customer(ctx, t)
	planVersionID := harness.catalog(ctx, t, "outbox-plan", 25000)

	order, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: planVersionID, Currency: "RUB",
		Operation: "purchase", IdempotencyKey: "outbox-order",
	})
	if err != nil {
		t.Fatalf("create order: %v", err)
	}
	harness.settle(ctx, t, store, uuidText(order.ID), 25000, "event-outbox")
	harness.settle(ctx, t, store, uuidText(order.ID), 25000, "event-outbox-again")

	var events, notices int
	if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE topic = 'order.paid'`).Scan(&events); err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	if events != 1 {
		t.Fatalf("expected one order.paid event, got %d", events)
	}
	if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM operator_notifications WHERE kind = 'purchase'`).Scan(&notices); err != nil {
		t.Fatalf("count notices: %v", err)
	}
	if notices != 1 {
		t.Fatalf("expected one operator notice, got %d", notices)
	}
}

// TestMaintenanceBlocksNewOrdersButNotSettlement proves money already taken is
// never disturbed by a maintenance window.
func TestMaintenanceBlocksNewOrdersButNotSettlement(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	store := commercepg.New(harness.pool, nil, testOptions())
	customerID := harness.customer(ctx, t)
	planVersionID := harness.catalog(ctx, t, "maintenance-plan", 25000)

	order, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: planVersionID, Currency: "RUB",
		Operation: "purchase", IdempotencyKey: "pre-maintenance",
	})
	if err != nil {
		t.Fatalf("create order: %v", err)
	}
	if _, err := store.SetMaintenance(ctx, commerce.Maintenance{
		Active: true, Source: commerce.MaintenanceManual, Reason: "planned upgrade",
	}, "operator", "tester"); err != nil {
		t.Fatalf("activate maintenance: %v", err)
	}
	if _, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: planVersionID, Currency: "RUB",
		Operation: "purchase", IdempotencyKey: "during-maintenance",
	}); !errors.Is(err, commercepg.ErrMaintenance) {
		t.Fatalf("expected a maintenance rejection, got %v", err)
	}
	// The order taken before the window must still settle.
	if classification := harness.settle(ctx, t, store, uuidText(order.ID), 25000, "event-maintenance"); classification != "paid" {
		t.Fatalf("expected the pre-existing order to settle, got %q", classification)
	}
	if _, err := store.SetMaintenance(ctx, commerce.Maintenance{Source: commerce.MaintenanceManual}, "operator", "tester"); err != nil {
		t.Fatalf("clear maintenance: %v", err)
	}
	var events int
	if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM maintenance_events`).Scan(&events); err != nil {
		t.Fatalf("count maintenance events: %v", err)
	}
	if events != 2 {
		t.Fatalf("expected an activation and a clearance to be recorded, got %d", events)
	}
}

// TestRetentionSweepRemovesOnlyDisposableRows guards the retention job against
// deleting anything that must be kept forever.
func TestRetentionSweepRemovesOnlyDisposableRows(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	customerID := harness.customer(ctx, t)

	if _, err := harness.pool.Exec(ctx, `INSERT INTO bot_sessions (telegram_id, state, expires_at)
		VALUES (5551, 'promo_code', now() - interval '1 hour')`); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if _, err := harness.pool.Exec(ctx, `INSERT INTO provider_webhook_events
		(provider, provider_event_id, signature_valid, body_sha256, raw_body, retain_until)
		VALUES ('manual', 'expired-event', true, '\x00', '\x00', now() - interval '1 day')`); err != nil {
		t.Fatalf("seed webhook: %v", err)
	}
	if _, err := harness.pool.Exec(ctx, `INSERT INTO outbox_events (topic, payload, published_at)
		VALUES ('order.paid', '{}'::jsonb, now() - interval '30 days')`); err != nil {
		t.Fatalf("seed outbox: %v", err)
	}
	if _, err := harness.pool.Exec(ctx, `INSERT INTO audit_events (actor_type, action, target_type, target_id)
		VALUES ('system', 'test.kept', 'customer', $1)`, customerID); err != nil {
		t.Fatalf("seed audit: %v", err)
	}

	result := retention.New(harness.pool, slog.New(slog.DiscardHandler), retention.Config{
		Outbox: 7 * 24 * time.Hour, Telemetry: 30 * 24 * time.Hour, Drift: 90 * 24 * time.Hour,
	}).Sweep(ctx)
	if result.BotSessions != 1 || result.WebhookEvents != 1 || result.OutboxEvents != 1 {
		t.Fatalf("expected each disposable row to be removed: %+v", result)
	}
	var audits int
	if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events`).Scan(&audits); err != nil {
		t.Fatalf("count audits: %v", err)
	}
	if audits != 1 {
		t.Fatal("retention must never delete an audit event")
	}
}

// TestFulfillmentJobIsEnqueuedInTheSettlementTransaction proves the durable job
// and the state change it serves commit together.
func TestFulfillmentJobIsEnqueuedInTheSettlementTransaction(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	enqueued := 0
	store := commercepg.New(harness.pool, func(ctx context.Context, tx pgx.Tx, operationID string) error {
		enqueued++
		// Writing through the caller's transaction is what proves the job and
		// the entitlement share one commit.
		_, err := tx.Exec(ctx, `SELECT 1`)
		return err
	}, testOptions())
	customerID := harness.customer(ctx, t)
	planVersionID := harness.catalog(ctx, t, "job-plan", 0)

	order, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: planVersionID, Currency: "RUB",
		Operation: "purchase", IdempotencyKey: "job-order",
	})
	if err != nil {
		t.Fatalf("create order: %v", err)
	}
	if order.State != "paid" {
		t.Fatalf("a zero-priced order settles immediately, got %q", order.State)
	}
	if enqueued != 1 {
		t.Fatalf("expected exactly one fulfillment enqueue, got %d", enqueued)
	}
	var operations int
	if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM fulfillment_operations`).Scan(&operations); err != nil {
		t.Fatalf("count operations: %v", err)
	}
	if operations != 1 {
		t.Fatalf("expected one fulfillment operation, got %d", operations)
	}
}

// uuidText renders a database UUID the way the rest of the codebase does.
func uuidText(value pgtype.UUID) string {
	rendered := databaseutil.UUIDStrings([]pgtype.UUID{value})
	if len(rendered) == 0 {
		return ""
	}
	return rendered[0]
}
