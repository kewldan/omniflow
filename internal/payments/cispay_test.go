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
	"strings"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/commerce"
)

func cispayTestEnv(values map[string]string) Settings {
	return Settings{Env: func(key string) string { return values[key] }, PublicURL: "https://shop.example"}
}

func cispayTestProvider(t *testing.T, handler http.HandlerFunc) *CisPay {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	built, err := buildCisPay(cispayTestEnv(map[string]string{
		"APP_CISPAY_SHOP_ID": "shop-example", "APP_CISPAY_API_KEY": "cis_sec_synthetic",
		"APP_CISPAY_BASE_URL": server.URL, "APP_CISPAY_PAYMENT_METHOD": "card",
	}))
	if err != nil {
		t.Fatal(err)
	}
	provider := built.(*CisPay)
	provider.clock = func() time.Time { return time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC) }
	return provider
}

func cispayTestSign(body []byte, key string) http.Header {
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write(body)
	headers := make(http.Header)
	headers.Set("X-Signature", hex.EncodeToString(mac.Sum(nil)))
	return headers
}

func TestCisPayBuilder(t *testing.T) {
	if _, err := buildCisPay(cispayTestEnv(nil)); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("no credentials must leave CisPay unregistered, got %v", err)
	}
	if _, err := buildCisPay(cispayTestEnv(map[string]string{"APP_CISPAY_SHOP_ID": "shop"})); err == nil || errors.Is(err, ErrNotConfigured) {
		t.Fatalf("half a configuration must be reported, got %v", err)
	}
	if _, err := buildCisPay(cispayTestEnv(map[string]string{"APP_CISPAY_SHOP_ID": "shop", "APP_CISPAY_API_KEY": "key", "APP_CISPAY_PAYMENT_METHOD": "wire"})); err == nil {
		t.Fatal("an unknown method must be refused")
	}
	built, err := buildCisPay(cispayTestEnv(map[string]string{"APP_CISPAY_SHOP_ID": "shop", "APP_CISPAY_API_KEY": "key"}))
	if err != nil {
		t.Fatal(err)
	}
	provider := built.(*CisPay)
	if provider.Name() != "cispay" || provider.baseURL != cispayDefaultBaseURL || provider.method != "" {
		t.Fatalf("unexpected defaults: %#v", provider)
	}
	if !provider.SupportsCurrency("rub") || provider.SupportsCurrency("USD") {
		t.Fatal("CisPay settles RUB only")
	}
	if capabilities := provider.Capabilities(); capabilities.Refunds || capabilities.Recurring || !capabilities.Webhooks || !capabilities.Polling {
		t.Fatalf("unexpected capabilities: %#v", capabilities)
	}
	if !NeedsWebhookPolling(provider) {
		t.Fatal("CisPay notifications must be confirmed by Poll")
	}
}

func TestCisPayCreateSendsMinorUnitsAndMapsTheIntent(t *testing.T) {
	var got map[string]any
	provider := cispayTestProvider(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/payments" {
			t.Errorf("unexpected call %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("X-Shop-ID") != "shop-example" || request.Header.Get("X-Api-Key") != "cis_sec_synthetic" {
			t.Error("missing authentication headers")
		}
		raw, _ := io.ReadAll(request.Body)
		_ = json.Unmarshal(raw, &got)
		writer.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(writer, `{"id":"invoice-1","order_id":"order-1","store_id":"shop-example","status":"PENDING","amount":10001,"currency":"RUB","payment_method":"CARD","payment_url":"https://pay.cispay.app/invoice-1","expires_in_seconds":1800}`)
	})
	intent, err := provider.Create(t.Context(), CreateRequest{
		OrderID: "order-1", Amount: commerce.Money{Amount: 10001, Currency: "RUB"},
		Description: "Plan", ReturnURL: "https://shop.example/account/orders/order-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got["amount"] != float64(10001) || got["currency"] != "RUB" || got["order_id"] != "order-1" || got["payment_method"] != "CARD" || got["customer_id"] != "order-1" {
		t.Fatalf("unexpected request: %#v", got)
	}
	if got["redirect_success_url"] != "https://shop.example/account/orders/order-1" {
		t.Fatalf("return URL not forwarded: %#v", got)
	}
	if intent.ProviderReference != "invoice-1" || intent.Status != "pending" || intent.CheckoutURL != "https://pay.cispay.app/invoice-1" || intent.Amount != (commerce.Money{Amount: 10001, Currency: "RUB"}) {
		t.Fatalf("unexpected intent: %#v", intent)
	}
	if intent.ExpiresAt == nil || !intent.ExpiresAt.Equal(time.Date(2026, 11, 19, 12, 30, 0, 0, time.UTC)) {
		t.Fatalf("unexpected expiry: %v", intent.ExpiresAt)
	}
}

