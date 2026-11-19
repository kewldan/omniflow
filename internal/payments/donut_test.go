package payments

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/omniflow/omniflow/internal/commerce"
)

// Signatures below were computed with Bedolaga's own Python signing routine
// (DonutService._build_signature_string over json.loads output) and key
// "secret", so they pin byte-for-byte compatibility, including Python's
// rendering of a numeric 100.00 as "100.0".
const (
	donutTestCallbackStringAmount  = `{"token":"tok","transaction_id":42,"order_id":"order-1","amount":{"value":"100.00","currency":"RUB"},"recalculated":false,"status":{"type":"success"},"signature":"3bcaee44f9e09b65312ae135817ee6dbf06fd98062b808e2320208a0fc148b68"}`
	donutTestCallbackNumericAmount = `{"token":"tok","transaction_id":"tx-9","order_id":"order-1","amount":{"value":100.00,"currency":"RUB"},"status":{"type":"success"},"signature":"5e55a0122cb134a173172523276ca12a68e818c58a92180c9f08efcae0f2d953"}`
	donutTestPayinSignature        = "cb4a9820056754aa7aa8fae242149d6799dfa1cd814c1513e69f6c0d35bdc6d3"
)

func donutTestProvider(t *testing.T, status int, reply string, seen *map[string]any) *Donut {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		raw, _ := io.ReadAll(request.Body)
		if seen != nil {
			*seen = map[string]any{}
			_ = json.Unmarshal(raw, seen)
			(*seen)["_path"] = request.URL.Path
		}
		writer.WriteHeader(status)
		_, _ = io.WriteString(writer, reply)
	}))
	t.Cleanup(server.Close)
	built, err := buildDonut(Settings{Env: func(key string) string {
		return map[string]string{"APP_DONUT_TOKEN": "tok", "APP_DONUT_SECRET": "secret", "APP_DONUT_BASE_URL": server.URL}[key]
	}, PublicURL: "https://shop.example"})
	if err != nil {
		t.Fatal(err)
	}
	return built.(*Donut)
}

func TestDonutBuilder(t *testing.T) {
	settings := func(values map[string]string) Settings {
		return Settings{Env: func(key string) string { return values[key] }}
	}
	if _, err := buildDonut(settings(nil)); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("expected ErrNotConfigured, got %v", err)
	}
	if _, err := buildDonut(settings(map[string]string{"APP_DONUT_TOKEN": "tok"})); err == nil || errors.Is(err, ErrNotConfigured) {
		t.Fatalf("a partial configuration must be reported, got %v", err)
	}
	if _, err := buildDonut(settings(map[string]string{"APP_DONUT_TOKEN": "tok", "APP_DONUT_SECRET": "s", "APP_DONUT_METHOD": "cash"})); err == nil {
		t.Fatal("an unknown method must be refused")
	}
	built, err := buildDonut(settings(map[string]string{"APP_DONUT_TOKEN": "tok", "APP_DONUT_SECRET": "s"}))
	if err != nil {
		t.Fatal(err)
	}
	provider := built.(*Donut)
	if provider.Name() != "donut" || provider.baseURL != donutDefaultBaseURL || provider.currency != "RUB" || provider.method != "CARD" || provider.callbackURL != "" {
		t.Fatalf("unexpected defaults: %#v", provider)
	}
	if !provider.SupportsCurrency("rub") || provider.SupportsCurrency("USD") {
		t.Fatal("Donut settles its configured currency only")
	}
	if capabilities := provider.Capabilities(); capabilities != (Capabilities{Webhooks: true}) {
		t.Fatalf("unexpected capabilities: %#v", capabilities)
	}
}

func TestDonutCreateSignsThePayin(t *testing.T) {
	var seen map[string]any
	provider := donutTestProvider(t, 200, `{"transaction_id":777,"redirect_url":"https://pay.donut.example/777","status":{"type":"created"}}`, &seen)
	intent, err := provider.Create(t.Context(), CreateRequest{OrderID: "order-1", Amount: commerce.Money{Amount: 10001, Currency: "RUB"}, ReturnURL: "https://shop.example/back"})
	if err != nil {
		t.Fatal(err)
	}
	if seen["_path"] != "/p2p_payin" || seen["signature"] != donutTestPayinSignature || seen["redirect"] != "true" || seen["description"] != "CARD" {
		t.Fatalf("unexpected payin: %#v", seen)
	}
	if amount := seen["amount"].(map[string]any); amount["value"] != "100.01" || amount["currency"] != "RUB" {
		t.Fatalf("amount must be major units with two decimals: %#v", amount)
	}
	if seen["callback_url"] != "https://shop.example/v1/payments/webhooks/donut" || seen["return_url"] != "https://shop.example/back" {
		t.Fatalf("callback and return URLs missing: %#v", seen)
	}
	if intent.ProviderReference != "777" || intent.Status != "pending" || intent.CheckoutURL != "https://pay.donut.example/777" || intent.Amount.Amount != 10001 {
		t.Fatalf("unexpected intent: %#v", intent)
	}
}

