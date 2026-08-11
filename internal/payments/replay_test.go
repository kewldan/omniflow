package payments

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// The fixtures under testdata are recorded shapes of provider notifications with
// every real identifier replaced. Replaying them keeps webhook parsing honest
// without a sandbox account and without ever committing a real payment payload.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return body
}

func cryptoBotSignature(token string, body []byte) string {
	secret := sha256.Sum256([]byte(token))
	mac := hmac.New(sha256.New, secret[:])
	_, _ = mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestCryptoBotWebhookReplayIsVerifiedAndParsed(t *testing.T) {
	t.Parallel()
	const token = "12345:test-cryptobot-token"
	provider, err := NewCryptoBot(token, true)
	if err != nil {
		t.Fatalf("configure provider: %v", err)
	}
	body := fixture(t, "cryptobot_invoice_paid.json")
	headers := http.Header{}
	headers.Set("crypto-pay-api-signature", cryptoBotSignature(token, body))

	event, err := provider.VerifyWebhook(headers, body)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if event.Status != "succeeded" {
		t.Fatalf("expected a succeeded status, got %q", event.Status)
	}
	if event.Amount.Currency != "RUB" || event.Amount.Amount != 149900 {
		t.Fatalf("expected 149900 RUB in minor units, got %d %s", event.Amount.Amount, event.Amount.Currency)
	}
	if event.ProviderReference != "987654" {
		t.Fatalf("expected the invoice ID as the provider reference, got %q", event.ProviderReference)
	}
	// The event identifier is what deduplicates a redelivered webhook, so it
	// must be stable across replays of the same body.
	repeat, err := provider.VerifyWebhook(headers, body)
	if err != nil || repeat.ID != event.ID {
		t.Fatalf("replaying the same body must yield the same event ID: %q vs %q (%v)", repeat.ID, event.ID, err)
	}
}

func TestCryptoBotWebhookRejectsATamperedBody(t *testing.T) {
	t.Parallel()
	const token = "12345:test-cryptobot-token"
	provider, err := NewCryptoBot(token, true)
	if err != nil {
		t.Fatalf("configure provider: %v", err)
	}
	body := fixture(t, "cryptobot_invoice_paid.json")
	headers := http.Header{}
	headers.Set("crypto-pay-api-signature", cryptoBotSignature(token, body))

	tampered := append([]byte(nil), body...)
	tampered[len(tampered)-2] = ' '
	if _, err := provider.VerifyWebhook(headers, tampered); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("expected a signature rejection, got %v", err)
	}
	// A signature computed with another token must be rejected as well.
	otherHeaders := http.Header{}
	otherHeaders.Set("crypto-pay-api-signature", cryptoBotSignature("99999:another-token", body))
	if _, err := provider.VerifyWebhook(otherHeaders, body); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("expected a signature rejection, got %v", err)
	}
	if _, err := provider.VerifyWebhook(http.Header{}, body); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("a missing signature must be rejected, got %v", err)
	}
}

func TestYooKassaWebhookReplayIsParsedAsUntrustedHint(t *testing.T) {
	t.Parallel()
	provider, err := NewYooKassa("100500", "test_secret_value")
	if err != nil {
		t.Fatalf("configure provider: %v", err)
	}
	body := fixture(t, "yookassa_payment_succeeded.json")
	event, err := provider.VerifyWebhook(http.Header{}, body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if event.ProviderReference != "2f0a1b3c-000f-5000-9000-1f7a0a0a0a0a" {
		t.Fatalf("unexpected provider reference %q", event.ProviderReference)
	}
	if event.Amount.Amount != 149900 || event.Amount.Currency != "RUB" {
		t.Fatalf("expected 149900 RUB in minor units, got %d %s", event.Amount.Amount, event.Amount.Currency)
	}
	if event.Status != "succeeded" {
		t.Fatalf("expected a succeeded status, got %q", event.Status)
	}
}

func TestYooKassaWebhookRejectsAMalformedEnvelope(t *testing.T) {
	t.Parallel()
	provider, err := NewYooKassa("100500", "test_secret_value")
	if err != nil {
		t.Fatalf("configure provider: %v", err)
	}
	for _, body := range [][]byte{
		[]byte(`{"type":"not-a-notification","event":"payment.succeeded","object":{"id":"x"}}`),
		[]byte(`{"type":"notification","event":"payment.succeeded","object":{}}`),
		[]byte(`not json`),
	} {
		if _, err := provider.VerifyWebhook(http.Header{}, body); err == nil {
			t.Fatalf("expected a rejection for %s", body)
		}
	}
}
