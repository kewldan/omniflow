//go:build integration

package integrationtest

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/payments"
	"github.com/omniflow/omniflow/internal/paymentservice"
)

// reversingProvider is a signed-webhook adapter whose notifications report a
// refund or a chargeback the provider made on its own side. The body is
// "<status>:<reference>[:<cumulative refunded>]".
type reversingProvider struct {
	hostedStubProvider
}

func (provider *reversingProvider) Capabilities() payments.Capabilities {
	return payments.Capabilities{Webhooks: true}
}

func (provider *reversingProvider) VerifyWebhook(_ http.Header, body []byte) (payments.WebhookEvent, error) {
	parts := strings.Split(string(body), ":")
	if len(parts) < 2 {
		return payments.WebhookEvent{}, payments.ErrProviderResponse
	}
	event := payments.WebhookEvent{ID: string(body), ProviderReference: parts[1], Status: parts[0]}
	if len(parts) == 3 {
		var cumulative int64
		for _, digit := range parts[2] {
			cumulative = cumulative*10 + int64(digit-'0')
		}
		event.RefundedMinor = cumulative
	}
	return event, nil
}

// A refund the provider made from its own dashboard takes back what the
// payment bought, once; a partial refund followed by the rest applies only the
// difference; and a chargeback reverses the payment and retires the card it
// was bound from.
func TestProviderRefundsAndChargebacksReverseThePurchaseOnce(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	provider := &reversingProvider{hostedStubProvider: hostedStubProvider{name: "yookassa", currency: "RUB"}}
	service := paymentservice.New(harness.pool, store, provider)

	customerID := harness.customer(ctx, t)
	orderID, intentID := harness.paidThroughYooKassa(ctx, t, store, customerID, "reversal-full")
	if _, err := harness.pool.Exec(ctx, `INSERT INTO payment_methods
		(user_id, provider, provider_token, display_label, is_default, source_payment_intent_id)
		VALUES ($1::uuid, 'yookassa', 'pm-disputed', 'Visa *4242', true, $2::uuid)`, customerID, intentID); err != nil {
		t.Fatalf("seed method: %v", err)
	}
	reference := "reversal-full-payment"

	// An unknown payment is rejected, as any webhook is.
	if _, err := service.HandleWebhook(ctx, "yookassa", http.Header{}, []byte("refunded:not-ours")); err == nil {
		t.Fatal("a reversal for an unknown payment was accepted")
	}

	// Partial, then the remainder; each delivered twice.
	for _, body := range []string{"refunded:" + reference + ":10000", "refunded:" + reference + ":10000"} {
		classification, err := service.HandleWebhook(ctx, "yookassa", http.Header{}, []byte(body))
		if err != nil {
			t.Fatalf("partial refund: %v", err)
		}
		if classification != paymentservice.ClassificationReversed && classification != "duplicate" {
			t.Fatalf("partial refund classified %q", classification)
		}
	}
	var refunded int64
	var state string
	if err := harness.pool.QueryRow(ctx, `SELECT refunded_minor, state FROM orders WHERE id = $1::uuid`, orderID).Scan(&refunded, &state); err != nil {
		t.Fatalf("read order: %v", err)
	}
	if refunded != 10000 || state != string(commerce.OrderPartiallyRefunded) {
		t.Fatalf("after a partial refund: refunded=%d state=%s", refunded, state)
	}

	// The same cumulative level reported in a new notification changes nothing.
	if classification, err := service.HandleWebhook(ctx, "yookassa", http.Header{}, []byte("refunded:"+reference+":010000")); err != nil || classification != paymentservice.ClassificationReversalKnown {
		t.Fatalf("a repeated level: %q %v", classification, err)
	}

	// A chargeback for the whole payment applies the remainder only.
	if _, err := service.HandleWebhook(ctx, "yookassa", http.Header{}, []byte("chargeback:"+reference)); err != nil {
		t.Fatalf("chargeback: %v", err)
	}
	if err := harness.pool.QueryRow(ctx, `SELECT refunded_minor, state FROM orders WHERE id = $1::uuid`, orderID).Scan(&refunded, &state); err != nil {
		t.Fatalf("read order: %v", err)
	}
	if refunded != 25000 || state != string(commerce.OrderRefunded) {
		t.Fatalf("after the chargeback: refunded=%d state=%s", refunded, state)
	}
	var refunds, ledger int
	if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM refunds WHERE payment_intent_id = $1::uuid AND status = 'succeeded'`, intentID).Scan(&refunds); err != nil {
		t.Fatalf("count refunds: %v", err)
	}
	if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM ledger_transactions WHERE type = 'refund' AND idempotency_key LIKE 'refund:' || $1 || ':%'`, intentID).Scan(&ledger); err != nil {
		t.Fatalf("count ledger: %v", err)
	}
	if refunds != 2 || ledger != 2 {
		t.Fatalf("refunds=%d ledger=%d, want two of each", refunds, ledger)
	}
	var methodStatus string
	if err := harness.pool.QueryRow(ctx, `SELECT status FROM payment_methods WHERE provider_token = 'pm-disputed'`).Scan(&methodStatus); err != nil {
		t.Fatalf("read method: %v", err)
	}
	if methodStatus != "failed" {
		t.Fatalf("the disputed card is %s, want failed", methodStatus)
	}
	var incidents int
	if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM operator_notifications WHERE dedupe_key LIKE 'chargeback:' || $1 || '%'`, intentID).Scan(&incidents); err != nil {
		t.Fatalf("count incidents: %v", err)
	}
	if incidents != 1 {
		t.Fatalf("chargeback incidents = %d, want 1", incidents)
	}

	// Nothing is left to reverse: a further report is known, not applied.
	if classification, err := service.HandleWebhook(ctx, "yookassa", http.Header{}, []byte("refunded:"+reference+":25000")); err != nil || classification != paymentservice.ClassificationReversalKnown {
		t.Fatalf("a refund after the chargeback: %q %v", classification, err)
	}
}

// A provider top-up that is refunded takes the credit back out of the wallet.
func TestAProviderRefundOfATopUpDebitsTheWallet(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	provider := &reversingProvider{hostedStubProvider: hostedStubProvider{name: "yookassa", currency: "RUB"}}
	service := paymentservice.New(harness.pool, store, provider)
	customerID := harness.customer(ctx, t)

	order, err := store.CreateTopUpOrder(ctx, commercepg.TopUpInput{CustomerID: customerID, Currency: "RUB", AmountMinor: 30000, IdempotencyKey: "reversal-topup"})
	if err != nil {
		t.Fatalf("top-up: %v", err)
	}
	var intentID string
	if err = harness.pool.QueryRow(ctx, `INSERT INTO payment_intents
		(order_id, provider, status, amount_minor, currency, provider_reference, idempotency_key)
		VALUES ($1, 'yookassa', 'processing', 30000, 'RUB', 'topup-ref', 'topup-ref') RETURNING id::text`, order.ID).Scan(&intentID); err != nil {
		t.Fatalf("intent: %v", err)
	}
	if _, _, err = store.RecordProviderPayment(ctx, intentID, "topup-paid", commerce.Money{Amount: 30000, Currency: "RUB"}, false); err != nil {
		t.Fatalf("settle: %v", err)
	}
	if _, err = service.HandleWebhook(ctx, "yookassa", http.Header{}, []byte("refunded:topup-ref")); err != nil {
		t.Fatalf("refund: %v", err)
	}
	var balance int64
	if err = harness.pool.QueryRow(ctx, `SELECT COALESCE(sum(amount_minor), 0)::bigint FROM ledger_entries
		WHERE account_type = 'customer_wallet' AND user_id = $1::uuid AND currency = 'RUB'`, customerID).Scan(&balance); err != nil {
		t.Fatalf("balance: %v", err)
	}
	if balance != 0 {
		t.Fatalf("wallet balance after a refunded top-up = %d, want 0", balance)
	}
}