func TestDonutCreateClassifiesRefusals(t *testing.T) {
	cases := []struct {
		status  int
		reply   string
		refused bool
	}{
		{200, `{"status":{"type":"error","error_code":"12","error_description":"limit"}}`, true},
		{400, `{"status":{"type":"error"}}`, true},
		{500, `{"status":{"type":"error"}}`, false},
		{502, `bad gateway`, false},
		{200, `{"transaction_id":1,"status":{"type":"created"}}`, false},
	}
	for _, testCase := range cases {
		provider := donutTestProvider(t, testCase.status, testCase.reply, nil)
		_, err := provider.Create(t.Context(), CreateRequest{OrderID: "order-1", Amount: commerce.Money{Amount: 10000, Currency: "RUB"}})
		if err == nil || Refused(err) != testCase.refused {
			t.Fatalf("HTTP %d %s: refused=%v err=%v", testCase.status, testCase.reply, Refused(err), err)
		}
	}
	provider := donutTestProvider(t, 200, `{}`, nil)
	if _, err := provider.Create(t.Context(), CreateRequest{OrderID: "order-1", Amount: commerce.Money{Amount: 10000, Currency: "USD"}}); !Refused(err) {
		t.Fatalf("a foreign currency must be refused locally, got %v", err)
	}
}

func TestDonutWebhookMatchesBedolagaVectors(t *testing.T) {
	provider := donutTestProvider(t, 200, `{}`, nil)
	event, err := provider.VerifyWebhook(http.Header{}, []byte(donutTestCallbackStringAmount))
	if err != nil {
		t.Fatal(err)
	}
	if event.ID != "success:42" || event.Status != "succeeded" || event.ProviderReference != "42" || event.MerchantReference != "order-1" || event.Amount != (commerce.Money{Amount: 10000, Currency: "RUB"}) {
		t.Fatalf("unexpected event: %#v", event)
	}
	again, err := provider.VerifyWebhook(http.Header{}, []byte(donutTestCallbackStringAmount))
	if err != nil || again.ID != event.ID {
		t.Fatalf("a redelivery must carry the same ID: %#v %v", again, err)
	}
	numeric, err := provider.VerifyWebhook(http.Header{}, []byte(donutTestCallbackNumericAmount))
	if err != nil {
		t.Fatalf("a numeric amount must verify as Python renders it: %v", err)
	}
	if numeric.Amount != (commerce.Money{Amount: 10000, Currency: "RUB"}) || numeric.ProviderReference != "tx-9" {
		t.Fatalf("unexpected event: %#v", numeric)
	}
}

func TestDonutWebhookRejectsForgeries(t *testing.T) {
	provider := donutTestProvider(t, 200, `{}`, nil)
	tampered := strings.Replace(donutTestCallbackStringAmount, `"100.00"`, `"1.00"`, 1)
	if _, err := provider.VerifyWebhook(http.Header{}, []byte(tampered)); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("a tampered amount must fail, got %v", err)
	}
	status := strings.Replace(donutTestCallbackStringAmount, `"type":"success"`, `"type":"processing"`, 1)
	if _, err := provider.VerifyWebhook(http.Header{}, []byte(status)); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("a tampered status must fail, got %v", err)
	}
	unsigned := strings.Replace(donutTestCallbackStringAmount, `"signature":"3bcaee44`, `"other":"3bcaee44`, 1)
	if _, err := provider.VerifyWebhook(http.Header{}, []byte(unsigned)); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("a missing signature must fail, got %v", err)
	}
	if _, err := provider.VerifyWebhook(http.Header{}, []byte(`not json`)); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("garbage must fail, got %v", err)
	}
	other := &Donut{secret: "other", clock: provider.clock}
	if _, err := other.VerifyWebhook(http.Header{}, []byte(donutTestCallbackStringAmount)); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("another secret must fail, got %v", err)
	}
}

func TestDonutWebhookUnknownStatusIsIgnored(t *testing.T) {
	provider := donutTestProvider(t, 200, `{}`, nil)
	body := map[string]any{"token": "tok", "transaction_id": "tx-1", "order_id": "order-1", "amount": map[string]any{"value": "100.00", "currency": "RUB"}, "status": map[string]any{"type": "on_hold"}}
	body["signature"] = provider.sign([][2]string{{"token", "tok"}, {"transaction_id", "tx-1"}, {"order_id", "order-1"}, {"amount.value", "100.00"}, {"amount.currency", "RUB"}, {"status.type", "on_hold"}})
	raw, _ := json.Marshal(body)
	event, err := provider.VerifyWebhook(http.Header{}, raw)
	if err != nil || event.Status != "pending" {
		t.Fatalf("an unknown status must be acknowledged and never settle: %#v %v", event, err)
	}
	for statusType, expected := range map[string]string{"created": "pending", "processing": "pending", "cancelled": "cancelled", "error": "failed", "success": "succeeded"} {
		if got := donutStatus(statusType); got != expected {
			t.Fatalf("%s: got %s, want %s", statusType, got, expected)
		}
	}
	if _, ok := Provider(provider).(WebhookAcknowledger); ok {
		t.Fatal("Donut needs only HTTP 200")
	}
}

func TestDonutPollAndRefundAreUnsupported(t *testing.T) {
	provider := donutTestProvider(t, 200, `{}`, nil)
	if _, err := provider.Poll(t.Context(), "777"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported, got %v", err)
	}
	if _, err := provider.Refund(t.Context(), RefundRequest{PaymentReference: "777", IdempotencyKey: "refund-key"}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported, got %v", err)
	}
}

func TestDonutPythonFloatRendering(t *testing.T) {
	for value, expected := range map[float64]string{100: "100.0", 100.5: "100.5", 0.1: "0.1", 1e16: "1e+16", 0.00001: "1e-05"} {
		if got := donutPythonFloat(value); got != expected {
			t.Fatalf("%v: got %s, want %s", value, got, expected)
		}
	}
}
