package payments

import (
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/commerce"
)

// The vectors below were produced with Bedolaga's signing code: the sorted
// top level serialised by json.dumps(ensure_ascii=False, separators=(",",
// ":")) and HMAC-SHA256'd with the token "sever-token".
const (
	severpayTestNotification = `{"type": "payin", "data": {"id": 81234, "uid": "d2c1a7e4", "order_id": "6f1c1f3e-8a7b-4c2d-9e0f-112233445566", "status": "success", "amount": 1499.0, "currency": "RUB", "client": {"email": "guest@telegram.org", "note": "Оплата \"VPN\" <&> \\ /\n \u0001"}, "fee": 1e-05, "big": 1e+16, "flags": [true, false, null, -0.0, 100, 0.1, 123456.789]}, "sign": "f63caaf3f38047252583a6458a884533030e5725d209226678b4e2ce24c2bbee"}`
	severpayTestCanonical    = "{\"data\":{\"id\":81234,\"uid\":\"d2c1a7e4\",\"order_id\":\"6f1c1f3e-8a7b-4c2d-9e0f-112233445566\",\"status\":\"success\",\"amount\":1499.0,\"currency\":\"RUB\",\"client\":{\"email\":\"guest@telegram.org\",\"note\":\"Оплата \\\"VPN\\\" <&> \\\\ /\\n \\u0001\"},\"fee\":1e-05,\"big\":1e+16,\"flags\":[true,false,null,-0.0,100,0.1,123456.789]},\"type\":\"payin\"}"
	severpayTestCreateBody   = `{"amount":1499.0,"client_email":"guest@telegram.org","client_id":"guest","currency":"RUB","lifetime":1440,"mid":4242,"order_id":"6f1c1f3e-8a7b-4c2d-9e0f-112233445566","salt":"00112233445566778899aabbccddeeff","url_return":"https://shop.example.com/account/orders/1?a=1&b=2"}`
	severpayTestCreateSign   = "43e1986918ce0ffb4bb07ee6905d4d2a9c909d35303d39dbc7bc9d2834ec328a"
)

func severpayTestProvider(t *testing.T, extra map[string]string, handler http.HandlerFunc) *SeverPay {
	t.Helper()
	values := map[string]string{"APP_SEVERPAY_MID": "4242", "APP_SEVERPAY_TOKEN": "sever-token"}
	for key, value := range extra {
		values[key] = value
	}
	built, err := buildSeverPay(freekassaTestEnv(values))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	provider := built.(*SeverPay)
	provider.salt = func() (string, error) { return "00112233445566778899aabbccddeeff", nil }
	if handler != nil {
		server := httptest.NewServer(handler)
		t.Cleanup(server.Close)
		provider.baseURL = server.URL
	}
	return provider
}

func TestSeverPayBuilderReadsItsEnvironment(t *testing.T) {
	if _, err := buildSeverPay(freekassaTestEnv(map[string]string{})); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("no credentials must leave the adapter unregistered, got %v", err)
	}
	_, err := buildSeverPay(freekassaTestEnv(map[string]string{"APP_SEVERPAY_TOKEN": "t"}))
	if err == nil || errors.Is(err, ErrNotConfigured) || !strings.Contains(err.Error(), "APP_SEVERPAY_MID") {
		t.Fatalf("a partial configuration must name what is missing, got %v", err)
	}
	for _, values := range []map[string]string{
		{"APP_SEVERPAY_MID": "merchant", "APP_SEVERPAY_TOKEN": "t"},
		{"APP_SEVERPAY_MID": "1", "APP_SEVERPAY_TOKEN": "t", "APP_SEVERPAY_LIFETIME": "10"},
		{"APP_SEVERPAY_MID": "1", "APP_SEVERPAY_TOKEN": "t", "APP_SEVERPAY_LIFETIME": "5000"},
	} {
		if _, err := buildSeverPay(freekassaTestEnv(values)); err == nil {
			t.Fatalf("%v must be rejected", values)
		}
	}
	provider := severpayTestProvider(t, nil, nil)
	if provider.lifetime != 1440 || provider.Name() != "severpay" || !provider.SupportsCurrency("RUB") || provider.SupportsCurrency("USD") {
		t.Fatalf("unexpected adapter %#v", provider)
	}
	if caps := provider.Capabilities(); caps.Refunds || caps.Recurring || !caps.Webhooks || !caps.Polling {
		t.Fatalf("unexpected capabilities %#v", caps)
	}
}

