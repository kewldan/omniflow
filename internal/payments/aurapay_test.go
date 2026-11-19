package payments

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/omniflow/omniflow/internal/commerce"
)

// The vector below was produced with Bedolaga's verification code: values
// sorted by key, converted with str() (None as ""), concatenated, and
// HMAC-SHA256'd with "aura-secret".
const (
	aurapayTestBody      = `{"id":"inv_42","order_id":"6f1c1f3e-8a7b-4c2d-9e0f-112233445566","status":"PAID","amount":1499.0,"service":"sbp","payer_details":{"card":"220000******1234","bank":"Сбер","name":"O'Neil","tags":[1,2.5,true,null,"x y"]},"custom_fields":null,"test":false,"commission":"0.00"}`
	aurapayTestMessage   = `1499.00.00inv_426f1c1f3e-8a7b-4c2d-9e0f-112233445566{'card': '220000******1234', 'bank': 'Сбер', 'name': "O'Neil", 'tags': [1, 2.5, True, None, 'x\xa0y']}sbpPAIDFalse`
	aurapayTestSignature = "775d7d0f7253da68117595388077d72ad7f536c6c02bd3f4b9467eb58a727350"
)

func aurapayTestProvider(t *testing.T, extra map[string]string, handler http.HandlerFunc) *AuraPay {
	t.Helper()
	values := map[string]string{
		"APP_AURAPAY_API_KEY": "aura-key", "APP_AURAPAY_SHOP_ID": "5d1c7e2a-0000-4000-8000-000000000001",
		"APP_AURAPAY_SECRET_KEY": "aura-secret",
	}
	for key, value := range extra {
		values[key] = value
	}
	built, err := buildAuraPay(freekassaTestEnv(values))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	provider := built.(*AuraPay)
	if handler != nil {
		server := httptest.NewServer(handler)
		t.Cleanup(server.Close)
		provider.baseURL = server.URL
	}
	return provider
}

func aurapayTestSign(body string) http.Header {
	message, _ := aurapaySignedMessage([]byte(body))
	mac := hmac.New(sha256.New, []byte("aura-secret"))
	_, _ = mac.Write([]byte(message))
	headers := http.Header{}
	headers.Set("X-SIGNATURE", hex.EncodeToString(mac.Sum(nil)))
	return headers
}

func TestAuraPayBuilderReadsItsEnvironment(t *testing.T) {
	if _, err := buildAuraPay(freekassaTestEnv(map[string]string{})); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("no credentials must leave the adapter unregistered, got %v", err)
	}
	_, err := buildAuraPay(freekassaTestEnv(map[string]string{"APP_AURAPAY_API_KEY": "k", "APP_AURAPAY_SHOP_ID": "s"}))
	if err == nil || errors.Is(err, ErrNotConfigured) || !strings.Contains(err.Error(), "APP_AURAPAY_SECRET_KEY") {
		t.Fatalf("a partial configuration must name what is missing, got %v", err)
	}
	for _, extra := range []map[string]string{{"APP_AURAPAY_PAYMENT_METHOD": "crypto"}, {"APP_AURAPAY_PAYMENT_LIFETIME_MINUTES": "0"}} {
		values := map[string]string{"APP_AURAPAY_API_KEY": "k", "APP_AURAPAY_SHOP_ID": "s", "APP_AURAPAY_SECRET_KEY": "x"}
		for key, value := range extra {
			values[key] = value
		}
		if _, err := buildAuraPay(freekassaTestEnv(values)); err == nil {
			t.Fatalf("%v must be rejected", extra)
		}
	}
	provider := aurapayTestProvider(t, nil, nil)
	if provider.lifetime != 60 || provider.callbackURL != "https://shop.example.com/v1/payments/webhooks/aurapay" || provider.Name() != "aurapay" {
		t.Fatalf("unexpected adapter %#v", provider)
	}
	if !provider.SupportsCurrency("RUB") || provider.SupportsCurrency("USD") {
		t.Fatal("only the configured currency is supported")
	}
	if caps := provider.Capabilities(); caps.Refunds || caps.Recurring || !caps.Webhooks || !caps.Polling {
		t.Fatalf("unexpected capabilities %#v", caps)
	}
}

