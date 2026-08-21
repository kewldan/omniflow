package payments

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// cryptoBotAt points an adapter at a test server standing in for Crypto Pay.
func cryptoBotAt(t *testing.T, handler http.HandlerFunc) *CryptoBot {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	provider, err := NewCryptoBot("token", true)
	if err != nil {
		t.Fatal(err)
	}
	provider.baseURL = server.URL
	return provider
}

// Cancelling an order withdraws its CryptoBot invoice so an open checkout tab
// cannot pay it afterwards. The call is idempotent from the caller's side: an
// invoice that is already gone is the outcome wanted, not a failure.
func TestCryptoBotCancelDeletesTheInvoiceAndToleratesAMissingOne(t *testing.T) {
	var seenMethod, seenInvoice string
	deleted := cryptoBotAt(t, func(writer http.ResponseWriter, request *http.Request) {
		_ = request.ParseForm()
		seenMethod, seenInvoice = request.URL.Path, request.Form.Get("invoice_id")
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"ok":true,"result":true}`))
	})
	if err := deleted.CancelIntent(context.Background(), "42"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if seenMethod != "/deleteInvoice" || seenInvoice != "42" {
		t.Fatalf("expected deleteInvoice for invoice 42, got %s %s", seenMethod, seenInvoice)
	}

	missing := cryptoBotAt(t, func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = writer.Write([]byte(`{"ok":false,"error":{"code":400,"name":"INVOICE_NOT_FOUND"}}`))
	})
	if err := missing.CancelIntent(context.Background(), "42"); err != nil {
		t.Fatalf("an invoice that is already gone must not be an error: %v", err)
	}

	// Any other refusal is reported, so the caller knows the checkout may
	// still accept money and a late payment remains possible.
	refused := cryptoBotAt(t, func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = writer.Write([]byte(`{"ok":false,"error":{"code":400,"name":"INVOICE_PAID"}}`))
	})
	if err := refused.CancelIntent(context.Background(), "42"); !errors.Is(err, ErrProviderResponse) {
		t.Fatalf("a refused deletion must surface as a provider error, got %v", err)
	}
	down := cryptoBotAt(t, func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusBadGateway)
	})
	if err := down.CancelIntent(context.Background(), "42"); !errors.Is(err, ErrProviderResponse) {
		t.Fatalf("an unavailable provider must surface as an error, got %v", err)
	}
}

// Only the adapters with something to withdraw implement cancellation. A Stars
// invoice link cannot be revoked and a captured YooKassa payment has nothing to
// cancel, so neither may advertise the capability; a manual transfer has no
// provider object and cancels trivially.
func TestCancellationIsOfferedOnlyWhereTheProviderHasSomethingToWithdraw(t *testing.T) {
	cryptobot, _ := NewCryptoBot("token", true)
	yookassa, _ := NewYooKassa("shop", "secret")
	for name, provider := range map[string]Provider{
		"cryptobot": cryptobot, "manual": Manual{},
	} {
		if _, ok := provider.(Canceller); !ok {
			t.Errorf("%s must implement Canceller", name)
		}
	}
	for name, provider := range map[string]Provider{
		"telegram_stars": &TelegramStars{}, "yookassa": yookassa,
	} {
		if _, ok := provider.(Canceller); ok {
			t.Errorf("%s must not advertise a cancellation it cannot perform", name)
		}
	}
	if err := (Manual{}).CancelIntent(context.Background(), "order"); err != nil {
		t.Fatalf("manual cancellation: %v", err)
	}
}