func TestSeverPayRequestsAreSignedLikeBedolaga(t *testing.T) {
	if got := hex.EncodeToString(severpaySign("sever-token", severpayTestCreateBody)); got != severpayTestCreateSign {
		t.Fatalf("signature mismatch: %s", got)
	}
	var received string
	pollStatus := "new"
	provider := severpayTestProvider(t, nil, func(writer http.ResponseWriter, request *http.Request) {
		raw, _ := io.ReadAll(request.Body)
		received = string(raw)
		switch request.URL.Path {
		case "/payin/create":
			_, _ = writer.Write([]byte(`{"status":true,"data":{"id":81234,"uid":"d2c1a7e4","url":"https://severpay.io/pay/d2c1a7e4","expire_at":1763640000}}`))
		case "/payin/get":
			_, _ = writer.Write([]byte(`{"status":true,"data":{"id":81234,"order_id":"x","status":"` + pollStatus + `","amount":1499.0,"currency":"RUB"}}`))
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	})
	intent, err := provider.Create(t.Context(), CreateRequest{
		OrderID: freekassaTestOrder, Amount: commerce.Money{Amount: 149900, Currency: "RUB"},
		ReturnURL: "https://shop.example.com/account/orders/1?a=1&b=2",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// The body sent is the signed one with `sign` sorted in between salt and
	// url_return.
	want := strings.Replace(severpayTestCreateBody, `,"url_return"`, `,"sign":"`+severpayTestCreateSign+`","url_return"`, 1)
	if received != want {
		t.Fatalf("unexpected create body\n got %s\nwant %s", received, want)
	}
	if intent.ProviderReference != "81234" || intent.CheckoutURL != "https://severpay.io/pay/d2c1a7e4" || intent.Status != "pending" ||
		intent.ExpiresAt == nil || !intent.ExpiresAt.Equal(time.Unix(1763640000, 0)) {
		t.Fatalf("unexpected intent %#v", intent)
	}
	for status, normalized := range map[string]string{"new": "pending", "process": "processing", "success": "succeeded", "decline": "failed", "fail": "failed", "odd": "pending"} {
		pollStatus = status
		polled, err := provider.Poll(t.Context(), "81234")
		if err != nil || polled.Status != normalized || polled.Amount.Amount != 149900 {
			t.Fatalf("%s must map to %s: %#v %v", status, normalized, polled, err)
		}
	}
	if !strings.Contains(received, `"id":"81234"`) || !strings.Contains(received, `"mid":4242`) {
		t.Fatalf("the lookup must carry the ID as a string and the merchant: %s", received)
	}
}

func TestSeverPayWebhookReproducesPythonSerialisation(t *testing.T) {
	canonical, sign, err := severpayCanonical([]byte(severpayTestNotification))
	if err != nil || canonical != severpayTestCanonical || sign != "f63caaf3f38047252583a6458a884533030e5725d209226678b4e2ce24c2bbee" {
		t.Fatalf("canonical form differs from Python's\n got %s\nwant %s (%v)", canonical, severpayTestCanonical, err)
	}
	provider := severpayTestProvider(t, nil, nil)
	event, err := provider.VerifyWebhook(http.Header{}, []byte(severpayTestNotification))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if event.ID != "payin:81234:success" || event.Status != "succeeded" || event.ProviderReference != "81234" ||
		event.MerchantReference != freekassaTestOrder || event.Amount != (commerce.Money{Amount: 149900, Currency: "RUB"}) {
		t.Fatalf("unexpected event %#v", event)
	}
	repeat, err := provider.VerifyWebhook(http.Header{}, []byte(severpayTestNotification))
	if err != nil || repeat.ID != event.ID {
		t.Fatalf("a redelivery must keep its event ID: %v", err)
	}
	for name, body := range map[string]string{
		"tampered amount":   strings.Replace(severpayTestNotification, `"amount": 1499.0`, `"amount": 14.99`, 1),
		"tampered status":   strings.Replace(severpayTestNotification, `"status": "success"`, `"status": "new"`, 1),
		"wrong signature":   strings.Replace(severpayTestNotification, `"sign": "f63c`, `"sign": "063c`, 1),
		"missing signature": strings.Replace(severpayTestNotification, `"sign"`, `"signature"`, 1),
	} {
		if _, err := provider.VerifyWebhook(http.Header{}, []byte(body)); !errors.Is(err, ErrInvalidSignature) {
			t.Fatalf("%s must be rejected, got %v", name, err)
		}
	}
	if _, err := provider.VerifyWebhook(http.Header{}, []byte(`not json`)); !errors.Is(err, ErrProviderResponse) {
		t.Fatalf("garbage must be rejected, got %v", err)
	}
	other := `{"type":"payout","data":{"id":5,"status":"success","amount":"10.00"}}`
	otherCanonical, _, _ := severpayCanonical([]byte(other))
	signed := strings.TrimSuffix(other, "}") + `,"sign":"` + hex.EncodeToString(severpaySign("sever-token", otherCanonical)) + `"}`
	if event, err := provider.VerifyWebhook(http.Header{}, []byte(signed)); err != nil || event.Status != "pending" {
		t.Fatalf("a non-payin notification must not settle: %#v %v", event, err)
	}
	if status, _, ack := provider.WebhookAck("paid"); status != http.StatusOK || string(ack) != `{"status":true}` {
		t.Fatalf("unexpected ack %d %s", status, ack)
	}
}

func TestSeverPayErrorsSeparateRefusalsFromOutagesAndRefundsAreUnsupported(t *testing.T) {
	status := http.StatusForbidden
	provider := severpayTestProvider(t, nil, func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(status)
		_, _ = writer.Write([]byte(`{"status":false,"msg":"bad sign"}`))
	})
	if _, err := provider.Poll(t.Context(), "1"); !Refused(err) {
		t.Fatalf("a 4xx is a refusal, got %v", err)
	}
	status = http.StatusBadGateway
	if _, err := provider.Poll(t.Context(), "1"); err == nil || Refused(err) {
		t.Fatalf("a 5xx is not a refusal, got %v", err)
	}
	status = http.StatusOK
	if _, err := provider.Poll(t.Context(), "1"); !errors.Is(err, ErrProviderResponse) {
		t.Fatalf("status:false must fail, got %v", err)
	}
	if _, err := provider.Refund(t.Context(), RefundRequest{}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("refunds are unsupported, got %v", err)
	}
}
