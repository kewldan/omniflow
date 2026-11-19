package payments

import (
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/omniflow/omniflow/internal/commerce"
)

const (
	riopayTestOrderID = "0b6f5a3e-1c2d-4e5f-8a9b-0c1d2e3f4a5b"
	riopayTestBody    = `{"id":"0b6f5a3e-1c2d-4e5f-8a9b-0c1d2e3f4a5b","externalId":"6f1c1f3e-8a7b-4c2d-9e0f-112233445566","status":"COMPLETED","amount":"1499.00","currency":"RUB","paymentType":"SBP","includedFee":"0.00"}`
	// HMAC-SHA512("rio-webhook-secret", riopayTestBody), Bedolaga's formula.
	riopayTestSignature = "8c91735d1a2899a3ff7f296209a53d0c3de4c25b0655e8c17ed033451de24becef380cf1f6c14e22327e08a396a3f7a4d6fbbfff0ae3aed1118f08f2d406548b"
)

func riopayTestProvider(t *testing.T, values map[string]string, handler http.HandlerFunc) *RioPay {
	t.Helper()
	built, err := buildRioPay(freekassaTestEnv(values))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	provider := built.(*RioPay)
	if handler != nil {
		server := httptest.NewServer(handler)
		t.Cleanup(server.Close)
		provider.baseURL = server.URL
	}
	return provider
}

func TestRioPayBuilderReadsItsEnvironment(t *testing.T) {
	if _, err := buildRioPay(freekassaTestEnv(map[string]string{})); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("no credentials must leave the adapter unregistered, got %v", err)
	}
	_, err := buildRioPay(freekassaTestEnv(map[string]string{"APP_RIOPAY_WEBHOOK_SECRET": "secret"}))
	if err == nil || errors.Is(err, ErrNotConfigured) || !strings.Contains(err.Error(), "APP_RIOPAY_API_TOKEN") {
		t.Fatalf("a webhook secret without a token is a partial configuration, got %v", err)
	}
	provider := riopayTestProvider(t, map[string]string{"APP_RIOPAY_API_TOKEN": "token"}, nil)
	if provider.webhookSecret != "token" || provider.currency != "RUB" || provider.Name() != "riopay" {
		t.Fatalf("the webhook secret defaults to the API token: %#v", provider)
	}
	if !provider.SupportsCurrency("RUB") || provider.SupportsCurrency("EUR") {
		t.Fatal("only the configured currency is supported")
	}
	if caps := provider.Capabilities(); caps.Refunds || caps.Recurring || !caps.Webhooks || !caps.Polling {
		t.Fatalf("unexpected capabilities %#v", caps)
	}
}

