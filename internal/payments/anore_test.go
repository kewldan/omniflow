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

func anoreTestSettings(values map[string]string) Settings {
	return Settings{Env: func(key string) string { return values[key] }, PublicURL: "https://bot.example"}
}

type anoreTestCall struct {
	method, path string
	headers      http.Header
	raw          []byte
}

func anoreTestProvider(t *testing.T, apiKey string, status int, reply string, calls *[]anoreTestCall) *Anore {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		raw, _ := io.ReadAll(request.Body)
		if calls != nil {
			*calls = append(*calls, anoreTestCall{request.Method, request.URL.Path, request.Header.Clone(), raw})
		}
		writer.WriteHeader(status)
		_, _ = io.WriteString(writer, reply)
	}))
	t.Cleanup(server.Close)
	built, err := buildAnore(anoreTestSettings(map[string]string{
		"APP_ANORE_API_KEY": apiKey, "APP_ANORE_SECRET_KEY": "synthetic-secret", "APP_ANORE_SHOP_ID": "7",
		"APP_ANORE_API_URL": server.URL + "/v1", "APP_ANORE_PAYMENT_METHODS": "sbp, card",
	}))
	if err != nil {
		t.Fatal(err)
	}
	provider := built.(*Anore)
	provider.clock = func() time.Time { return time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC) }
	return provider
}

func anoreTestSigned(body []byte, event string) http.Header {
	mac := hmac.New(sha256.New, []byte("synthetic-secret"))
	_, _ = mac.Write(body)
	headers := make(http.Header)
	headers.Set("Anore-Signature", hex.EncodeToString(mac.Sum(nil)))
	headers.Set("Anore-Event", event)
	headers.Set("Anore-Delivery-Id", "delivery-1")
	return headers
}

func TestAnoreBuilder(t *testing.T) {
	if _, err := buildAnore(anoreTestSettings(nil)); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("expected ErrNotConfigured, got %v", err)
	}
	for _, values := range []map[string]string{
		{"APP_ANORE_API_KEY": "key"},
		{"APP_ANORE_API_KEY": "an_live_x", "APP_ANORE_SECRET_KEY": "s"},
		{"APP_ANORE_API_KEY": "key", "APP_ANORE_SECRET_KEY": "s", "APP_ANORE_SHOP_ID": "-1"},
		{"APP_ANORE_API_KEY": "key", "APP_ANORE_SECRET_KEY": "s", "APP_ANORE_PAYMENT_METHODS": "wire"},
	} {
		if _, err := buildAnore(anoreTestSettings(values)); err == nil || errors.Is(err, ErrNotConfigured) {
			t.Fatalf("%v must be reported as misconfigured, got %v", values, err)
		}
	}
	built, err := buildAnore(anoreTestSettings(map[string]string{"APP_ANORE_API_KEY": "desk-key", "APP_ANORE_SECRET_KEY": "s"}))
	if err != nil {
		t.Fatal(err)
	}
	provider := built.(*Anore)
	if provider.baseURL != anoreDefaultBaseURL || len(provider.methods) != 4 || provider.callbackURL != "https://bot.example/v1/payments/webhooks/anore" {
		t.Fatalf("unexpected defaults: %#v", provider)
	}
	if !provider.SupportsCurrency("RUB") || provider.SupportsCurrency("USD") {
		t.Fatal("Anore settles RUB only")
	}
	if capabilities := provider.Capabilities(); capabilities.Refunds || capabilities.Recurring || !capabilities.Webhooks || !capabilities.Polling {
		t.Fatalf("unexpected capabilities: %#v", capabilities)
	}
}

