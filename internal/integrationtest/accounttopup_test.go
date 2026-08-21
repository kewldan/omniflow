//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/omniflow/omniflow/internal/accountcheckout"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/payments"
	"github.com/omniflow/omniflow/internal/paymentservice"
)

// refusingProvider is a payment adapter whose intent creation fails until it
// is told to succeed — the provider outage a top-up can run into after its
// order already exists.
type refusingProvider struct {
	hostedStubProvider
	refuse bool
}

func (provider *refusingProvider) Create(ctx context.Context, request payments.CreateRequest) (payments.Intent, error) {
	if provider.refuse {
		return payments.Intent{}, errors.New("provider is down")
	}
	return provider.hostedStubProvider.Create(ctx, request)
}

// A top-up whose provider refused the intent is still the customer's order: it
// is returned with the error, listed under the wallet's pending top-ups, and
// retried from the order by the same key without creating a second one.
func TestARefusedTopUpKeepsItsOrderAndCanBeRetried(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	store := commercepg.New(harness.pool, nil, testOptions())
	provider := &refusingProvider{hostedStubProvider: hostedStubProvider{name: "yookassa", currency: "RUB"}, refuse: true}
	paymentService := paymentservice.New(harness.pool, store, provider)
	service, err := accountcheckout.New(harness.pool, store, paymentService, accountcheckout.Options{
		Logger:   slog.New(slog.DiscardHandler),
		Settings: accountcheckout.Settings{Currency: "RUB", PublicURL: "https://example.test"},
	})
	if err != nil {
		t.Fatalf("build checkout: %v", err)
	}
	customerID := harness.customer(ctx, t)

	topUp, err := service.StartTopUp(ctx, customerID, "RUB", 50000, "yookassa", "topup-key-0001")
	if err == nil {
		t.Fatal("a refused intent was reported as started")
	}
	if topUp.OrderID == "" {
		t.Fatal("the order was dropped together with the provider's refusal")
	}
	if topUp.Payment.ID != "" {
		t.Fatalf("a payment was reported for a refused intent: %+v", topUp.Payment)
	}

	// The wallet lists it, so the customer can find it again.
	wallet, err := service.Wallet(ctx, customerID)
	if err != nil {
		t.Fatalf("wallet: %v", err)
	}
	if len(wallet.PendingTopUps) != 1 || wallet.PendingTopUps[0].ID != topUp.OrderID {
		t.Fatalf("pending top-ups = %+v, want the refused order", wallet.PendingTopUps)
	}
	// The intent row the service opened before the provider refused carries
	// no page to send the customer to; the entry says so rather than offering
	// a link that goes nowhere.
	if wallet.PendingTopUps[0].CheckoutURL != "" {
		t.Fatalf("a refused intent carries a checkout URL: %q", wallet.PendingTopUps[0].CheckoutURL)
	}

	// A retry with the same key is the same order, not a second top-up.
	provider.refuse = false
	again, err := service.StartTopUp(ctx, customerID, "RUB", 50000, "yookassa", "topup-key-0001")
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if again.OrderID != topUp.OrderID {
		t.Fatalf("the retry created order %s, want %s", again.OrderID, topUp.OrderID)
	}
	if again.Payment.Handoff != accountcheckout.HandoffHosted || again.Payment.CheckoutURL == "" {
		t.Fatalf("the retried payment has no hosted handoff: %+v", again.Payment)
	}
	var orders int
	if err = harness.pool.QueryRow(ctx, `SELECT count(*) FROM orders WHERE user_id = $1::uuid`, customerID).Scan(&orders); err != nil {
		t.Fatalf("count orders: %v", err)
	}
	if orders != 1 {
		t.Fatalf("%d top-up orders exist after a retry, want 1", orders)
	}

	// The order screen can pay it too: the same methods, the same order.
	if _, err = service.StartOrderPayment(ctx, customerID, topUp.OrderID, "en", "yookassa"); err != nil {
		t.Fatalf("pay the top-up from its order: %v", err)
	}
	if provider.created != 1 {
		t.Fatalf("paying from the order created another intent: %d creates", provider.created)
	}

	// Once a payment exists the wallet's pending entry carries its handoff.
	wallet, err = service.Wallet(ctx, customerID)
	if err != nil {
		t.Fatalf("wallet after retry: %v", err)
	}
	if len(wallet.PendingTopUps) != 1 || wallet.PendingTopUps[0].CheckoutURL == "" {
		t.Fatalf("the pending top-up lost its handoff: %+v", wallet.PendingTopUps)
	}
}

// A method that does not settle the wallet currency is refused before any
// order exists; the window is not charged for a top-up that never was.
func TestATopUpWithAnUnsuitableMethodCreatesNothing(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	store := commercepg.New(harness.pool, nil, testOptions())
	usd := &hostedStubProvider{name: "cryptobot", currency: "USD"}
	service, err := accountcheckout.New(harness.pool, store, paymentservice.New(harness.pool, store, usd), accountcheckout.Options{
		Logger:   slog.New(slog.DiscardHandler),
		Settings: accountcheckout.Settings{Currency: "RUB", PublicURL: "https://example.test"},
	})
	if err != nil {
		t.Fatalf("build checkout: %v", err)
	}
	customerID := harness.customer(ctx, t)
	if _, err = service.StartTopUp(ctx, customerID, "RUB", 50000, "cryptobot", "topup-key-0002"); !errors.Is(err, accountcheckout.ErrProviderCurrency) {
		t.Fatalf("a USD-only method was accepted for a RUB top-up: %v", err)
	}
	var orders int
	if err = harness.pool.QueryRow(ctx, `SELECT count(*) FROM orders WHERE user_id = $1::uuid`, customerID).Scan(&orders); err != nil {
		t.Fatalf("count orders: %v", err)
	}
	if orders != 0 {
		t.Fatalf("%d orders were created for a refused method", orders)
	}
}
