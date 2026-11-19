package payments

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/commerce"
)

const tabpayTestSecret = "whsec_test_secret"

var tabpayTestNow = time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)

func tabpayTestProvider(t *testing.T, handler http.HandlerFunc) *TabPay {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	built, err := buildTabPay(Settings{Env: func(key string) string {
		return map[string]string{"APP_TABPAY_API_KEY": "tp_test_key", "APP_TABPAY_WEBHOOK_SECRET": tabpayTestSecret, "APP_TABPAY_BASE_URL": server.URL}[key]
	}})
	if err != nil {
		t.Fatal(err)
	}
	provider := built.(*TabPay)
	provider.clock = func() time.Time { return tabpayTestNow }
	return provider
}

func tabpayTestSigned(body []byte, at time.Time, secret string) http.Header {
	timestamp := strconv.FormatInt(at.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp + "."))
	_, _ = mac.Write(body)
	headers := make(http.Header)
	headers.Set("X-Timestamp", timestamp)
	headers.Set("X-Signature-V2", hex.EncodeToString(mac.Sum(nil)))
	return headers
}

func TestTabPayBuilder(t *testing.T) {
	settings := func(values map[string]string) Settings {
		return Settings{Env: func(key string) string { return values[key] }}
	}
	if _, err := buildTabPay(settings(nil)); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("expected ErrNotConfigured, got %v", err)
	}
	for _, values := range []map[string]string{
		{"APP_TABPAY_API_KEY": "tp_key"},
		{"APP_TABPAY_API_KEY": "tp_key", "APP_TABPAY_WEBHOOK_SECRET": "s", "APP_TABPAY_METHOD": "cash"},
		{"APP_TABPAY_API_KEY": "tp_key", "APP_TABPAY_WEBHOOK_SECRET": "s", "APP_TABPAY_WEBHOOK_MAX_AGE_SECONDS": "-5"},
	} {
		if _, err := buildTabPay(settings(values)); err == nil || errors.Is(err, ErrNotConfigured) {
			t.Fatalf("%v must be reported as misconfigured, got %v", values, err)
		}
	}
	built, err := buildTabPay(settings(map[string]string{"APP_TABPAY_API_KEY": "tp_key", "APP_TABPAY_WEBHOOK_SECRET": "s"}))
	if err != nil {
		t.Fatal(err)
	}
	provider := built.(*TabPay)
	if provider.Name() != "tabpay" || provider.baseURL != tabpayDefaultBaseURL || provider.maxAge != 5*time.Minute {
		t.Fatalf("unexpected defaults: %#v", provider)
	}
	if !provider.SupportsCurrency("RUB") || provider.SupportsCurrency("USD") {
		t.Fatal("TabPay settles RUB only")
	}
	if capabilities := provider.Capabilities(); capabilities.Refunds || capabilities.Recurring || !capabilities.Webhooks || !capabilities.Polling {
		t.Fatalf("unexpected capabilities: %#v", capabilities)
	}
}

func TestTabPayCreate(t *testing.T) {
	var got map[string]any
	provider := tabpayTestProvider(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/payments" || request.Header.Get("X-Api-Key") != "tp_test_key" {
			t.Errorf("unexpected call %s %s", request.Method, request.URL.Path)
		}
		raw, _ := io.ReadAll(request.Body)
		_ = json.Unmarshal(raw, &got)
		writer.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(writer, `{"id":"pay_1","orderId":"order-1","status":"CREATED","amountKopecks":10001,"payUrl":"https://tabpay.org/pay/pay_1","isTest":false}`)
	})
	intent, err := provider.Create(t.Context(), CreateRequest{OrderID: "order-1", Amount: commerce.Money{Amount: 10001, Currency: "RUB"}, Description: "Plan", ReturnURL: "https://shop.example/back"})
	if err != nil {
		t.Fatal(err)
	}
	if got["orderId"] != "order-1" || got["amountKopecks"] != float64(10001) || got["successUrl"] != "https://shop.example/back" || got["description"] != "Plan" {
		t.Fatalf("unexpected request: %#v", got)
	}
	if _, ok := got["method"]; ok {
		t.Fatal("without a configured method the customer chooses on the payment page")
	}
	if intent.ProviderReference != "pay_1" || intent.Status != "pending" || intent.CheckoutURL != "https://tabpay.org/pay/pay_1" || intent.Amount != (commerce.Money{Amount: 10001, Currency: "RUB"}) {
		t.Fatalf("unexpected intent: %#v", intent)
	}
	for _, money := range []commerce.Money{{Amount: 99, Currency: "RUB"}, {Amount: 10000, Currency: "USD"}} {
		if _, err := provider.Create(t.Context(), CreateRequest{OrderID: "order-1", Amount: money}); !Refused(err) {
			t.Fatalf("%v must be refused locally, got %v", money, err)
		}
	}
}

