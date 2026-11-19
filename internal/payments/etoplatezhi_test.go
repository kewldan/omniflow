package payments

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/omniflow/omniflow/internal/commerce"
)

// Both signatures were computed with Bedolaga's EtoplatezhiService._sign
// (Python) and key "secret". The callback exercises nested objects, an empty
// string, a null, an empty list, a mixed list, a boolean and a float.
const (
	etoplatezhiTestURLSignature = "GmCbtlbl3dtZdN1kj+rda1k1fQxIdTXRUF4VaeBf+jJHJJM+ocM1DcT6XlHMeAc3NYNeO4RcNkkaszWzKHCS+Q=="
	etoplatezhiTestCallback     = `{"project_id":123,"payment":{"id":"order-1","type":"purchase","status":"success","date":"2026-11-19T12:00:00+0000",` +
		`"method":"card","sum":{"amount":10001,"currency":"RUB"},"is_new_attempts_available":false,` +
		`"description":"Plan"},"customer":{"id":"order-1"},"operation":{"id":17,"type":"sale","status":"success",` +
		`"sum_initial":{"amount":10001,"currency":"RUB"},"rate":1.0,"provider":{"id":5,"payment_id":"","auth_code":null}},` +
		`"errors":[],"tags":["a",true,{"k":2}],"signature":"FXLAQ/VNpvGpT9zHIHf9+dsQdzIxhSzLqQjm1/Ww9tNPY3Trs7eYy7SOz/nrwoV7SKOg1B6MxoJOXhvniYB85g=="}`
)

func etoplatezhiTestProvider(t *testing.T) *EtoPlatezhi {
	t.Helper()
	built, err := buildEtoPlatezhi(Settings{Env: func(key string) string {
		return map[string]string{"APP_ETOPLATEZHI_PROJECT_ID": "123", "APP_ETOPLATEZHI_SECRET_KEY": "secret"}[key]
	}, PublicURL: "https://shop.example"})
	if err != nil {
		t.Fatal(err)
	}
	return built.(*EtoPlatezhi)
}

func TestEtoPlatezhiBuilder(t *testing.T) {
	settings := func(values map[string]string) Settings {
		return Settings{Env: func(key string) string { return values[key] }}
	}
	if _, err := buildEtoPlatezhi(settings(nil)); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("expected ErrNotConfigured, got %v", err)
	}
	for _, values := range []map[string]string{
		{"APP_ETOPLATEZHI_PROJECT_ID": "123"},
		{"APP_ETOPLATEZHI_PROJECT_ID": "abc", "APP_ETOPLATEZHI_SECRET_KEY": "s"},
		{"APP_ETOPLATEZHI_PROJECT_ID": "1", "APP_ETOPLATEZHI_SECRET_KEY": "s", "APP_ETOPLATEZHI_FORCE_PAYMENT_METHOD": "cash"},
	} {
		if _, err := buildEtoPlatezhi(settings(values)); err == nil || errors.Is(err, ErrNotConfigured) {
			t.Fatalf("%v must be reported as misconfigured, got %v", values, err)
		}
	}
	provider := etoplatezhiTestProvider(t)
	if provider.Name() != "etoplatezhi" || provider.currency != "RUB" || provider.callbackURL != "https://shop.example/v1/payments/webhooks/etoplatezhi" {
		t.Fatalf("unexpected defaults: %#v", provider)
	}
	if capabilities := provider.Capabilities(); capabilities != (Capabilities{Webhooks: true}) {
		t.Fatalf("unexpected capabilities: %#v", capabilities)
	}
	if !provider.SupportsCurrency("RUB") || provider.SupportsCurrency("USD") {
		t.Fatal("EtoPlatezhi settles its configured currency only")
	}
}

func TestEtoPlatezhiCreateSignsThePaymentPageURL(t *testing.T) {
	provider := etoplatezhiTestProvider(t)
	intent, err := provider.Create(t.Context(), CreateRequest{
		OrderID: "order-1", Amount: commerce.Money{Amount: 10001, Currency: "RUB"}, Description: "Plan",
		ReturnURL: "https://shop.example/back",
	})
	if err != nil {
		t.Fatal(err)
	}
	checkout, err := url.Parse(intent.CheckoutURL)
	if err != nil || checkout.Host != "paymentpage.etoplatezhi.ru" || checkout.Path != "/payment" {
		t.Fatalf("unexpected checkout URL: %s", intent.CheckoutURL)
	}
	query := checkout.Query()
	if query.Get("signature") != etoplatezhiTestURLSignature {
		t.Fatalf("signature differs from Bedolaga's: %s", query.Get("signature"))
	}
	if query.Get("payment_amount") != "10001" || query.Get("payment_id") != "order-1" || query.Get("project_id") != "123" {
		t.Fatalf("unexpected parameters: %v", query)
	}
	if intent.ProviderReference != "order-1" || intent.Status != "pending" || intent.Amount.Amount != 10001 {
		t.Fatalf("unexpected intent: %#v", intent)
	}
	if _, err := provider.Create(t.Context(), CreateRequest{OrderID: "order-1", Amount: commerce.Money{Amount: 100, Currency: "EUR"}}); !Refused(err) {
		t.Fatalf("a foreign currency must be refused, got %v", err)
	}
}

