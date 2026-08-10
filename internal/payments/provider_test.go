package payments

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"testing"

	"github.com/omniflow/omniflow/internal/commerce"
)

func TestCryptoBotWebhookSignatureAndReplayKey(t *testing.T) {
	provider, err := NewCryptoBot("token", true)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"update_id":9,"update_type":"invoice_paid","request_date":"2026-08-10T00:00:00Z","payload":{"invoice_id":42,"status":"paid","amount":"10.50","fiat":"USD"}}`)
	secret := sha256.Sum256([]byte("token"))
	mac := hmac.New(sha256.New, secret[:])
	_, _ = mac.Write(body)
	headers := make(http.Header)
	headers.Set("crypto-pay-api-signature", hex.EncodeToString(mac.Sum(nil)))
	event, err := provider.VerifyWebhook(headers, body)
	if err != nil {
		t.Fatal(err)
	}
	if event.ID != "invoice_paid:42:9" || event.Amount.Amount != 1050 || event.Status != "succeeded" {
		t.Fatalf("unexpected event: %#v", event)
	}
	replayed, err := provider.VerifyWebhook(headers, body)
	if err != nil || replayed.ID != event.ID {
		t.Fatalf("same event must produce the same replay key: %#v %v", replayed, err)
	}
	headers.Set("crypto-pay-api-signature", "00")
	if _, err := provider.VerifyWebhook(headers, body); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("expected invalid signature, got %v", err)
	}
}

func TestTelegramStarsRejectsNonStars(t *testing.T) {
	provider := &TelegramStars{}
	_, err := provider.Create(t.Context(), CreateRequest{OrderID: "order", Amount: commerce.Money{Amount: 10, Currency: "RUB"}})
	if !errors.Is(err, commerce.ErrCurrencyMismatch) {
		t.Fatalf("expected currency rejection, got %v", err)
	}
	if provider.SupportsCurrency("RUB") || !provider.SupportsCurrency("xtr") {
		t.Fatal("the Stars adapter must accept XTR and nothing else")
	}
	if provider.Capabilities().Refunds {
		t.Fatal("an adapter without a bot token must not advertise refunds")
	}
}

func TestSupportsCurrencyFallsBackToTheAdapterDefault(t *testing.T) {
	cryptobot, err := NewCryptoBot("token", true)
	if err != nil {
		t.Fatal(err)
	}
	if !SupportsCurrency(cryptobot, "USD") || SupportsCurrency(cryptobot, "XTR") {
		t.Fatal("CryptoBot must settle fiat currencies and reject Telegram Stars")
	}
	yookassa, err := NewYooKassa("shop", "secret")
	if err != nil {
		t.Fatal(err)
	}
	if !SupportsCurrency(yookassa, "RUB") || SupportsCurrency(yookassa, "JPY") {
		t.Fatal("YooKassa must settle RUB and reject unsupported currencies")
	}
	if !SupportsCurrency(Manual{}, "RUB") || !SupportsCurrency(Manual{}, "USD") {
		t.Fatal("the manual adapter must accept any currency an operator records")
	}
}

func TestProviderAmountParsingRejectsExtraPrecision(t *testing.T) {
	if _, err := parseMinor("1.001", 2); err == nil {
		t.Fatal("expected precision error")
	}
}

func TestProviderAmountsUseCurrencyExponent(t *testing.T) {
	for _, test := range []struct {
		currency string
		minor    int64
		text     string
	}{
		{currency: "JPY", minor: 42, text: "42"},
		{currency: "RUB", minor: 4205, text: "42.05"},
		{currency: "KWD", minor: 42005, text: "42.005"},
	} {
		exponent, err := currencyExponent(test.currency)
		if err != nil {
			t.Fatal(err)
		}
		if got := formatMinor(test.minor, exponent); got != test.text {
			t.Fatalf("%s: got %q, want %q", test.currency, got, test.text)
		}
		parsed, err := parseMinor(test.text, exponent)
		if err != nil || parsed != test.minor {
			t.Fatalf("%s: parsed %d: %v", test.currency, parsed, err)
		}
	}
}
