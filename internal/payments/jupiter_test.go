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

// Computed with Bedolaga's JupiterService signing routine and key "secret".
// FPGate signs the same callback fields as Donut, so the callback vector is
// the same; the payin differs because Jupiter sends redirect=false.
const (
	jupiterTestCallback         = `{"token":"tok","transaction_id":42,"order_id":"order-1","amount":{"value":"100.00","currency":"RUB"},"recalculated":false,"status":{"type":"success"},"signature":"3bcaee44f9e09b65312ae135817ee6dbf06fd98062b808e2320208a0fc148b68"}`
	jupiterTestPayinSignature   = "d3cb3f69b9fbfd914eb1191fe23260a62d2d23219917629820586222d65b6248"
	jupiterTestNumericAmountCbk = `{"token":"tok","transaction_id":"tx-9","order_id":"order-1","amount":{"value":100.00,"currency":"RUB"},"status":{"type":"success"},"signature":"5e55a0122cb134a173172523276ca12a68e818c58a92180c9f08efcae0f2d953"}`
)

func jupiterTestProvider(t *testing.T, status int, reply string, seen *map[string]any) *Jupiter {
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
	built, err := buildJupiter(Settings{Env: func(key string) string {
		return map[string]string{"APP_JUPITER_TOKEN": "tok", "APP_JUPITER_SECRET": "secret", "APP_JUPITER_BASE_URL": server.URL}[key]
	}, PublicURL: "https://shop.example"})
	if err != nil {
		t.Fatal(err)
	}
	return built.(*Jupiter)
}

func TestJupiterBuilder(t *testing.T) {
	settings := func(values map[string]string) Settings {
		return Settings{Env: func(key string) string { return values[key] }}
	}
	if _, err := buildJupiter(settings(nil)); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("expected ErrNotConfigured, got %v", err)
	}
	if _, err := buildJupiter(settings(map[string]string{"APP_JUPITER_SECRET": "s"})); err == nil || errors.Is(err, ErrNotConfigured) {
		t.Fatalf("a partial configuration must be reported, got %v", err)
	}
	if _, err := buildJupiter(settings(map[string]string{"APP_JUPITER_TOKEN": "t", "APP_JUPITER_SECRET": "s", "APP_JUPITER_CURRENCY": "XYZ"})); err == nil {
		t.Fatal("an unknown currency must be refused")
	}
	built, err := buildJupiter(settings(map[string]string{"APP_JUPITER_TOKEN": "t", "APP_JUPITER_SECRET": "s"}))
	if err != nil {
		t.Fatal(err)
	}
	provider := built.(*Jupiter)
	if provider.Name() != "jupiter" || provider.baseURL != jupiterDefaultBaseURL || provider.description != "SBP" || provider.fallback != (jupiterCustomer{Email: "user@vpn.bot", Phone: "0000000000", Name: "User"}) {
		t.Fatalf("unexpected defaults: %#v", provider)
	}
	if !provider.SupportsCurrency("RUB") || provider.SupportsCurrency("EUR") {
		t.Fatal("Jupiter settles its configured currency only")
	}
	if capabilities := provider.Capabilities(); capabilities != (Capabilities{Webhooks: true}) {
		t.Fatalf("unexpected capabilities: %#v", capabilities)
	}
}