func TestTabPayCreateReusesThePaymentOnADuplicateOrder(t *testing.T) {
	provider := tabpayTestProvider(t, func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodPost:
			writer.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(writer, `{"message":"duplicate orderId"}`)
		case request.URL.Query().Get("orderId") == "order-1":
			_, _ = io.WriteString(writer, `{"id":"pay_1","orderId":"order-1","status":"CREATED","amountKopecks":10000,"payUrl":"https://tabpay.org/pay/pay_1"}`)
		}
	})
	intent, err := provider.Create(t.Context(), CreateRequest{OrderID: "order-1", Amount: commerce.Money{Amount: 10000, Currency: "RUB"}})
	if err != nil || intent.ProviderReference != "pay_1" || intent.CheckoutURL == "" {
		t.Fatalf("the existing payment must be reused: %#v %v", intent, err)
	}

	methodConflict := tabpayTestProvider(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPost {
			writer.WriteHeader(http.StatusConflict)
			return
		}
		writer.WriteHeader(http.StatusNotFound)
	})
	if _, err := methodConflict.Create(t.Context(), CreateRequest{OrderID: "order-1", Amount: commerce.Money{Amount: 10000, Currency: "RUB"}}); err == nil {
		t.Fatal("a 409 with no payment behind it is an error")
	}
}

func TestTabPayClassifiesProviderErrors(t *testing.T) {
	for status, refused := range map[int]bool{400: true, 401: true, 422: true, 429: false, 500: false, 503: false} {
		provider := tabpayTestProvider(t, func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(status)
			_, _ = io.WriteString(writer, `{"message":["amountKopecks must be positive"]}`)
		})
		_, err := provider.Create(t.Context(), CreateRequest{OrderID: "order-1", Amount: commerce.Money{Amount: 10000, Currency: "RUB"}})
		if err == nil || Refused(err) != refused {
			t.Fatalf("HTTP %d: refused=%v err=%v", status, Refused(err), err)
		}
	}
}

func TestTabPayPollMapsStatuses(t *testing.T) {
	cases := map[string]string{
		`{"id":"pay_1","status":"SUCCESS","amountKopecks":10000}`:                  "succeeded",
		`{"id":"pay_1","status":"SUCCESS","amountKopecks":"10000"}`:                "succeeded",
		`{"id":"pay_1","status":"SUCCESS","amountKopecks":10000,"isTest":true}`:    "failed",
		`{"id":"pay_1","status":"SUCCESS","amountKopecks":10000,"isTest":"maybe"}`: "succeeded",
		`{"id":"pay_1","status":"SUCCESS"}`:                                        "processing",
		`{"id":"pay_1","status":"CREATED","amountKopecks":10000}`:                  "pending",
		`{"id":"pay_1","status":"PENDING","amountKopecks":10000}`:                  "processing",
		`{"id":"pay_1","status":"FAILED","amountKopecks":10000}`:                   "failed",
		`{"id":"pay_1","status":"EXPIRED","amountKopecks":10000}`:                  "expired",
		`{"id":"pay_1","status":"REFUNDED","amountKopecks":10000}`:                 "cancelled",
		`{"id":"pay_1","status":"CANCELED","amountKopecks":10000}`:                 "cancelled",
	}
	for reply, expected := range cases {
		provider := tabpayTestProvider(t, func(writer http.ResponseWriter, request *http.Request) {
			if request.URL.Path != "/v1/payments/pay_1" {
				t.Errorf("unexpected poll %s", request.URL.Path)
			}
			_, _ = io.WriteString(writer, reply)
		})
		intent, err := provider.Poll(t.Context(), "pay_1")
		if err != nil || intent.Status != expected {
			t.Fatalf("%s: got %#v %v, want %s", reply, intent, err, expected)
		}
	}
}

func TestTabPayRecover(t *testing.T) {
	missing := tabpayTestProvider(t, func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusNotFound) })
	if _, found, err := missing.Recover(t.Context(), "order-1"); err != nil || found {
		t.Fatalf("404 means no payment: %v %v", found, err)
	}
	if err := Probe(t.Context(), missing); err != nil {
		t.Fatalf("a 404 lookup proves the key works: %v", err)
	}
	down := tabpayTestProvider(t, func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusBadGateway) })
	if _, _, err := down.Recover(t.Context(), "order-1"); err == nil {
		t.Fatal("an outage must not read as 'no payment'")
	}
	unauthorised := tabpayTestProvider(t, func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusUnauthorized) })
	if err := Probe(t.Context(), unauthorised); err == nil {
		t.Fatal("a rejected key must fail the probe")
	}
}