func TestAuraPayCreateAndPoll(t *testing.T) {
	var body map[string]json.RawMessage
	pollStatus := "PENDING"
	provider := aurapayTestProvider(t, map[string]string{"APP_AURAPAY_PAYMENT_METHOD": "card"}, func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-ApiKey") != "aura-key" || request.Header.Get("X-ShopId") != "5d1c7e2a-0000-4000-8000-000000000001" {
			t.Errorf("missing auth headers %v", request.Header)
		}
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/invoice/create":
			_ = json.NewDecoder(request.Body).Decode(&body)
			_, _ = writer.Write([]byte(`{"id":"inv_42","status":"PENDING","payment_data":{"url":"https://app.aurapay.tech/pay/inv_42"},"expires_at":"2026-11-19T13:00:00+00:00"}`))
		case request.Method == http.MethodPost && request.URL.Path == "/invoice/status":
			var lookup map[string]string
			_ = json.NewDecoder(request.Body).Decode(&lookup)
			if lookup["id"] != "inv_42" {
				t.Errorf("unexpected lookup %v", lookup)
			}
			_, _ = writer.Write([]byte(`{"id":"inv_42","status":"` + pollStatus + `","amount":"1499.00"}`))
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
	want := map[string]string{
		"amount": `1499.00`, "order_id": `"` + freekassaTestOrder + `"`, "lifetime": `60`, "comment": `"Subscription"`,
		"service": `"card"`, "success_url": `"https://shop.example.com/account/orders/1"`,
		"fail_url": `"https://shop.example.com/account/orders/1"`, "callback_url": `"https://shop.example.com/v1/payments/webhooks/aurapay"`,
	}
	for key, value := range want {
		if string(body[key]) != value {
			t.Fatalf("%s: want %s, got %s", key, value, body[key])
		}
	}
	if intent.ProviderReference != "inv_42" || intent.Status != "pending" || intent.CheckoutURL != "https://app.aurapay.tech/pay/inv_42" || intent.ExpiresAt == nil {
		t.Fatalf("unexpected intent %#v", intent)
	}
	for status, normalized := range map[string]string{"PENDING": "pending", "PAID": "succeeded", "EXPIRED": "expired", "HELD": "pending"} {
		pollStatus = status
		polled, err := provider.Poll(t.Context(), "inv_42")
		if err != nil || polled.Status != normalized || polled.Amount != (commerce.Money{Amount: 149900, Currency: "RUB"}) {
			t.Fatalf("%s must map to %s: %#v %v", status, normalized, polled, err)
		}
	}
}

func TestAuraPayWebhookConcatenatesValuesLikeBedolaga(t *testing.T) {
	message, err := aurapaySignedMessage([]byte(aurapayTestBody))
	if err != nil || message != aurapayTestMessage {
		t.Fatalf("signed message differs from Python's\n got %s\nwant %s (%v)", message, aurapayTestMessage, err)
	}
	provider := aurapayTestProvider(t, nil, nil)
	headers := http.Header{}
	headers.Set("X-SIGNATURE", aurapayTestSignature)
	event, err := provider.VerifyWebhook(headers, []byte(aurapayTestBody))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if event.ID != "PAID:inv_42" || event.Status != "succeeded" || event.ProviderReference != "inv_42" ||
		event.MerchantReference != freekassaTestOrder || event.Amount != (commerce.Money{Amount: 149900, Currency: "RUB"}) {
		t.Fatalf("unexpected event %#v", event)
	}
	repeat, err := provider.VerifyWebhook(headers, []byte(aurapayTestBody))
	if err != nil || repeat.ID != event.ID {
		t.Fatalf("a redelivery must keep its event ID: %v", err)
	}
	if _, err := provider.VerifyWebhook(headers, []byte(strings.Replace(aurapayTestBody, "1499.0", "14.99", 1))); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("a tampered amount must be rejected, got %v", err)
	}
	if _, err := provider.VerifyWebhook(http.Header{}, []byte(aurapayTestBody)); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("a missing signature must be rejected, got %v", err)
	}
	unknown := `{"id":"inv_43","order_id":"x","status":"REFUNDED","amount":"10.00"}`
	if event, err := provider.VerifyWebhook(aurapayTestSign(unknown), []byte(unknown)); err != nil || event.Status != "pending" {
		t.Fatalf("an unknown status must not settle: %#v %v", event, err)
	}
	if status, _, ack := provider.WebhookAck("paid"); status != http.StatusOK || string(ack) != `{"status":true}` {
		t.Fatalf("unexpected ack %d %s", status, ack)
	}
}

func TestAuraPayErrorsSeparateRefusalsFromOutagesAndRefundsAreUnsupported(t *testing.T) {
	status := http.StatusBadRequest
	provider := aurapayTestProvider(t, nil, func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(status)
	})
	if _, err := provider.Poll(t.Context(), "inv_42"); !Refused(err) {
		t.Fatalf("a 4xx is a refusal, got %v", err)
	}
	status = http.StatusServiceUnavailable
	if _, err := provider.Poll(t.Context(), "inv_42"); err == nil || Refused(err) {
		t.Fatalf("a 5xx is not a refusal, got %v", err)
	}
	if _, err := provider.Refund(t.Context(), RefundRequest{}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("refunds are unsupported, got %v", err)
	}
}