func TestJupiterCreateSignsThePayin(t *testing.T) {
	var seen map[string]any
	provider := jupiterTestProvider(t, 200, `{"transaction_id":"tx-1","details":{"qrcode_url":"https://qr.example/tx-1"},"status":{"type":"processing"}}`, &seen)
	intent, err := provider.Create(t.Context(), CreateRequest{
		OrderID: "order-1", Amount: commerce.Money{Amount: 10001, Currency: "RUB"}, Description: "Plan",
		Metadata: map[string]string{"customer_email": "buyer@example.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen["_path"] != "/p2p_payin_v2.1" || seen["signature"] != jupiterTestPayinSignature || seen["redirect"] != "false" || seen["description"] != "Plan" {
		t.Fatalf("unexpected payin: %#v", seen)
	}
	customer := seen["customer"].(map[string]any)
	if customer["id"] != "order-1" || customer["email"] != "buyer@example.com" || customer["phone"] != "0000000000" || customer["name"] != "User" {
		t.Fatalf("unexpected customer: %#v", customer)
	}
	if _, ok := seen["return_url"]; ok {
		t.Fatal("FPGate v2.1 payin carries no return_url")
	}
	if seen["callback_url"] != "https://shop.example/v1/payments/webhooks/jupiter" {
		t.Fatalf("callback URL missing: %#v", seen)
	}
	if intent.ProviderReference != "tx-1" || intent.Status != "pending" || intent.CheckoutURL != "https://qr.example/tx-1" {
		t.Fatalf("unexpected intent: %#v", intent)
	}
}

func TestJupiterCreateClassifiesRefusals(t *testing.T) {
	cases := []struct {
		status  int
		reply   string
		refused bool
	}{
		{200, `{"status":{"type":"error","error_code":"7"}}`, true},
		{200, `{"transaction_id":"x","details":{"url":"https://p.example"},"status":{"type":"created"}}`, true},
		{503, `{"status":{"type":"error"}}`, false},
		{504, ``, false},
	}
	for _, testCase := range cases {
		provider := jupiterTestProvider(t, testCase.status, testCase.reply, nil)
		_, err := provider.Create(t.Context(), CreateRequest{OrderID: "order-1", Amount: commerce.Money{Amount: 10000, Currency: "RUB"}})
		if err == nil || Refused(err) != testCase.refused {
			t.Fatalf("HTTP %d %s: refused=%v err=%v", testCase.status, testCase.reply, Refused(err), err)
		}
	}
}

func TestJupiterWebhook(t *testing.T) {
	provider := jupiterTestProvider(t, 200, `{}`, nil)
	event, err := provider.VerifyWebhook(http.Header{}, []byte(jupiterTestCallback))
	if err != nil {
		t.Fatal(err)
	}
	if event.ID != "success:42" || event.Status != "succeeded" || event.MerchantReference != "order-1" || event.Amount != (commerce.Money{Amount: 10000, Currency: "RUB"}) {
		t.Fatalf("unexpected event: %#v", event)
	}
	if again, _ := provider.VerifyWebhook(http.Header{}, []byte(jupiterTestCallback)); again.ID != event.ID {
		t.Fatal("a redelivery must carry the same ID")
	}
	if _, err := provider.VerifyWebhook(http.Header{}, []byte(jupiterTestNumericAmountCbk)); err != nil {
		t.Fatalf("a numeric amount must verify as Python renders it: %v", err)
	}
	tampered := strings.Replace(jupiterTestCallback, `"order-1"`, `"order-2"`, 1)
	if _, err := provider.VerifyWebhook(http.Header{}, []byte(tampered)); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("a tampered order must fail, got %v", err)
	}
	amount := strings.Replace(jupiterTestCallback, `"100.00"`, `"10.00"`, 1)
	if _, err := provider.VerifyWebhook(http.Header{}, []byte(amount)); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("a tampered amount must fail, got %v", err)
	}
	for statusType, expected := range map[string]string{"processing": "pending", "cancelled": "cancelled", "declined": "failed", "error": "failed", "success": "succeeded", "mystery": "pending"} {
		if got := jupiterStatus(statusType); got != expected {
			t.Fatalf("%s: got %s, want %s", statusType, got, expected)
		}
	}
}

func TestJupiterPollAndRefundAreUnsupported(t *testing.T) {
	provider := jupiterTestProvider(t, 200, `{}`, nil)
	if _, err := provider.Poll(t.Context(), "tx-1"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported, got %v", err)
	}
	if _, err := provider.Refund(t.Context(), RefundRequest{PaymentReference: "tx-1", IdempotencyKey: "refund-key"}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported, got %v", err)
	}
}