func TestRioPayCreateAndPoll(t *testing.T) {
	var body map[string]string
	pollStatus := "PENDING"
	provider := riopayTestProvider(t, map[string]string{"APP_RIOPAY_API_TOKEN": "token", "APP_RIOPAY_SUCCESS_URL": "https://fallback.example"}, func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-Api-Token") != "token" {
			t.Errorf("missing API token header")
		}
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/v1/orders":
			_ = json.NewDecoder(request.Body).Decode(&body)
			writer.WriteHeader(http.StatusCreated)
			_, _ = writer.Write([]byte(`{"id":"` + riopayTestOrderID + `","status":"CREATED","paymentLink":"https://pay.riopay.online/` + riopayTestOrderID + `","amount":"1499.00","currency":"RUB"}`))
		case request.Method == http.MethodGet && request.URL.Path == "/v1/orders/"+riopayTestOrderID:
			_, _ = writer.Write([]byte(`{"id":"` + riopayTestOrderID + `","status":"` + pollStatus + `","amount":1499,"currency":"RUB"}`))
		default:
			t.Errorf("unexpected request %s %s", request.Method, request.URL.Path)
			writer.WriteHeader(http.StatusNotFound)
		}
	})
	intent, err := provider.Create(t.Context(), CreateRequest{
		OrderID: freekassaTestOrder, Amount: commerce.Money{Amount: 149900, Currency: "RUB"},
		Description: "Subscription", ReturnURL: "https://shop.example.com/account/orders/1",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if body["amount"] != "1499.00" || body["externalId"] != freekassaTestOrder || body["purpose"] != "Subscription" || body["successUrl"] != "https://shop.example.com/account/orders/1" {
		t.Fatalf("unexpected create body %v", body)
	}
	if intent.ProviderReference != riopayTestOrderID || intent.Status != "pending" || intent.Amount.Amount != 149900 || !strings.HasPrefix(intent.CheckoutURL, "https://pay.riopay.online/") {
		t.Fatalf("unexpected intent %#v", intent)
	}
	for status, want := range map[string]string{"PENDING": "pending", "COMPLETED": "succeeded", "CANCELED": "cancelled", "FAILED": "failed", "EXPIRED": "expired", "REFUNDED": "pending"} {
		pollStatus = status
		polled, err := provider.Poll(t.Context(), riopayTestOrderID)
		if err != nil || polled.Status != want {
			t.Fatalf("%s must map to %s: %#v %v", status, want, polled, err)
		}
		if status == "COMPLETED" && polled.Amount != (commerce.Money{Amount: 149900, Currency: "RUB"}) {
			t.Fatalf("a completed order carries its amount: %#v", polled)
		}
	}
}

func TestRioPayWebhookVerifiesHMACSHA512(t *testing.T) {
	provider := riopayTestProvider(t, map[string]string{"APP_RIOPAY_API_TOKEN": "token", "APP_RIOPAY_WEBHOOK_SECRET": "rio-webhook-secret"}, nil)
	headers := http.Header{}
	headers.Set("X-Signature", riopayTestSignature)
	event, err := provider.VerifyWebhook(headers, []byte(riopayTestBody))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if event.ID != "COMPLETED:"+riopayTestOrderID || event.Status != "succeeded" || event.ProviderReference != riopayTestOrderID ||
		event.MerchantReference != freekassaTestOrder || event.Amount != (commerce.Money{Amount: 149900, Currency: "RUB"}) {
		t.Fatalf("unexpected event %#v", event)
	}
	repeat, err := provider.VerifyWebhook(headers, []byte(riopayTestBody))
	if err != nil || repeat.ID != event.ID {
		t.Fatalf("a redelivery must keep its event ID: %v", err)
	}
	tampered := strings.Replace(riopayTestBody, "1499.00", "14.99", 1)
	if _, err := provider.VerifyWebhook(headers, []byte(tampered)); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("a tampered amount must be rejected, got %v", err)
	}
	if _, err := provider.VerifyWebhook(http.Header{}, []byte(riopayTestBody)); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("a missing signature must be rejected, got %v", err)
	}
	other := riopayTestProvider(t, map[string]string{"APP_RIOPAY_API_TOKEN": "token"}, nil)
	if _, err := other.VerifyWebhook(headers, []byte(riopayTestBody)); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("a signature under another key must be rejected, got %v", err)
	}
	unknown := strings.Replace(riopayTestBody, "COMPLETED", "ON_HOLD", 1)
	mac := riopayTestSign("rio-webhook-secret", unknown)
	headers.Set("X-Signature", mac)
	if event, err := provider.VerifyWebhook(headers, []byte(unknown)); err != nil || event.Status != "pending" {
		t.Fatalf("an unknown status must not settle: %#v %v", event, err)
	}
	if status, _, ack := provider.WebhookAck("paid"); status != http.StatusOK || string(ack) != `{"status":"ok"}` {
		t.Fatalf("unexpected ack %d %s", status, ack)
	}
}

func riopayTestSign(secret, body string) string {
	mac := hmac.New(sha512.New, []byte(secret))
	_, _ = mac.Write([]byte(body))
	return hex.EncodeToString(mac.Sum(nil))
}

func TestRioPayErrorsSeparateRefusalsFromOutagesAndRefundsAreUnsupported(t *testing.T) {
	status := http.StatusUnauthorized
	provider := riopayTestProvider(t, map[string]string{"APP_RIOPAY_API_TOKEN": "token"}, func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(status)
	})
	create := CreateRequest{OrderID: freekassaTestOrder, Amount: commerce.Money{Amount: 100, Currency: "RUB"}}
	if _, err := provider.Create(t.Context(), create); !Refused(err) {
		t.Fatalf("a 4xx is a refusal, got %v", err)
	}
	status = http.StatusServiceUnavailable
	if _, err := provider.Create(t.Context(), create); err == nil || Refused(err) {
		t.Fatalf("a 5xx is not a refusal, got %v", err)
	}
	if _, err := provider.Refund(t.Context(), RefundRequest{}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("refunds are unsupported, got %v", err)
	}
}
