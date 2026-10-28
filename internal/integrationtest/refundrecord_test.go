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

// refundingProvider stands in for YooKassa's refund API. answer decides what
// each refund request gets back; before it answers, onRefund lets a test look
// at what Omniflow has already written down.
type refundingProvider struct {
	answer   func(payments.RefundRequest) (payments.Refund, error)
	onRefund func()
	requests int
}

func (provider *refundingProvider) Name() string { return "yookassa" }

func (provider *refundingProvider) Capabilities() payments.Capabilities {
	return payments.Capabilities{Refunds: true, PartialRefund: true}
}

func (provider *refundingProvider) Create(context.Context, payments.CreateRequest) (payments.Intent, error) {
	return payments.Intent{}, errors.New("not used")
}

func (provider *refundingProvider) Poll(context.Context, string) (payments.Intent, error) {
	return payments.Intent{}, payments.ErrUnsupported
}

func (provider *refundingProvider) Refund(_ context.Context, request payments.RefundRequest) (payments.Refund, error) {
	provider.requests++
	if provider.onRefund != nil {
		provider.onRefund()
	}
	return provider.answer(request)
}

func (provider *refundingProvider) VerifyWebhook(http.Header, []byte) (payments.WebhookEvent, error) {
	return payments.WebhookEvent{}, payments.ErrUnsupported
}

// paidThroughYooKassa settles a priced purchase through a yookassa intent and
// returns the order and the intent.
func (harness *harness) paidThroughYooKassa(ctx context.Context, t *testing.T, store *commercepg.Store, customerID, code string) (string, string) {
	t.Helper()
	planVersionID := harness.catalog(ctx, t, code, 25000)
	order, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: planVersionID, Currency: "RUB",
		Operation: "purchase", IdempotencyKey: code + "-order",
	})
	if err != nil {
		t.Fatalf("create order: %v", err)
	}
	var intentID string
	if err := harness.pool.QueryRow(ctx, `INSERT INTO payment_intents
		(order_id, provider, status, amount_minor, currency, provider_reference, idempotency_key)
		VALUES ($1, 'yookassa', 'processing', 25000, 'RUB', $2, $2) RETURNING id::text`, order.ID, code+"-payment").Scan(&intentID); err != nil {
		t.Fatalf("create intent: %v", err)
	}
	if _, classification, err := store.RecordProviderPayment(ctx, intentID, code+"-event", commerce.Money{Amount: 25000, Currency: "RUB"}, false); err != nil || classification != commerce.ClassificationPaid {
		t.Fatalf("settle: %q %v", classification, err)
	}
	return uuidText(order.ID), intentID
}

