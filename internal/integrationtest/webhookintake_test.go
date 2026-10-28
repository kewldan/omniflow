//go:build integration

package integrationtest

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/payments"
	"github.com/omniflow/omniflow/internal/paymentservice"
)

// notifyingProvider stands in for YooKassa: an unsigned notification whose
// body names a payment, which the service must confirm by polling.
type notifyingProvider struct {
	hostedStubProvider
	polls atomic.Int32
}

func (provider *notifyingProvider) Capabilities() payments.Capabilities {
	return payments.Capabilities{Polling: true, Webhooks: true}
}

func (provider *notifyingProvider) Poll(context.Context, string) (payments.Intent, error) {
	provider.polls.Add(1)
	return payments.Intent{Status: "succeeded"}, nil
}

func (provider *notifyingProvider) VerifyWebhook(_ http.Header, body []byte) (payments.WebhookEvent, error) {
	reference, ok := strings.CutPrefix(string(body), "payment:")
	if !ok {
		return payments.WebhookEvent{}, payments.ErrProviderResponse
	}
	return payments.WebhookEvent{
		ID: "payment.succeeded:" + reference, ProviderReference: reference, Status: "succeeded",
		Amount: commerce.Money{Amount: 100, Currency: "RUB"},
	}, nil
}

// The webhook route is unauthenticated. A body that fails validation is
// recorded by digest only, and a well-formed notification for a payment this
// installation never opened does not make it spend a provider request.
func TestWebhookIntakeStoresNoJunkAndPollsOnlyKnownPayments(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	provider := &notifyingProvider{hostedStubProvider: hostedStubProvider{name: "yookassa", currency: "RUB"}}
	service := paymentservice.New(harness.pool, store, provider)

	junk := strings.Repeat("x", 4096)
	if _, err := service.HandleWebhook(ctx, "yookassa", http.Header{}, []byte(junk)); err == nil {
		t.Fatal("an invalid body was accepted")
	}
	var bodyBytes int
	if err := harness.pool.QueryRow(ctx,
		`SELECT octet_length(raw_body) FROM provider_webhook_events WHERE NOT signature_valid`,
	).Scan(&bodyBytes); err != nil {
		t.Fatalf("read invalid event: %v", err)
	}
	if bodyBytes != 0 {
		t.Fatalf("an invalid webhook body was stored (%d bytes)", bodyBytes)
	}

	if _, err := service.HandleWebhook(ctx, "yookassa", http.Header{}, []byte("payment:not-ours")); err == nil {
		t.Fatal("a notification for an unknown payment was accepted")
	}
	if polls := provider.polls.Load(); polls != 0 {
		t.Fatalf("an unknown payment triggered %d provider polls", polls)
	}
	var status, code string
	if err := harness.pool.QueryRow(ctx,
		`SELECT status, error_code FROM provider_webhook_events WHERE provider_event_id = 'payment.succeeded:not-ours'`,
	).Scan(&status, &code); err != nil {
		t.Fatalf("read event: %v", err)
	}
	if status != "failed" || code != "payment_not_found" {
		t.Fatalf("event = %s/%s, want failed/payment_not_found", status, code)
	}

	// A notification for a payment this installation did open is still
	// confirmed with the provider before anything is believed.
	var orderID string
	if err := harness.pool.QueryRow(ctx, `INSERT INTO orders
		(user_id, state, operation, currency, subtotal_minor, external_minor, paid_minor, idempotency_key)
		VALUES ($1::uuid, 'paid', 'purchase', 'RUB', 100, 100, 100, 'webhook-intake-order')
		RETURNING id::text`, harness.customer(ctx, t)).Scan(&orderID); err != nil {
		t.Fatalf("seed order: %v", err)
	}
	if _, err := harness.pool.Exec(ctx, `INSERT INTO payment_intents
		(order_id, provider, status, amount_minor, currency, provider_reference, idempotency_key)
		VALUES ($1::uuid, 'yookassa', 'pending', 100, 'RUB', 'ours', 'webhook-intake-intent')`, orderID); err != nil {
		t.Fatalf("seed intent: %v", err)
	}
	_, _ = service.HandleWebhook(ctx, "yookassa", http.Header{}, []byte("payment:ours"))
	if polls := provider.polls.Load(); polls != 1 {
		t.Fatalf("a known payment was polled %d times, want 1", polls)
	}
}
