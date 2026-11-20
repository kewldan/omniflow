//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/omniflow/omniflow/internal/accountcheckout"
	"github.com/omniflow/omniflow/internal/payments"
	"github.com/omniflow/omniflow/internal/paymentservice"
)

// multiMethodProvider is an adapter configured with two methods, as FreeKassa
// is with APP_FREEKASSA_PAYMENT_METHODS=sbp,card.
type multiMethodProvider struct {
	hostedStubProvider
	methods []string
}

func (provider *multiMethodProvider) Methods() []string { return []string{"sbp", "card"} }

func (provider *multiMethodProvider) Create(ctx context.Context, request payments.CreateRequest) (payments.Intent, error) {
	provider.methods = append(provider.methods, request.Method)
	return provider.hostedStubProvider.Create(ctx, request)
}

// Each method is its own button; the operator's settings rename, bound, hide,
// and target them; the panel's provider switch removes a provider from the
// customer list; and a button the customer was not offered is refused when
// pressed anyway.
func TestPaymentMethodSettingsDriveTheCustomerButtons(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	multi := &multiMethodProvider{hostedStubProvider: hostedStubProvider{name: "freekassa", currency: "RUB"}}
	other := &hostedStubProvider{name: "cryptobot", currency: "RUB"}
	service := paymentservice.New(harness.pool, store, multi, other)
	checkout, err := accountcheckout.New(harness.pool, store, service, accountcheckout.Options{
		Logger:   slog.New(slog.DiscardHandler),
		Settings: accountcheckout.Settings{Currency: "RUB", PublicURL: "https://example.test"},
	})
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	keys := func() []string {
		service.InvalidateOptions()
		options := service.Options()
		out := make([]string, 0, len(options))
		for _, option := range options {
			out = append(out, option.Key)
		}
		return out
	}
	if got := keys(); len(got) != 3 || got[0] != "cryptobot" && got[0] != "freekassa.sbp" {
		t.Fatalf("buttons = %v, want cryptobot and both freekassa methods", got)
	}

	// The panel's provider switch now decides visibility and order.
	if _, err = harness.pool.Exec(ctx, `INSERT INTO payment_provider_settings (provider, merchant_id, enabled, display_order)
		VALUES ('cryptobot', '', false, 0), ('freekassa', '', true, 1)`); err != nil {
		t.Fatalf("provider settings: %v", err)
	}
	if got := keys(); len(got) != 2 || got[0] != "freekassa.sbp" || got[1] != "freekassa.card" {
		t.Fatalf("buttons after disabling cryptobot = %v", got)
	}

	// SBP: renamed, at least 100.00, quick amounts; card: email sign-ins only.
	if _, err = harness.pool.Exec(ctx, `INSERT INTO payment_method_settings
		(provider, method, enabled, display_order, name_ru, name_en, min_minor, quick_amounts_minor)
		VALUES ('freekassa', 'sbp', true, 0, 'СБП', 'SBP', 10000, '{10000,50000}')`); err != nil {
		t.Fatalf("sbp settings: %v", err)
	}
	if _, err = harness.pool.Exec(ctx, `INSERT INTO payment_method_settings
		(provider, method, enabled, display_order, login_kinds)
		VALUES ('freekassa', 'card', true, 1, '{email}')`); err != nil {
		t.Fatalf("card settings: %v", err)
	}
	service.InvalidateOptions()

	customerID := harness.customer(ctx, t)
	wallet, err := checkout.Wallet(ctx, customerID)
	if err != nil {
		t.Fatalf("wallet: %v", err)
	}
	if len(wallet.Providers) != 1 || wallet.Providers[0].Key != "freekassa.sbp" ||
		wallet.Providers[0].Presentation.Name("en") != "SBP" || len(wallet.Providers[0].Presentation.QuickAmountsMinor) != 2 {
		t.Fatalf("a customer without an e-mail sign-in sees %+v", wallet.Providers)
	}

	// The SBP button opens a payment with the SBP method.
	if _, err = checkout.StartTopUpWith(ctx, customerID, "RUB", 20000, accountcheckout.PaymentSelection{Provider: "freekassa", Method: "sbp"}, "methods-sbp-0001"); err != nil {
		t.Fatalf("sbp top-up: %v", err)
	}
	if len(multi.methods) != 1 || multi.methods[0] != "sbp" {
		t.Fatalf("the adapter was asked for methods %v", multi.methods)
	}
	var stored string
	if err = harness.pool.QueryRow(ctx, `SELECT method FROM payment_intents WHERE idempotency_key LIKE 'order-%-freekassa-sbp'`).Scan(&stored); err != nil || stored != "sbp" {
		t.Fatalf("stored method = %q %v", stored, err)
	}

	// Below the SBP minimum, and the card button this customer was not shown.
	if _, err = checkout.StartTopUpWith(ctx, customerID, "RUB", 5000, accountcheckout.PaymentSelection{Provider: "freekassa", Method: "sbp"}, "methods-low-0001"); !errors.Is(err, accountcheckout.ErrProviderAmount) {
		t.Fatalf("an amount under the minimum: %v", err)
	}
	if _, err = checkout.StartTopUpWith(ctx, customerID, "RUB", 20000, accountcheckout.PaymentSelection{Provider: "freekassa", Method: "card"}, "methods-card-0001"); !errors.Is(err, accountcheckout.ErrProviderUnavailable) {
		t.Fatalf("a hidden button was accepted: %v", err)
	}
	if _, err = checkout.StartTopUpWith(ctx, customerID, "RUB", 20000, accountcheckout.PaymentSelection{Provider: "cryptobot"}, "methods-off-0001"); !errors.Is(err, accountcheckout.ErrProviderUnavailable) {
		t.Fatalf("a provider the operator switched off was accepted: %v", err)
	}

	// A first-top-up-only button disappears once a top-up was paid.
	if _, err = harness.pool.Exec(ctx, `UPDATE payment_method_settings SET topup_audience = 'first_topup' WHERE method = 'sbp'`); err != nil {
		t.Fatalf("audience: %v", err)
	}
	if _, err = harness.pool.Exec(ctx, `UPDATE orders SET paid_minor = subtotal_minor, state = 'paid' WHERE user_id = $1::uuid AND operation = 'topup'`, customerID); err != nil {
		t.Fatalf("mark paid: %v", err)
	}
	service.InvalidateOptions()
	if wallet, err = checkout.Wallet(ctx, customerID); err != nil {
		t.Fatalf("wallet: %v", err)
	}
	if len(wallet.Providers) != 0 {
		t.Fatalf("a returning customer still sees %+v", wallet.Providers)
	}
}