func TestCisPayCreateRefusesBeforeCallingTheProvider(t *testing.T) {
	calls := 0
	provider := cispayTestProvider(t, func(http.ResponseWriter, *http.Request) { calls++ })
	for _, money := range []commerce.Money{{Amount: 4999, Currency: "RUB"}, {Amount: 10000, Currency: "USD"}, {Amount: 0, Currency: "RUB"}} {
		_, err := provider.Create(t.Context(), CreateRequest{OrderID: "order-1", Amount: money})
		if !Refused(err) {
			t.Fatalf("%v must be refused locally, got %v", money, err)
		}
	}
	if calls != 0 {
		t.Fatal("a refused amount must never reach CisPay")
	}
}

func TestCisPayCreateRejectsAnUnsafeCheckoutURL(t *testing.T) {
	provider := cispayTestProvider(t, func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, `{"id":"invoice-1","order_id":"order-1","status":"PENDING","amount":10000,"payment_url":"javascript:alert(1)"}`)
	})
	if _, err := provider.Create(t.Context(), CreateRequest{OrderID: "order-1", Amount: commerce.Money{Amount: 10000, Currency: "RUB"}}); !errors.Is(err, ErrProviderResponse) {
		t.Fatalf("expected a provider response error, got %v", err)
	}
}

func TestCisPayClassifiesProviderErrors(t *testing.T) {
	for status, refused := range map[int]bool{400: true, 401: true, 403: true, 409: false, 429: false, 500: false, 503: false} {
		provider := cispayTestProvider(t, func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(status)
			_, _ = io.WriteString(writer, `{"detail":"cis_sec_private"}`)
		})
		_, err := provider.Create(t.Context(), CreateRequest{OrderID: "order-1", Amount: commerce.Money{Amount: 10000, Currency: "RUB"}})
		if err == nil || Refused(err) != refused {
			t.Fatalf("HTTP %d: refused=%v, err=%v", status, Refused(err), err)
		}
		if strings.Contains(err.Error(), "private") {
			t.Fatal("a provider body must not leak into the error")
		}
	}
}

func TestCisPayPollMapsStatuses(t *testing.T) {
	cases := map[string]struct{ body, status string }{
		"paid":     {`{"id":"invoice-1","status":"PAID","amount":10000}`, "succeeded"},
		"pending":  {`{"id":"invoice-1","status":"PENDING","amount":10000}`, "pending"},
		"failed":   {`{"id":"invoice-1","status":"FAILED","amount":10000}`, "failed"},
		"expired":  {`{"id":"invoice-1","status":"EXPIRED","amount":10000}`, "expired"},
		"refunded": {`{"id":"invoice-1","status":"REFUNDED","amount":10000}`, "cancelled"},
		"sandbox":  {`{"id":"invoice-1","status":"PAID","amount":10000,"is_sandbox":true}`, "failed"},
		"blocked":  {`{"id":"invoice-1","status":"PAID","amount":10000,"block_reason":"risk"}`, "failed"},
	}
	for name, testCase := range cases {
		provider := cispayTestProvider(t, func(writer http.ResponseWriter, request *http.Request) {
			if request.URL.Path != "/payments/status" || request.URL.Query().Get("id") != "invoice-1" {
				t.Errorf("unexpected poll %s", request.URL)
			}
			_, _ = io.WriteString(writer, testCase.body)
		})
		intent, err := provider.Poll(t.Context(), "invoice-1")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if intent.Status != testCase.status || intent.Amount != (commerce.Money{Amount: 10000, Currency: "RUB"}) {
			t.Fatalf("%s: unexpected intent %#v", name, intent)
		}
	}
}

func TestCisPayPollSettlesCryptoInTheRoubleSourceAmount(t *testing.T) {
	provider := cispayTestProvider(t, func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, `{"id":"invoice-1","status":"PAID","amount":125,"currency":"USD","payment_method":"CRYPTO","source_amount":10000,"source_currency":"RUB"}`)
	})
	intent, err := provider.Poll(t.Context(), "invoice-1")
	if err != nil {
		t.Fatal(err)
	}
	if intent.Amount != (commerce.Money{Amount: 10000, Currency: "RUB"}) {
		t.Fatalf("crypto must settle against the rouble source amount: %#v", intent.Amount)
	}
}

