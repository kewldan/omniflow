//go:build integration

package integrationtest

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/omniflow/omniflow/internal/accountcheckout"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/payments"
	"github.com/omniflow/omniflow/internal/paymentservice"
)

// bindingProvider stands in for YooKassa with save_payment_method: it reports
// whether consent reached it, and its authenticated read of a settled payment
// names the token the bank allowed it to keep.
type bindingProvider struct {
	hostedStubProvider
	asked   []bool
	amounts map[string]commerce.Money
}

func (provider *bindingProvider) Capabilities() payments.Capabilities {
	return payments.Capabilities{Polling: true, Webhooks: true, Recurring: true}
}

func (provider *bindingProvider) WebhooksNeedPolling() bool { return true }

func (provider *bindingProvider) Create(ctx context.Context, request payments.CreateRequest) (payments.Intent, error) {
	provider.asked = append(provider.asked, request.SavePaymentMethod)
	if provider.amounts == nil {
		provider.amounts = map[string]commerce.Money{}
	}
	provider.amounts["ref-"+request.OrderID] = request.Amount
	return provider.hostedStubProvider.Create(ctx, request)
}

func (provider *bindingProvider) Poll(_ context.Context, reference string) (payments.Intent, error) {
	return payments.Intent{
		ProviderReference: reference, Status: "succeeded", Amount: provider.amounts[reference],
		BoundMethod: &payments.BoundMethod{Token: "pm-" + reference, Label: "Visa *4242"},
	}, nil
}

func (provider *bindingProvider) ChargeSaved(context.Context, payments.SavedChargeRequest) (payments.Intent, error) {
	return payments.Intent{}, payments.ErrUnsupported
}

func (provider *bindingProvider) VerifyWebhook(_ http.Header, body []byte) (payments.WebhookEvent, error) {
	reference, ok := strings.CutPrefix(string(body), "payment:")
	if !ok {
		return payments.WebhookEvent{}, payments.ErrProviderResponse
	}
	return payments.WebhookEvent{ID: "payment.succeeded:" + reference, ProviderReference: reference, Status: "succeeded"}, nil
}

// A card is kept only when the customer ticked the consent on that payment and
// the operator enabled recurring after a passing test; the binding happens on
// the authenticated read of the settled payment, once however often the
// settlement is reported; and a payment without consent never produces a
// saved method even if the provider offers a token.
func TestSavedMethodIsBoundOnlyWithConsentAndOnce(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	provider := &bindingProvider{hostedStubProvider: hostedStubProvider{name: "yookassa", currency: "RUB"}}
	payments := paymentservice.New(harness.pool, store, provider)
	checkout, err := accountcheckout.New(harness.pool, store, payments, accountcheckout.Options{
		Logger:   slog.New(slog.DiscardHandler),
		Settings: accountcheckout.Settings{Currency: "RUB", PublicURL: "https://example.test"},
	})
	if err != nil {
		t.Fatalf("build checkout: %v", err)
	}

	// Recurring is off: the consent is dropped before it reaches the provider.
	customerID := harness.customer(ctx, t)
	if _, err = checkout.StartTopUpWith(ctx, customerID, "RUB", 10000, accountcheckout.PaymentSelection{Provider: "yookassa", SaveMethod: true}, "save-method-off-0001"); err != nil {
		t.Fatalf("top-up without recurring: %v", err)
	}
	if len(provider.asked) != 1 || provider.asked[0] {
		t.Fatalf("consent reached the provider while recurring was off: %v", provider.asked)
	}

	if _, err = harness.pool.Exec(ctx, `INSERT INTO payment_provider_settings
		(provider, merchant_id, enabled, recurring_enabled, recurring_test_status, recurring_tested_at)
		VALUES ('yookassa', '', true, true, 'passed', now())`); err != nil {
		t.Fatalf("enable recurring: %v", err)
	}
	if !payments.RecurringAllowed(ctx, "yookassa") {
		t.Fatal("recurring is not allowed after a passing test")
	}

	// With consent.
	topUp, err := checkout.StartTopUpWith(ctx, customerID, "RUB", 20000, accountcheckout.PaymentSelection{Provider: "yookassa", SaveMethod: true}, "save-method-on-0001")
	if err != nil {
		t.Fatalf("top-up with consent: %v", err)
	}
	if !provider.asked[len(provider.asked)-1] {
		t.Fatal("consent did not reach the provider")
	}
	reference := "ref-" + topUp.OrderID
	for range 2 {
		// The second delivery is a duplicate; the reconciler below is a third
		// report of the same settlement.
		if _, err = payments.HandleWebhook(ctx, "yookassa", http.Header{}, []byte("payment:"+reference)); err != nil {
			t.Fatalf("webhook: %v", err)
		}
	}
	var intentID string
	if err = harness.pool.QueryRow(ctx, `SELECT id::text FROM payment_intents WHERE provider_reference = $1`, reference).Scan(&intentID); err != nil {
		t.Fatalf("read intent: %v", err)
	}
	_, _ = payments.Reconcile(ctx, intentID)

	var (
		count     int
		isDefault bool
		label     string
		source    string
	)
	if err = harness.pool.QueryRow(ctx, `SELECT count(*), bool_and(is_default), max(display_label), max(source_payment_intent_id::text)
		FROM payment_methods WHERE user_id = $1::uuid`, customerID).Scan(&count, &isDefault, &label, &source); err != nil {
		t.Fatalf("read methods: %v", err)
	}
	if count != 1 || !isDefault || label != "Visa *4242" || source != intentID {
		t.Fatalf("methods = %d default=%v label=%q source=%q, want one default bound from %s", count, isDefault, label, source, intentID)
	}

	// Without consent: the provider offers a token, and nothing is kept.
	other := harness.customer(ctx, t)
	plain, err := checkout.StartTopUpWith(ctx, other, "RUB", 20000, accountcheckout.PaymentSelection{Provider: "yookassa"}, "save-method-none-0001")
	if err != nil {
		t.Fatalf("top-up without consent: %v", err)
	}
	if _, err = payments.HandleWebhook(ctx, "yookassa", http.Header{}, []byte("payment:ref-"+plain.OrderID)); err != nil {
		t.Fatalf("webhook without consent: %v", err)
	}
	if err = harness.pool.QueryRow(ctx, `SELECT count(*) FROM payment_methods WHERE user_id = $1::uuid`, other).Scan(&count); err != nil {
		t.Fatalf("read methods: %v", err)
	}
	if count != 0 {
		t.Fatalf("a payment without consent bound %d methods", count)
	}
}
