//go:build integration

package integrationtest

import (
	"context"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/fulfillment"
)

// TestAFullRefundTakesBackThePeriodItBought proves a refund of a subscription
// order no longer leaves the access standing: an extension refunded after it
// was provisioned falls back to the end the customer had before it, and the
// shorter expiry reaches Remnawave through a queued reconcile.
func TestAFullRefundTakesBackThePeriodItBought(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	store := commercepg.New(harness.pool, nil, testOptions())
	customerID := harness.customer(ctx, t)
	panel := newMemoryRemnawave()
	worker := fulfillment.NewWorker(harness.pool, panel)

	purchaseID, _, _ := harness.paidOrder(ctx, t, store, customerID, "refund-revoke", 25000)
	harness.fulfillAll(ctx, t, worker)
	var planVersionID, subscriptionID string
	var purchaseEnd time.Time
	if err := harness.pool.QueryRow(ctx, `SELECT plan_version_id::text, subscription_id::text, ends_at FROM entitlements
		WHERE order_id = $1::uuid`, purchaseID).Scan(&planVersionID, &subscriptionID, &purchaseEnd); err != nil {
		t.Fatalf("read purchase: %v", err)
	}

	extension, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: planVersionID, Currency: "RUB",
		Operation: "extension", IdempotencyKey: "refund-revoke-extension", SubscriptionID: subscriptionID,
	})
	if err != nil {
		t.Fatalf("extension order: %v", err)
	}
	extensionID := uuidText(extension.ID)
	harness.settle(ctx, t, store, extensionID, 25000, "refund-revoke-extension-payment")
	harness.fulfillAll(ctx, t, worker)
	var extendedEnd time.Time
	var remoteID int64
	if err := harness.pool.QueryRow(ctx, `SELECT ends_at, remnawave_user_id FROM entitlements WHERE order_id = $1::uuid`, extensionID).Scan(&extendedEnd, &remoteID); err != nil {
		t.Fatalf("read extension: %v", err)
	}
	if !extendedEnd.After(purchaseEnd.Add(29 * 24 * time.Hour)) {
		t.Fatalf("the extension should have stacked on the purchase: %s vs %s", extendedEnd, purchaseEnd)
	}

	var intentID string
	if err := harness.pool.QueryRow(ctx, `SELECT id::text FROM payment_intents WHERE order_id = $1::uuid`, extensionID).Scan(&intentID); err != nil {
		t.Fatalf("read intent: %v", err)
	}
	if _, err := store.RecordRefund(ctx, intentID, "refund-revoke-1", "refund:refund-revoke-1", commerce.Money{Amount: 25000, Currency: "RUB"}); err != nil {
		t.Fatalf("refund: %v", err)
	}
	var revokedEnd time.Time
	if err := harness.pool.QueryRow(ctx, `SELECT ends_at FROM entitlements WHERE order_id = $1::uuid`, extensionID).Scan(&revokedEnd); err != nil {
		t.Fatalf("read revoked: %v", err)
	}
	if !revokedEnd.Equal(purchaseEnd) {
		t.Fatalf("the refund must give back exactly the extension: end %s, want %s", revokedEnd, purchaseEnd)
	}
	var access string
	if err := harness.pool.QueryRow(ctx, `SELECT payload ->> 'access' FROM operator_notifications
		WHERE kind = 'refund' AND dedupe_key = 'refund:refund-revoke-1'`).Scan(&access); err != nil {
		t.Fatalf("read refund notice: %v", err)
	}
	if access != "revoked" {
		t.Fatalf("the refund notice must say the access was revoked, got %q", access)
	}
	harness.fulfillAll(ctx, t, worker)
	if pushed := panel.expiryOf(remoteID); !pushed.Equal(purchaseEnd) {
		t.Fatalf("Remnawave must be told the shorter end %s, holds %s", purchaseEnd, pushed)
	}
	var orderState string
	if err := harness.pool.QueryRow(ctx, `SELECT state FROM orders WHERE id = $1::uuid`, extensionID).Scan(&orderState); err != nil {
		t.Fatalf("read order: %v", err)
	}
	if orderState != "refunded" {
		t.Fatalf("order is %q, want refunded", orderState)
	}
}

// TestARefundedTopUpIsTakenBackOutOfTheWallet proves a top-up refund debits
// the credit it bought, and that a wallet which no longer holds it is left
// alone and brought to an operator instead of going negative.
func TestARefundedTopUpIsTakenBackOutOfTheWallet(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	store := commercepg.New(harness.pool, nil, testOptions())
	customerID := harness.customer(ctx, t)

	topUp := func(key string) string {
		order, err := store.CreateTopUpOrder(ctx, commercepg.TopUpInput{
			CustomerID: customerID, Currency: "RUB", AmountMinor: 50000, IdempotencyKey: key,
		})
		if err != nil {
			t.Fatalf("top-up: %v", err)
		}
		harness.settle(ctx, t, store, uuidText(order.ID), 50000, key+"-payment")
		var intentID string
		if err := harness.pool.QueryRow(ctx, `SELECT id::text FROM payment_intents WHERE order_id = $1`, order.ID).Scan(&intentID); err != nil {
			t.Fatalf("read intent: %v", err)
		}
		return intentID
	}

	first := topUp("refund-topup-1")
	if _, err := store.RecordRefund(ctx, first, "refund-topup-first", "refund:refund-topup-first", commerce.Money{Amount: 50000, Currency: "RUB"}); err != nil {
		t.Fatalf("refund: %v", err)
	}
	if balance := harness.walletBalance(ctx, t, customerID); balance != 0 {
		t.Fatalf("a refunded top-up must leave the wallet, balance %d", balance)
	}
	// The same refund recorded again debits nothing more.
	if _, err := store.RecordRefund(ctx, first, "refund-topup-first", "refund:refund-topup-first", commerce.Money{Amount: 50000, Currency: "RUB"}); err != nil {
		t.Fatalf("replayed refund: %v", err)
	}
	if balance := harness.walletBalance(ctx, t, customerID); balance != 0 {
		t.Fatalf("a replayed refund debited again, balance %d", balance)
	}

	// The second top-up is spent before it is refunded.
	second := topUp("refund-topup-2")
	if err := store.AdjustWallet(ctx, customerID, "RUB", -30000, "debit", "spent", "refund-topup-spend", "spent on something", "operator-1"); err != nil {
		t.Fatalf("spend: %v", err)
	}
	if _, err := store.RecordRefund(ctx, second, "refund-topup-second", "refund:refund-topup-second", commerce.Money{Amount: 50000, Currency: "RUB"}); err != nil {
		t.Fatalf("refund: %v", err)
	}
	if balance := harness.walletBalance(ctx, t, customerID); balance != 20000 {
		t.Fatalf("a wallet short of the refund must be left alone, balance %d", balance)
	}
	var incidents int
	if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM operator_notifications
		WHERE kind = 'incident' AND payload ->> 'status' = 'topup_refund_wallet_short'`).Scan(&incidents); err != nil {
		t.Fatalf("count incidents: %v", err)
	}
	if incidents != 1 {
		t.Fatalf("expected one operator incident for the short wallet, got %d", incidents)
	}
}