func TestCisPayPollRejectsMalformedAnswers(t *testing.T) {
	for _, body := range []string{`[]`, `{`, `{"id":"invoice-1","status":"PAID","amount":10000.5}`, `{"id":"invoice-1","status":"WHO","amount":10000}`, `{"id":"other","status":"PAID","amount":10000}`} {
		provider := cispayTestProvider(t, func(writer http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(writer, body) })
		if _, err := provider.Poll(t.Context(), "invoice-1"); err == nil {
			t.Fatalf("%s must be rejected", body)
		}
	}
}

func TestCisPayWebhook(t *testing.T) {
	provider := cispayTestProvider(t, func(http.ResponseWriter, *http.Request) {})
	body := []byte(`{"id":"invoice-1","order_id":"order-1","store_id":"shop-example","status":"PAID","amount":10000,"currency":"RUB","payment_method":"CARD","is_sandbox":false}`)
	event, err := provider.VerifyWebhook(cispayTestSign(body, "cis_sec_synthetic"), body)
	if err != nil {
		t.Fatal(err)
	}
	if event.ID != "paid:invoice-1" || event.ProviderReference != "invoice-1" || event.MerchantReference != "order-1" || event.Status != "succeeded" || event.Amount != (commerce.Money{Amount: 10000, Currency: "RUB"}) {
		t.Fatalf("unexpected event: %#v", event)
	}
	again, err := provider.VerifyWebhook(cispayTestSign(body, "cis_sec_synthetic"), body)
	if err != nil || again.ID != event.ID {
		t.Fatalf("a redelivery must carry the same ID: %#v %v", again, err)
	}

	if _, err := provider.VerifyWebhook(cispayTestSign(body, "other-key"), body); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("a foreign key must fail, got %v", err)
	}
	if _, err := provider.VerifyWebhook(http.Header{"X-Signature": {"invalid"}}, body); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("a malformed signature must fail, got %v", err)
	}
	tampered := []byte(strings.Replace(string(body), "10000", "99999", 1))
	if _, err := provider.VerifyWebhook(cispayTestSign(body, "cis_sec_synthetic"), tampered); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("a tampered amount must fail, got %v", err)
	}

	unknown := []byte(`{"id":"invoice-1","order_id":"order-1","store_id":"shop-example","status":"SETTLING","amount":10000}`)
	if _, err := provider.VerifyWebhook(cispayTestSign(unknown, "cis_sec_synthetic"), unknown); !errors.Is(err, ErrProviderResponse) {
		t.Fatalf("an unknown status must be rejected, got %v", err)
	}
	foreign := []byte(`{"id":"invoice-1","order_id":"order-1","store_id":"other-shop","status":"PAID","amount":10000}`)
	if _, err := provider.VerifyWebhook(cispayTestSign(foreign, "cis_sec_synthetic"), foreign); !errors.Is(err, ErrProviderResponse) {
		t.Fatalf("another store's payment must be rejected, got %v", err)
	}
	subscription := []byte(`{"event":"subscription.cancelled","object":"subscription","id":"subscription-1"}`)
	headers := cispayTestSign(subscription, "cis_sec_synthetic")
	headers.Set("X-CISPay-Event", "subscription.cancelled")
	if _, err := provider.VerifyWebhook(headers, subscription); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("a subscription notice is not a payment, got %v", err)
	}
}

func TestCisPayRefundIsUnsupported(t *testing.T) {
	provider := cispayTestProvider(t, func(http.ResponseWriter, *http.Request) { t.Error("refund must not call CisPay") })
	if _, err := provider.Refund(t.Context(), RefundRequest{PaymentReference: "invoice-1", IdempotencyKey: "refund-key", Amount: commerce.Money{Amount: 100, Currency: "RUB"}}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported, got %v", err)
	}
	if _, ok := Provider(provider).(RecurringCharger); ok {
		t.Fatal("CisPay must not claim to charge saved methods")
	}
}

func TestCisPayProbe(t *testing.T) {
	provider := cispayTestProvider(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/store/capabilities" {
			t.Errorf("probe must be a read: %s %s", request.Method, request.URL.Path)
		}
		_, _ = io.WriteString(writer, `{"store_id":"shop-example","is_active":true,"payment_methods":[]}`)
	})
	if err := Probe(t.Context(), provider); err != nil {
		t.Fatal(err)
	}
	inactive := cispayTestProvider(t, func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, `{"store_id":"shop-example","is_active":false}`)
	})
	if err := Probe(t.Context(), inactive); err == nil {
		t.Fatal("an inactive store must fail the probe")
	}
}
