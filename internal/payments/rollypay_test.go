package payments

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/omniflow/omniflow/internal/commerce"
)

const (
	rollypayTestBody      = `{"event_type":"payment.paid","payment_id":"pay_8f2c","order_id":"6f1c1f3e-8a7b-4c2d-9e0f-112233445566","status":"paid","amount":"1499.00","currency":"RUB"}`
	rollypayTestTimestamp = "1763553600"
	// HMAC-SHA256("rolly-secret", "<timestamp>.<body>"), Bedolaga's formula.
	rollypayTestSignature = "9457269d84586ebe480d6993d5f4ab9b522c88ad592affc3b3fe05c367547cc4"
)

func rollypayTestProvider(t *testing.T, extra map[string]string, handler http.HandlerFunc) *RollyPay {
	t.Helper()
	values := map[string]string{"APP_ROLLYPAY_API_KEY": "rolly-key", "APP_ROLLYPAY_SIGNING_SECRET": "rolly-secret"}
	for key, value := range extra {
		values[key] = value
	}
	built, err := buildRollyPay(freekassaTestEnv(values))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	provider := built.(*RollyPay)
	if handler != nil {
		server := httptest.NewServer(handler)
		t.Cleanup(server.Close)
		provider.baseURL = server.URL
	}
	return provider
}

func rollypayTestHeaders(timestamp, body string) http.Header {
	mac := hmac.New(sha256.New, []byte("rolly-secret"))
	_, _ = mac.Write([]byte(timestamp + "." + body))
	headers := http.Header{}
	headers.Set("X-Timestamp", timestamp)
	headers.Set("X-Signature", hex.EncodeToString(mac.Sum(nil)))
	return headers
}

func TestRollyPayBuilderReadsItsEnvironment(t *testing.T) {
	if _, err := buildRollyPay(freekassaTestEnv(map[string]string{})); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("no credentials must leave the adapter unregistered, got %v", err)
	}
	_, err := buildRollyPay(freekassaTestEnv(map[string]string{"APP_ROLLYPAY_API_KEY": "key"}))
	if err == nil || errors.Is(err, ErrNotConfigured) || !strings.Contains(err.Error(), "APP_ROLLYPAY_SIGNING_SECRET") {
		t.Fatalf("a partial configuration must name what is missing, got %v", err)
	}
	if _, err := buildRollyPay(freekassaTestEnv(map[string]string{"APP_ROLLYPAY_API_KEY": "key", "APP_ROLLYPAY_SIGNING_SECRET": "s", "APP_ROLLYPAY_PAYMENT_METHOD": "paypal"})); err == nil {
		t.Fatal("an unknown payment method must be rejected")
	}
	provider := rollypayTestProvider(t, nil, nil)
	if provider.Name() != "rollypay" || !provider.SupportsCurrency("RUB") || provider.SupportsCurrency("USD") {
		t.Fatalf("unexpected adapter %#v", provider)
	}
	if caps := provider.Capabilities(); caps.Refunds || caps.Recurring || !caps.Webhooks || !caps.Polling {
		t.Fatalf("unexpected capabilities %#v", caps)
	}
}