// The expected body is Bedolaga's own wire vector for a 101-kopek invoice.
func TestAnoreCreateSignsTheExactWireBytes(t *testing.T) {
	var calls []anoreTestCall
	provider := anoreTestProvider(t, "an_live_synthetic", 200, `{"success":true,"id":"invoice-1","orderId":"order-1","amount":1.01,"currency":"RUB","status":"new","paymentUrl":"https://pay.anore.cc/invoice-1","expiresIn":600}`, &calls)
	intent, err := provider.Create(t.Context(), CreateRequest{
		OrderID: "order-1", Amount: commerce.Money{Amount: 101, Currency: "RUB"}, Description: "Тест",
		ReturnURL: "https://cabinet.example/success",
	})
	if err != nil {
		t.Fatal(err)
	}
	call := calls[0]
	if call.method != http.MethodPost || call.path != "/v1/payments" || call.headers.Get("Authorization") != "Bearer an_live_synthetic" {
		t.Fatalf("unexpected call: %#v", call)
	}
	mac := hmac.New(sha256.New, []byte("synthetic-secret"))
	_, _ = mac.Write(call.raw)
	if call.headers.Get("Anore-Signature") != hex.EncodeToString(mac.Sum(nil)) {
		t.Fatal("request signature must cover the exact bytes sent")
	}
	var body map[string]any
	if err := json.Unmarshal(call.raw, &body); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"amount": 1.01, "currency": "RUB", "description": "Тест", "orderId": "order-1", "shopId": float64(7),
		"methods": []any{"sbp", "card"}, "successurl": "https://cabinet.example/success",
		"failurl": "https://cabinet.example/success", "callbackUrl": "https://bot.example/v1/payments/webhooks/anore",
	}
	wantJSON, _ := json.Marshal(want)
	gotJSON, _ := json.Marshal(body)
	if string(wantJSON) != string(gotJSON) {
		t.Fatalf("unexpected body:\n got %s\nwant %s", gotJSON, wantJSON)
	}
	if intent.ProviderReference != "invoice-1" || intent.Status != "pending" || intent.CheckoutURL != "https://pay.anore.cc/invoice-1" || intent.Amount != (commerce.Money{Amount: 101, Currency: "RUB"}) {
		t.Fatalf("unexpected intent: %#v", intent)
	}
	if intent.ExpiresAt == nil || !intent.ExpiresAt.Equal(time.Date(2026, 11, 19, 12, 10, 0, 0, time.UTC)) {
		t.Fatalf("unexpected expiry: %v", intent.ExpiresAt)
	}
}

func TestAnoreCreateRejectsAMismatchedAnswer(t *testing.T) {
	for _, reply := range []string{
		`{"success":true,"id":"invoice-1","orderId":"other","amount":1.01,"currency":"RUB","status":"new","paymentUrl":"https://pay.anore.cc/x"}`,
		`{"success":true,"id":"invoice-1","orderId":"order-1","amount":2.00,"currency":"RUB","status":"new","paymentUrl":"https://pay.anore.cc/x"}`,
		`{"success":true,"id":"invoice-1","orderId":"order-1","amount":1.01,"currency":"RUB","status":"new","paymentUrl":"http://pay.anore.cc/x"}`,
		`{"success":false}`, `{"success":"true"}`, `[]`, `not json`,
	} {
		provider := anoreTestProvider(t, "desk", 200, reply, nil)
		if _, err := provider.Create(t.Context(), CreateRequest{OrderID: "order-1", Amount: commerce.Money{Amount: 101, Currency: "RUB"}}); !errors.Is(err, ErrProviderResponse) {
			t.Fatalf("%s must be rejected, got %v", reply, err)
		}
	}
}

func TestAnoreClassifiesProviderErrors(t *testing.T) {
	for status, refused := range map[int]bool{400: true, 401: true, 403: true, 404: true, 429: false, 503: false} {
		provider := anoreTestProvider(t, "desk", status, `{"error":"an_live_synthetic synthetic-secret"}`, nil)
		_, err := provider.Create(t.Context(), CreateRequest{OrderID: "order-1", Amount: commerce.Money{Amount: 100, Currency: "RUB"}})
		if err == nil || Refused(err) != refused || strings.Contains(err.Error(), "synthetic") {
			t.Fatalf("HTTP %d: refused=%v err=%v", status, Refused(err), err)
		}
	}
}

func TestAnorePollMapsStatuses(t *testing.T) {
	cases := []struct{ key, reply, status string }{
		{"desk", `{"success":true,"id":"invoice-1","amount":"100.00","currency":"RUB","status":"paid","paid":true}`, "succeeded"},
		{"desk", `{"success":true,"id":"invoice-1","amount":100,"currency":"RUB","status":"new","paid":false}`, "pending"},
		{"desk", `{"success":true,"id":"invoice-1","amount":100,"currency":"RUB","status":"expired"}`, "expired"},
		{"desk", `{"success":true,"id":"invoice-1","amount":100,"rubAmount":103.5,"currency":"RUB","status":"paid"}`, "succeeded"},
		{"an_test_synthetic", `{"success":true,"id":"invoice-1","amount":100,"currency":"RUB","status":"paid"}`, "failed"},
	}
	for _, testCase := range cases {
		var calls []anoreTestCall
		provider := anoreTestProvider(t, testCase.key, 200, testCase.reply, &calls)
		intent, err := provider.Poll(t.Context(), "invoice-1")
		if err != nil {
			t.Fatalf("%s: %v", testCase.reply, err)
		}
		if calls[0].method != http.MethodGet || calls[0].path != "/v1/payments/invoice-1" {
			t.Fatalf("unexpected poll: %#v", calls[0])
		}
		if intent.Status != testCase.status || intent.Amount != (commerce.Money{Amount: 10000, Currency: "RUB"}) {
			t.Fatalf("%s: unexpected intent %#v", testCase.reply, intent)
		}
	}
	for _, reply := range []string{
		`{"success":true,"id":"invoice-1","amount":100,"currency":"RUB","status":"refunded"}`,
		`{"success":true,"id":"invoice-1","amount":100,"rubAmount":99,"currency":"RUB","status":"paid"}`,
		`{"success":true,"id":"invoice-1","amount":100.001,"currency":"RUB","status":"paid"}`,
		`{"success":true,"id":"invoice-1","amount":100,"currency":"RUB","status":"paid","paid":false}`,
		`{"success":true,"id":"invoice-1","amount":100,"currency":"USD","status":"paid"}`,
	} {
		provider := anoreTestProvider(t, "desk", 200, reply, nil)
		if _, err := provider.Poll(t.Context(), "invoice-1"); err == nil {
			t.Fatalf("%s must be rejected", reply)
		}
	}
}