func TestEtoPlatezhiWebhookMatchesBedolagaVector(t *testing.T) {
	provider := etoplatezhiTestProvider(t)
	event, err := provider.VerifyWebhook(http.Header{}, []byte(etoplatezhiTestCallback))
	if err != nil {
		t.Fatal(err)
	}
	if event.ID != "success:order-1" || event.Status != "succeeded" || event.ProviderReference != "order-1" || event.MerchantReference != "order-1" || event.Amount != (commerce.Money{Amount: 10001, Currency: "RUB"}) {
		t.Fatalf("unexpected event: %#v", event)
	}
	if again, _ := provider.VerifyWebhook(http.Header{}, []byte(etoplatezhiTestCallback)); again.ID != event.ID {
		t.Fatal("a redelivery must carry the same ID")
	}
	if _, ok := Provider(provider).(WebhookAcknowledger); ok {
		t.Fatal("EtoPlatezhi needs only HTTP 200")
	}
}

func TestEtoPlatezhiWebhookRejectsForgeries(t *testing.T) {
	provider := etoplatezhiTestProvider(t)
	for _, tampered := range []string{
		strings.Replace(etoplatezhiTestCallback, `"amount":10001,"currency":"RUB"},"is_new`, `"amount":10,"currency":"RUB"},"is_new`, 1),
		strings.Replace(etoplatezhiTestCallback, `"rate":1.0`, `"rate":1.5`, 1),
		strings.Replace(etoplatezhiTestCallback, `"payment_id":""`, `"payment_id":"x"`, 1),
		strings.Replace(etoplatezhiTestCallback, `"signature":"FXLAQ`, `"signature":"GXLAQ`, 1),
		`not json`,
	} {
		if _, err := provider.VerifyWebhook(http.Header{}, []byte(tampered)); !errors.Is(err, ErrInvalidSignature) {
			t.Fatalf("a forged callback must fail, got %v", err)
		}
	}
}

func TestEtoPlatezhiWebhookStatuses(t *testing.T) {
	provider := etoplatezhiTestProvider(t)
	for status, expected := range map[string]string{
		"success": "succeeded", "decline": "failed", "error": "failed", "awaiting 3ds result": "pending",
		"processing": "pending", "refunded": "refunded", "reversed": "refunded", "brand new": "pending",
	} {
		payload := map[string]any{
			"project_id": json.Number("123"),
			"payment":    map[string]any{"id": "order-1", "status": status, "sum": map[string]any{"amount": json.Number("100"), "currency": "RUB"}},
		}
		signature, _ := provider.sign(payload)
		payload["signature"] = signature
		raw, _ := json.Marshal(payload)
		event, err := provider.VerifyWebhook(http.Header{}, raw)
		if err != nil || event.Status != expected {
			t.Fatalf("%s: got %#v %v", status, event, err)
		}
	}
	foreign := map[string]any{"project_id": json.Number("999"), "payment": map[string]any{"id": "order-1", "status": "success", "sum": map[string]any{"amount": json.Number("100"), "currency": "RUB"}}}
	signature, _ := provider.sign(foreign)
	foreign["signature"] = signature
	raw, _ := json.Marshal(foreign)
	if _, err := provider.VerifyWebhook(http.Header{}, raw); !errors.Is(err, ErrProviderResponse) {
		t.Fatalf("another project's callback must be rejected, got %v", err)
	}
}

func TestEtoPlatezhiPollAndRefundAreUnsupported(t *testing.T) {
	provider := etoplatezhiTestProvider(t)
	if _, err := provider.Poll(t.Context(), "order-1"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported, got %v", err)
	}
	if _, err := provider.Refund(t.Context(), RefundRequest{PaymentReference: "order-1", IdempotencyKey: "refund-key"}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported, got %v", err)
	}
}