// TestARefundIsWrittenDownBeforeTheProviderIsAsked proves the crash window is
// closed: the refund row exists before the request leaves, a request whose
// answer never came back leaves it waiting rather than lost, and the revival
// asks again under the same key and records the money and the order state.
func TestARefundIsWrittenDownBeforeTheProviderIsAsked(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	store := commercepg.New(harness.pool, nil, testOptions())
	customerID := harness.customer(ctx, t)
	orderID, intentID := harness.paidThroughYooKassa(ctx, t, store, customerID, "refund-record")

	provider := &refundingProvider{}
	provider.onRefund = func() {
		var status string
		var referenced bool
		if err := harness.pool.QueryRow(ctx, `SELECT status, provider_reference IS NOT NULL FROM refunds
			WHERE payment_intent_id = $1::uuid`, intentID).Scan(&status, &referenced); err != nil {
			t.Errorf("the refund must be recorded before the provider is asked: %v", err)
			return
		}
		if provider.requests == 1 && (status != "pending" || referenced) {
			t.Errorf("the first request must find a pending, unanswered refund, found %s (answered %v)", status, referenced)
		}
	}
	// The provider takes the money back, and the answer is lost on the way.
	keys := map[string]bool{}
	provider.answer = func(request payments.RefundRequest) (payments.Refund, error) {
		keys[request.IdempotencyKey] = true
		return payments.Refund{}, errors.New("call provider: context deadline exceeded")
	}
	service := paymentservice.New(harness.pool, store, provider)
	if _, err := service.Refund(ctx, intentID, "refund-record-key", commerce.Money{Amount: 25000, Currency: "RUB"}, "customer asked", nil); err == nil {
		t.Fatal("a lost answer must be reported to the caller")
	}
	var refundID, status string
	if err := harness.pool.QueryRow(ctx, `SELECT id::text, status FROM refunds WHERE payment_intent_id = $1::uuid`, intentID).Scan(&refundID, &status); err != nil {
		t.Fatalf("the refund was lost with the answer: %v", err)
	}
	if status != "pending" {
		t.Fatalf("a refund with no answer must stay pending, got %q", status)
	}
	var orderState string
	if err := harness.pool.QueryRow(ctx, `SELECT state FROM orders WHERE id = $1::uuid`, orderID).Scan(&orderState); err != nil {
		t.Fatalf("read order: %v", err)
	}
	if orderState == "refunded" {
		t.Fatal("the order must not read refunded before the provider confirmed it")
	}

	// The reconciler asks again under the same key, and this time the answer
	// arrives.
	provider.answer = func(request payments.RefundRequest) (payments.Refund, error) {
		keys[request.IdempotencyKey] = true
		return payments.Refund{ProviderReference: "yk-refund-1", Status: "succeeded", Amount: request.Amount}, nil
	}
	revived, err := service.ReviveRefund(ctx, refundID)
	if err != nil {
		t.Fatalf("revive: %v", err)
	}
	if revived.Status != "succeeded" || revived.ProviderReference.String != "yk-refund-1" {
		t.Fatalf("revived refund is %s/%s", revived.Status, revived.ProviderReference.String)
	}
	if len(keys) != 1 || !keys["refund-record-key"] {
		t.Fatalf("every request must carry the refund's own idempotency key, saw %v", keys)
	}
	if err := harness.pool.QueryRow(ctx, `SELECT state FROM orders WHERE id = $1::uuid`, orderID).Scan(&orderState); err != nil {
		t.Fatalf("read order: %v", err)
	}
	var ledger int
	if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM ledger_transactions WHERE type = 'refund' AND reference_id = $1`, refundID).Scan(&ledger); err != nil {
		t.Fatalf("count ledger: %v", err)
	}
	if orderState != "refunded" || ledger != 1 {
		t.Fatalf("after the revival: order %s, %d refund transactions; want refunded and one", orderState, ledger)
	}
	// Reviving again changes nothing.
	if _, err := service.ReviveRefund(ctx, refundID); err != nil || provider.requests != 2 {
		t.Fatalf("an answered refund must not be sent again: %v after %d requests", err, provider.requests)
	}
}

// TestARefusedRefundReleasesItsReservation proves a definite refusal on the
// first request is recorded as failed, so the amount it reserved is free for
// a refund that can succeed.
func TestARefusedRefundReleasesItsReservation(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	store := commercepg.New(harness.pool, nil, testOptions())
	customerID := harness.customer(ctx, t)
	_, intentID := harness.paidThroughYooKassa(ctx, t, store, customerID, "refund-refused")

	refuse := true
	provider := &refundingProvider{answer: func(request payments.RefundRequest) (payments.Refund, error) {
		if refuse {
			return payments.Refund{}, &payments.StatusError{StatusCode: http.StatusBadRequest}
		}
		return payments.Refund{ProviderReference: "yk-refund-2", Status: "succeeded", Amount: request.Amount}, nil
	}}
	service := paymentservice.New(harness.pool, store, provider)
	if _, err := service.Refund(ctx, intentID, "refund-refused-1", commerce.Money{Amount: 25000, Currency: "RUB"}, "customer asked", nil); err == nil {
		t.Fatal("a refused refund must be reported")
	}
	var status string
	if err := harness.pool.QueryRow(ctx, `SELECT status FROM refunds WHERE idempotency_key = 'refund-refused-1'`).Scan(&status); err != nil {
		t.Fatalf("read refund: %v", err)
	}
	if status != "failed" {
		t.Fatalf("a definite refusal must be recorded as failed, got %q", status)
	}
	refuse = false
	refund, err := service.Refund(ctx, intentID, "refund-refused-2", commerce.Money{Amount: 25000, Currency: "RUB"}, "customer asked again", nil)
	if err != nil || refund.Status != "succeeded" {
		t.Fatalf("the refused amount must be refundable again: %+v %v", refund, err)
	}
}