func TestAnoreWebhook(t *testing.T) {
	provider := anoreTestProvider(t, "desk", 200, `{}`, nil)
	body := []byte(`{"event":"payment.succeeded","id":"invoice-1","orderId":"order-1","amount":"100.00","rubAmount":"103.00","currency":"RUB","status":"paid","paid":true}`)
	event, err := provider.VerifyWebhook(anoreTestSigned(body, "payment.succeeded"), body)
	if err != nil {
		t.Fatal(err)
	}
	if event.ID != "payment.succeeded:invoice-1" || event.Status != "succeeded" || event.ProviderReference != "invoice-1" || event.MerchantReference != "order-1" || event.Amount != (commerce.Money{Amount: 10000, Currency: "RUB"}) {
		t.Fatalf("unexpected event: %#v", event)
	}
	redelivered := anoreTestSigned(body, "payment.succeeded")
	redelivered.Set("Anore-Delivery-Id", "delivery-2")
	if again, err := provider.VerifyWebhook(redelivered, body); err != nil || again.ID != event.ID {
		t.Fatalf("a redelivery must carry the same ID: %#v %v", again, err)
	}

	missing := anoreTestSigned(body, "payment.succeeded")
	missing.Del("Anore-Signature")
	if _, err := provider.VerifyWebhook(missing, body); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("a missing signature must fail, got %v", err)
	}
	if _, err := provider.VerifyWebhook(anoreTestSigned(body, "payment.succeeded"), append(append([]byte{}, body...), ' ')); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("a changed body must fail, got %v", err)
	}
	tampered := []byte(strings.Replace(string(body), `"100.00"`, `"1.00"`, 1))
	if _, err := provider.VerifyWebhook(anoreTestSigned(body, "payment.succeeded"), tampered); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("a tampered amount must fail, got %v", err)
	}
	if _, err := provider.VerifyWebhook(anoreTestSigned(body, "payment.expired"), body); !errors.Is(err, ErrProviderResponse) {
		t.Fatalf("an event header that disagrees with the body must fail, got %v", err)
	}
	noDelivery := anoreTestSigned(body, "payment.succeeded")
	noDelivery.Del("Anore-Delivery-Id")
	if _, err := provider.VerifyWebhook(noDelivery, body); !errors.Is(err, ErrProviderResponse) {
		t.Fatalf("a missing delivery ID must fail, got %v", err)
	}
	mismatch := []byte(`{"event":"payment.succeeded","id":"invoice-1","orderId":"order-1","amount":"100.00","currency":"RUB","status":"expired"}`)
	if _, err := provider.VerifyWebhook(anoreTestSigned(mismatch, "payment.succeeded"), mismatch); !errors.Is(err, ErrProviderResponse) {
		t.Fatalf("an event/status mismatch must fail, got %v", err)
	}
	unknown := []byte(`{"event":"payment.refunded","id":"invoice-1","orderId":"order-1","amount":"100.00","currency":"RUB","status":"refunded"}`)
	if _, err := provider.VerifyWebhook(anoreTestSigned(unknown, "payment.refunded"), unknown); !errors.Is(err, ErrProviderResponse) {
		t.Fatalf("an unknown event must fail, got %v", err)
	}
	payout := []byte(`{"event":"payout.succeeded","id":"payout-1"}`)
	if event, err := provider.VerifyWebhook(anoreTestSigned(payout, "payout.succeeded"), payout); err != nil || event.Status == "succeeded" {
		t.Fatalf("a payout event is acknowledged and never settles: %#v %v", event, err)
	}
	if _, ok := Provider(provider).(WebhookAcknowledger); ok {
		t.Fatal("Anore needs only a 2xx, not a custom body")
	}
}

func TestAnoreRefundIsUnsupported(t *testing.T) {
	provider := anoreTestProvider(t, "desk", 200, `{}`, nil)
	if _, err := provider.Refund(t.Context(), RefundRequest{PaymentReference: "invoice-1", IdempotencyKey: "refund-key"}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported, got %v", err)
	}
}