func TestRollyPayCreateAndPoll(t *testing.T) {
	var body map[string]string
	nonces := map[string]bool{}
	uuidPattern := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	pollStatus := "created"
	provider := rollypayTestProvider(t, map[string]string{"APP_ROLLYPAY_PAYMENT_METHOD": "sbp"}, func(writer http.ResponseWriter, request *http.Request) {
		nonce := request.Header.Get("X-Nonce")
		if request.Header.Get("X-API-Key") != "rolly-key" || !uuidPattern.MatchString(nonce) || nonces[nonce] {
			t.Errorf("unexpected auth headers %v", request.Header)
		}
		nonces[nonce] = true
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/payments":
			_ = json.NewDecoder(request.Body).Decode(&body)
			_, _ = writer.Write([]byte(`{"payment_id":"pay_8f2c","pay_url":"https://rollypay.io/pay/pay_8f2c","status":"created","expires_at":"2026-11-19T12:30:00Z"}`))
		case request.Method == http.MethodGet && request.URL.Path == "/payments/pay_8f2c":
			_, _ = writer.Write([]byte(`{"payment_id":"pay_8f2c","status":"` + pollStatus + `","amount":"1499.00","currency":"RUB"}`))
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
		"amount": "1499.00", "payment_currency": "RUB", "order_id": freekassaTestOrder, "payment_method": "sbp",
		"description": "Subscription", "redirect_url": "https://shop.example.com/account/orders/1",
	}
	for key, value := range want {
		if body[key] != value {
			t.Fatalf("%s: want %q, got %q", key, value, body[key])
		}
	}
	if intent.ProviderReference != "pay_8f2c" || intent.Status != "pending" || intent.CheckoutURL != "https://rollypay.io/pay/pay_8f2c" ||
		intent.ExpiresAt == nil || intent.ExpiresAt.Format("15:04") != "12:30" {
		t.Fatalf("unexpected intent %#v", intent)
	}
	for status, normalized := range map[string]string{"created": "pending", "processing": "processing", "paid": "succeeded", "expired": "expired", "canceled": "cancelled", "chargeback": "chargeback", "weird": "pending"} {
		pollStatus = status
		polled, err := provider.Poll(t.Context(), "pay_8f2c")
		if err != nil || polled.Status != normalized || polled.Amount.Amount != 149900 {
			t.Fatalf("%s must map to %s: %#v %v", status, normalized, polled, err)
		}
	}
}

func TestRollyPayWebhookVerifiesTheTimestampedSignature(t *testing.T) {
	provider := rollypayTestProvider(t, nil, nil)
	headers := http.Header{}
	headers.Set("X-Timestamp", rollypayTestTimestamp)
	headers.Set("X-Signature", rollypayTestSignature)
	event, err := provider.VerifyWebhook(headers, []byte(rollypayTestBody))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if event.ID != "payment.paid:pay_8f2c:paid" || event.Status != "succeeded" || event.ProviderReference != "pay_8f2c" ||
		event.MerchantReference != freekassaTestOrder || event.Amount != (commerce.Money{Amount: 149900, Currency: "RUB"}) {
		t.Fatalf("unexpected event %#v", event)
	}
	repeat, err := provider.VerifyWebhook(headers, []byte(rollypayTestBody))
	if err != nil || repeat.ID != event.ID {
		t.Fatalf("a redelivery must keep its event ID: %v", err)
	}
	if _, err := provider.VerifyWebhook(headers, []byte(strings.Replace(rollypayTestBody, "1499.00", "1.00", 1))); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("a tampered amount must be rejected, got %v", err)
	}
	shifted := headers.Clone()
	shifted.Set("X-Timestamp", "1763553601")
	if _, err := provider.VerifyWebhook(shifted, []byte(rollypayTestBody)); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("a changed timestamp must be rejected, got %v", err)
	}
	unsigned := headers.Clone()
	unsigned.Del("X-Timestamp")
	if _, err := provider.VerifyWebhook(unsigned, []byte(rollypayTestBody)); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("a missing timestamp must be rejected, got %v", err)
	}
	unknown := `{"event_type":"payment.updated","payment_id":"pay_8f2c","order_id":"x","status":"on_hold","amount":"1499.00","currency":"RUB"}`
	if event, err := provider.VerifyWebhook(rollypayTestHeaders("1763553700", unknown), []byte(unknown)); err != nil || event.Status != "pending" {
		t.Fatalf("an unknown status must not settle: %#v %v", event, err)
	}
	if status, _, ack := provider.WebhookAck("paid"); status != http.StatusOK || string(ack) != `{"status":true}` {
		t.Fatalf("unexpected ack %d %s", status, ack)
	}
}

func TestRollyPayErrorsSeparateRefusalsFromOutagesAndRefundsAreUnsupported(t *testing.T) {
	status := http.StatusUnprocessableEntity
	provider := rollypayTestProvider(t, nil, func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(status)
		_, _ = writer.Write([]byte(`{"message":"bad"}`))
	})
	if _, err := provider.Poll(t.Context(), "pay_8f2c"); !Refused(err) {
		t.Fatalf("a 4xx is a refusal, got %v", err)
	}
	status = http.StatusInternalServerError
	if _, err := provider.Poll(t.Context(), "pay_8f2c"); err == nil || Refused(err) {
		t.Fatalf("a 5xx is not a refusal, got %v", err)
	}
	if _, err := provider.Refund(t.Context(), RefundRequest{}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("refunds are unsupported, got %v", err)
	}
}
