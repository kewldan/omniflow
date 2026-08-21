//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/accountcheckout"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/payments"
	"github.com/omniflow/omniflow/internal/paymentservice"
)

// hostedStubProvider is a payment adapter that settles one currency and hands
// back a hosted page, standing in for YooKassa or CryptoBot.
type hostedStubProvider struct {
	name     string
	currency string
	created  int
}

func (provider *hostedStubProvider) Name() string { return provider.name }

func (provider *hostedStubProvider) Capabilities() payments.Capabilities {
	return payments.Capabilities{Polling: true}
}

func (provider *hostedStubProvider) SupportsCurrency(currency string) bool {
	return currency == provider.currency
}

func (provider *hostedStubProvider) Create(_ context.Context, request payments.CreateRequest) (payments.Intent, error) {
	provider.created++
	return payments.Intent{
		ProviderReference: "ref-" + request.OrderID, Status: "pending",
		CheckoutURL: "https://pay.test/" + request.OrderID, Amount: request.Amount,
	}, nil
}

func (provider *hostedStubProvider) Poll(context.Context, string) (payments.Intent, error) {
	return payments.Intent{Status: "pending"}, nil
}

func (provider *hostedStubProvider) Refund(context.Context, payments.RefundRequest) (payments.Refund, error) {
	return payments.Refund{}, errors.New("not supported")
}

func (provider *hostedStubProvider) VerifyWebhook(http.Header, []byte) (payments.WebhookEvent, error) {
	return payments.WebhookEvent{}, errors.New("not supported")
}

// An order that expired between the page rendering and the button being
// pressed is a conflict the customer can read, not a failure; a method that
// does not settle the order's currency is refused before the provider is
// asked; and the method the checkout recorded is what the order resumes with
// when the page does not say.
func TestStartingAnOrderPaymentGuardsStateCurrencyAndRecordedMethod(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	store := commercepg.New(harness.pool, nil, testOptions())
	rub := &hostedStubProvider{name: "yookassa", currency: "RUB"}
	usd := &hostedStubProvider{name: "cryptobot", currency: "USD"}
	paymentService := paymentservice.New(harness.pool, store, rub, usd)
	clock := time.Now().UTC()
	service, err := accountcheckout.New(harness.pool, store, paymentService, accountcheckout.Options{
		Logger: slog.New(slog.DiscardHandler),
		Settings: accountcheckout.Settings{
			Currency: "RUB", PublicURL: "https://example.test", MultiSubscription: true,
		},
		Clock: func() time.Time { return clock },
	})
	if err != nil {
		t.Fatalf("build checkout: %v", err)
	}
	customerID := harness.customer(ctx, t)
	planVersionID := harness.catalog(ctx, t, "payable-plan", 34900)

	// The checkout records the method; the order inherits it without the page
	// having to pass it along.
	session := openSession(ctx, t, service, customerID, planVersionID, "purchase")
	yookassa := "yookassa"
	if _, err = service.Update(ctx, customerID, "en", accountcheckout.UpdateRequest{Provider: &yookassa}); err != nil {
		t.Fatalf("choose provider: %v", err)
	}
	session, _, _ = service.Store().Checkout(ctx, customerID)
	orderID, err := service.Confirm(ctx, session)
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	recorded, err := service.Store().RecordedOrderProvider(ctx, customerID, orderID)
	if err != nil || recorded != "yookassa" {
		t.Fatalf("recorded provider = %q, %v; want yookassa", recorded, err)
	}
	order, _, err := service.Order(ctx, customerID, orderID, "en")
	if err != nil {
		t.Fatalf("read order: %v", err)
	}
	choices, err := service.OrderPaymentChoices(ctx, customerID, order)
	if err != nil {
		t.Fatalf("order payment choices: %v", err)
	}
	if len(choices) != 1 || choices[0].Provider != "yookassa" || choices[0].AmountMinor != order.ExternalMinor {
		t.Fatalf("order payment choices = %+v; want yookassa alone, priced at what is owed", choices)
	}

	// A method that cannot settle RUB is refused before the provider is asked.
	if _, err = service.StartOrderPayment(ctx, customerID, orderID, "en", "cryptobot"); !errors.Is(err, accountcheckout.ErrProviderCurrency) {
		t.Fatalf("a USD-only method was accepted for a RUB order: %v", err)
	}
	if usd.created != 0 {
		t.Fatal("the refused method was still asked to create a payment")
	}
	if _, err = service.StartOrderPayment(ctx, customerID, orderID, "en", "nonexistent"); !errors.Is(err, accountcheckout.ErrProviderUnavailable) {
		t.Fatalf("an unknown method was not refused as unavailable: %v", err)
	}

	// No method named: the recorded one is used, and the same call twice resumes
	// the same intent.
	first, err := service.StartOrderPayment(ctx, customerID, orderID, "en", "")
	if err != nil {
		t.Fatalf("start payment with the recorded method: %v", err)
	}
	if first.Provider != "yookassa" || first.Handoff != accountcheckout.HandoffHosted {
		t.Fatalf("payment handle = %+v; want yookassa / hosted", first)
	}
	second, err := service.StartOrderPayment(ctx, customerID, orderID, "en", "")
	if err != nil {
		t.Fatalf("resume payment: %v", err)
	}
	if second.ID != first.ID || rub.created != 1 {
		t.Fatalf("a second start created another intent (%s vs %s, %d creates)", second.ID, first.ID, rub.created)
	}

	// Past the order's expiry the same button answers a conflict, whether or
	// not the sweep has already flipped the state.
	clock = order.ExpiresAt.Add(time.Minute)
	if _, err = service.StartOrderPayment(ctx, customerID, orderID, "en", ""); !errors.Is(err, accountcheckout.ErrOrderNotPayable) {
		t.Fatalf("an expired order accepted a payment: %v", err)
	}
	if _, err = harness.pool.Exec(ctx, `UPDATE orders SET state = 'expired' WHERE id = $1::uuid`, orderID); err != nil {
		t.Fatalf("expire order: %v", err)
	}
	if _, err = service.StartOrderPayment(ctx, customerID, orderID, "en", ""); !errors.Is(err, accountcheckout.ErrOrderNotPayable) {
		t.Fatalf("a swept order accepted a payment: %v", err)
	}
	expired, _, _ := service.Order(ctx, customerID, orderID, "en")
	if expired.State != commerce.OrderExpired {
		t.Fatalf("order state = %s", expired.State)
	}
	if got, _ := service.OrderPaymentChoices(ctx, customerID, expired); len(got) != 0 {
		t.Fatalf("an expired order still offers %d payment methods", len(got))
	}
}
