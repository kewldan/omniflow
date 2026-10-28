//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/payments"
	"github.com/omniflow/omniflow/internal/paymentservice"
)

// savedChargeProvider stands in for YooKassa on an automatic renewal: the
// saved-method charge is captured synchronously, and the provider then sends
// the payment.succeeded notification for the same payment.
type savedChargeProvider struct {
	reference string
	amount    commerce.Money
}

func (provider *savedChargeProvider) Name() string { return "yookassa" }

func (provider *savedChargeProvider) Capabilities() payments.Capabilities {
	return payments.Capabilities{Webhooks: true, Polling: true}
}

func (provider *savedChargeProvider) Create(context.Context, payments.CreateRequest) (payments.Intent, error) {
	return payments.Intent{}, errors.New("a renewal never opens a checkout")
}

func (provider *savedChargeProvider) ChargeSaved(_ context.Context, request payments.SavedChargeRequest) (payments.Intent, error) {
	provider.amount = request.Amount
	return payments.Intent{ProviderReference: provider.reference, Status: "succeeded", Amount: request.Amount}, nil
}

func (provider *savedChargeProvider) Poll(context.Context, string) (payments.Intent, error) {
	return payments.Intent{ProviderReference: provider.reference, Status: "succeeded", Amount: provider.amount}, nil
}

func (provider *savedChargeProvider) Refund(context.Context, payments.RefundRequest) (payments.Refund, error) {
	return payments.Refund{}, errors.New("not used")
}

func (provider *savedChargeProvider) VerifyWebhook(http.Header, []byte) (payments.WebhookEvent, error) {
	return payments.WebhookEvent{
		ID: "notification-" + provider.reference, Type: "payment.succeeded",
		ProviderReference: provider.reference, Status: "succeeded", Amount: provider.amount,
	}, nil
}

// TestTheWebhookAfterASynchronousChargeIsAReplay is the automatic-renewal
// path end to end: the saved-method charge settles the order at once, and the
// provider's notification for that same payment arriving afterwards is
// recorded as a replay. It used to be classified a duplicate and page an
// operator on every renewal.
func TestTheWebhookAfterASynchronousChargeIsAReplay(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	store := commercepg.New(harness.pool, nil, testOptions())
	customerID := harness.customer(ctx, t)
	planVersionID := harness.catalog(ctx, t, "replay-plan", 29900)
	provider := &savedChargeProvider{reference: "yk-payment-1"}
	service := paymentservice.New(harness.pool, store, provider)

	order, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: planVersionID, Currency: "RUB",
		Operation: "purchase", IdempotencyKey: "replay-order",
	})
	if err != nil {
		t.Fatalf("create order: %v", err)
	}
	intent, err := service.CreateIntent(ctx, paymentservice.CreateIntentInput{
		OrderID: uuidText(order.ID), Provider: "yookassa", IdempotencyKey: "renewal:replay:1",
		SavedMethodToken: "saved-method",
	})
	if err != nil {
		t.Fatalf("charge saved method: %v", err)
	}
	if intent.Status != "succeeded" {
		t.Fatalf("the synchronous charge must settle the intent, got %q", intent.Status)
	}

	classification, err := service.HandleWebhook(ctx, "yookassa", http.Header{}, []byte(`{"event":"payment.succeeded"}`))
	if err != nil {
		t.Fatalf("webhook: %v", err)
	}
	if classification != commerce.ClassificationReplay {
		t.Fatalf("the notification for the charge already settled must be a replay, got %q", classification)
	}
	var incidents, entitlements int
	if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM operator_notifications WHERE kind = 'incident'`).Scan(&incidents); err != nil {
		t.Fatalf("count incidents: %v", err)
	}
	if incidents != 0 {
		t.Fatalf("a renewal's own notification raised %d operator incidents", incidents)
	}
	if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM entitlements WHERE order_id = $1`, order.ID).Scan(&entitlements); err != nil {
		t.Fatalf("count entitlements: %v", err)
	}
	if entitlements != 1 {
		t.Fatalf("expected one entitlement for one charge, got %d", entitlements)
	}
	var webhookStatus string
	if err := harness.pool.QueryRow(ctx, `SELECT status FROM provider_webhook_events WHERE provider = 'yookassa'`).Scan(&webhookStatus); err != nil {
		t.Fatalf("read webhook: %v", err)
	}
	if webhookStatus != "processed" {
		t.Fatalf("the replayed notification must be processed, got %q", webhookStatus)
	}
}