func TestTabPayWebhook(t *testing.T) {
	provider := tabpayTestProvider(t, func(http.ResponseWriter, *http.Request) {})
	body := []byte(`{"id":"pay_1","orderId":"order-1","status":"SUCCESS","amountKopecks":10000,"telegramId":"1","test":false}`)
	event, err := provider.VerifyWebhook(tabpayTestSigned(body, tabpayTestNow, tabpayTestSecret), body)
	if err != nil {
		t.Fatal(err)
	}
	if event.ID != "success:pay_1" || event.Status != "succeeded" || event.ProviderReference != "pay_1" || event.MerchantReference != "order-1" || event.Amount != (commerce.Money{Amount: 10000, Currency: "RUB"}) {
		t.Fatalf("unexpected event: %#v", event)
	}
	again, err := provider.VerifyWebhook(tabpayTestSigned(body, tabpayTestNow.Add(-time.Minute), tabpayTestSecret), body)
	if err != nil || again.ID != event.ID {
		t.Fatalf("a redelivery must carry the same ID: %#v %v", again, err)
	}

	if _, err := provider.VerifyWebhook(tabpayTestSigned(body, tabpayTestNow, "other"), body); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("another secret must fail, got %v", err)
	}
	tampered := []byte(strings.Replace(string(body), "10000", "100000", 1))
	if _, err := provider.VerifyWebhook(tabpayTestSigned(body, tabpayTestNow, tabpayTestSecret), tampered); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("a tampered amount must fail, got %v", err)
	}
	for _, skew := range []time.Duration{-6 * time.Minute, 6 * time.Minute} {
		if _, err := provider.VerifyWebhook(tabpayTestSigned(body, tabpayTestNow.Add(skew), tabpayTestSecret), body); !errors.Is(err, ErrInvalidSignature) {
			t.Fatalf("a timestamp %v away must fail, got %v", skew, err)
		}
	}
	shifted := tabpayTestSigned(body, tabpayTestNow, tabpayTestSecret)
	shifted.Set("X-Timestamp", strconv.FormatInt(tabpayTestNow.Unix()+1, 10))
	if _, err := provider.VerifyWebhook(shifted, body); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("the timestamp is part of the signature, got %v", err)
	}
	badTimestamp := tabpayTestSigned(body, tabpayTestNow, tabpayTestSecret)
	badTimestamp.Set("X-Timestamp", "-1")
	if _, err := provider.VerifyWebhook(badTimestamp, body); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("a malformed timestamp must fail, got %v", err)
	}

	sandbox := []byte(`{"id":"pay_2","orderId":"order-2","status":"SUCCESS","amountKopecks":10000,"test":true}`)
	if event, err := provider.VerifyWebhook(tabpayTestSigned(sandbox, tabpayTestNow, tabpayTestSecret), sandbox); err != nil || event.Status == "succeeded" {
		t.Fatalf("a sandbox payment must never settle: %#v %v", event, err)
	}
	unknown := []byte(`{"id":"pay_3","orderId":"order-3","status":"DISPUTED","amountKopecks":10000}`)
	if event, err := provider.VerifyWebhook(tabpayTestSigned(unknown, tabpayTestNow, tabpayTestSecret), unknown); err != nil || event.Status != "pending" {
		t.Fatalf("an unknown status is acknowledged and ignored: %#v %v", event, err)
	}
	noAmount := []byte(`{"id":"pay_4","orderId":"order-4","status":"SUCCESS"}`)
	if _, err := provider.VerifyWebhook(tabpayTestSigned(noAmount, tabpayTestNow, tabpayTestSecret), noAmount); !errors.Is(err, ErrProviderResponse) {
		t.Fatalf("a success without an amount must be rejected, got %v", err)
	}
	if _, ok := Provider(provider).(WebhookAcknowledger); ok {
		t.Fatal("TabPay needs only a 2xx")
	}
}

func TestTabPayRefundIsUnsupported(t *testing.T) {
	provider := tabpayTestProvider(t, func(http.ResponseWriter, *http.Request) { t.Error("refund must not call TabPay") })
	if _, err := provider.Refund(t.Context(), RefundRequest{PaymentReference: "pay_1", IdempotencyKey: "refund-key"}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported, got %v", err)
	}
}
