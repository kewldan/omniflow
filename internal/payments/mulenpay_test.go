package payments

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/omniflow/omniflow/internal/commerce"
)

// Vectors produced by the reference integration: sha1("rub" + "150.00" +
// "4242" + secret) for creation, and sha1 over the str() of every value, joined,
// plus the secret, over a parsed callback for the legacy checksum.
const (
	mulenPayTestSecret     = "mulen-secret"
	mulenPayTestOrder      = "0b8f3c8e-1f7a-4d4e-9a4e-2f6f0f6b9a10"
	mulenPayTestCreateSign = "71f3422a54b8f4afa5436ee7c59661f3baf915ac"
	mulenPayTestHookStart  = `{"id":777,"uuid":"0b8f3c8e-1f7a-4d4e-9a4e-2f6f0f6b9a10","amount":150.0,"currency":"rub","payment_status":"success","is_test":false`
	mulenPayTestHookSign   = "74aa9d87863cf1e072b0a3af549754e9291c3954"
)

func mulenPayTestSettings(values map[string]string) Settings {
	return Settings{Env: func(key string) string { return values[key] }, PublicURL: "https://omniflow.example"}
}

func mulenPayTestProvider(t *testing.T, server *httptest.Server) *MulenPay {
	t.Helper()
	built, err := buildMulenPay(mulenPayTestSettings(map[string]string{
		"APP_MULENPAY_API_KEY": "mulen-api-key", "APP_MULENPAY_SECRET_KEY": mulenPayTestSecret, "APP_MULENPAY_SHOP_ID": "4242",
	}))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	provider := built.(*MulenPay)
	if server != nil {
		provider.baseURL = server.URL
		provider.http = server.Client()
	}
	return provider
}

func TestMulenPayBuilder(t *testing.T) {
	t.Parallel()
	if _, err := buildMulenPay(mulenPayTestSettings(nil)); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("no credentials must be ErrNotConfigured, got %v", err)
	}
	for _, partial := range []map[string]string{
		{"APP_MULENPAY_API_KEY": "k"},
		{"APP_MULENPAY_API_KEY": "k", "APP_MULENPAY_SECRET_KEY": "s"},
		{"APP_MULENPAY_SHOP_ID": "1"},
	} {
		if _, err := buildMulenPay(mulenPayTestSettings(partial)); err == nil || errors.Is(err, ErrNotConfigured) {
			t.Fatalf("%v: half a configuration must be a startup error, got %v", partial, err)
		}
	}
	if _, err := buildMulenPay(mulenPayTestSettings(map[string]string{"APP_MULENPAY_API_KEY": "k", "APP_MULENPAY_SECRET_KEY": "s", "APP_MULENPAY_SHOP_ID": "shop"})); err == nil {
		t.Fatal("a non-numeric shop ID must be rejected")
	}
	provider := mulenPayTestProvider(t, nil)
	if provider.Name() != "mulenpay" || provider.language != "ru" || provider.paymentSubject != 4 || provider.paymentMode != 4 ||
		provider.vatCode != 0 || provider.websiteURL != "https://omniflow.example" || provider.baseURL != mulenPayDefaultBaseURL {
		t.Fatalf("unexpected defaults %+v", provider)
	}
	if !provider.SupportsCurrency("RUB") || provider.SupportsCurrency("USD") || !NeedsWebhookPolling(provider) {
		t.Fatal("MulenPay settles RUB only and needs polled webhooks")
	}
}

func TestMulenPayCreateRequestShape(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v2/payments" {
			t.Errorf("unexpected %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer mulen-api-key" {
			t.Errorf("unexpected auth %q", request.Header.Get("Authorization"))
		}
		var body map[string]any
		decoder := json.NewDecoder(request.Body)
		decoder.UseNumber()
		_ = decoder.Decode(&body)
		if body["currency"] != "rub" || body["amount"] != "150.00" || body["uuid"] != mulenPayTestOrder ||
			body["shopId"] != json.Number("4242") || body["sign"] != mulenPayTestCreateSign ||
			body["language"] != "ru" || body["website_url"] != "https://omniflow.example" || body["client"] != "payer@example.com" {
			t.Errorf("unexpected body %v", body)
		}
		items, _ := body["items"].([]any)
		item, _ := items[0].(map[string]any)
		if len(items) != 1 || item["price"] != json.Number("150.00") || item["quantity"] != json.Number("1") ||
			item["payment_subject"] != json.Number("4") || len([]rune(item["description"].(string))) != 128 {
			t.Errorf("unexpected receipt items %v", items)
		}
		_, _ = io.WriteString(writer, `{"success":true,"id":777,"paymentUrl":"https://mulenpay.ru/payment/777"}`)
	}))
	defer server.Close()
	intent, err := mulenPayTestProvider(t, server).Create(context.Background(), CreateRequest{
		OrderID: mulenPayTestOrder, Amount: commerce.Money{Amount: 15000, Currency: "RUB"},
		Description: strings.Repeat("я", 200), Metadata: map[string]string{"customer_email": "payer@example.com"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if intent.ProviderReference != "777" || intent.CheckoutURL != "https://mulenpay.ru/payment/777" || intent.Status != "pending" || intent.Amount.Amount != 15000 {
		t.Fatalf("unexpected intent %+v", intent)
	}
}

func TestMulenPayCreateFallsBackToTheOrderAsClient(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(request.Body).Decode(&body)
		if body["client"] != mulenPayTestOrder {
			t.Errorf("client must fall back to the order reference, got %v", body["client"])
		}
		_, _ = io.WriteString(writer, `{"success":false}`)
	}))
	defer server.Close()
	_, err := mulenPayTestProvider(t, server).Create(context.Background(), CreateRequest{OrderID: mulenPayTestOrder, Amount: commerce.Money{Amount: 100, Currency: "RUB"}})
	if !errors.Is(err, ErrProviderResponse) {
		t.Fatalf("success=false must be a provider error, got %v", err)
	}
	if _, err := mulenPayTestProvider(t, nil).Create(context.Background(), CreateRequest{OrderID: "o", Amount: commerce.Money{Amount: 100, Currency: "USD"}}); err == nil {
		t.Fatal("a non-RUB order must be refused before calling MulenPay")
	}
}

func TestMulenPayPollMapsStatusCodes(t *testing.T) {
	t.Parallel()
	cases := map[string]string{"0": "pending", "1": "processing", "2": "cancelled", "3": "succeeded", "4": "failed", "5": "processing", "6": "processing", "9": "pending"}
	for code, want := range cases {
		for _, shape := range []string{
			`{"success":true,"payment":{"id":777,"status":` + code + `,"amount":"150.00","currency":"rub"}}`,
			`{"id":777,"status":` + code + `,"amount":150.0,"currency":"rub"}`,
		} {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.Method != http.MethodGet || request.URL.Path != "/v2/payments/777" {
					t.Errorf("unexpected poll %s %s", request.Method, request.URL.Path)
				}
				_, _ = io.WriteString(writer, shape)
			}))
			intent, err := mulenPayTestProvider(t, server).Poll(context.Background(), "777")
			server.Close()
			if err != nil {
				t.Fatalf("code %s: %v", code, err)
			}
			if intent.Status != want || intent.Amount != (commerce.Money{Amount: 15000, Currency: "RUB"}) {
				t.Fatalf("code %s: got %+v, want %s", code, intent, want)
			}
		}
	}
}

func TestMulenPayPollRejectsAnAnswerForAnotherPayment(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, `{"id":778,"status":3,"amount":"150.00","currency":"rub"}`)
	}))
	defer server.Close()
	if _, err := mulenPayTestProvider(t, server).Poll(context.Background(), "777"); !errors.Is(err, ErrProviderResponse) {
		t.Fatalf("a mismatched payment must be rejected, got %v", err)
	}
}

func TestMulenPayWebhookChecksumAndHint(t *testing.T) {
	t.Parallel()
	provider := mulenPayTestProvider(t, nil)
	signed := []byte(mulenPayTestHookStart + `,"sign":"` + mulenPayTestHookSign + `"}`)
	event, err := provider.VerifyWebhook(http.Header{}, signed)
	if err != nil {
		t.Fatalf("a callback with the reference checksum must verify: %v", err)
	}
	if event.ProviderReference != "777" || event.MerchantReference != mulenPayTestOrder || event.Status != "succeeded" ||
		event.Amount != (commerce.Money{Amount: 15000, Currency: "RUB"}) {
		t.Fatalf("unexpected event %+v", event)
	}
	repeat, _ := provider.VerifyWebhook(http.Header{}, signed)
	if repeat.ID != event.ID || event.ID != "success:777" {
		t.Fatalf("redeliveries must share one ID: %q vs %q", repeat.ID, event.ID)
	}
	// Unsigned callbacks are accepted as hints: the service polls before acting.
	if _, err := provider.VerifyWebhook(http.Header{}, []byte(mulenPayTestHookStart+`}`)); err != nil {
		t.Fatalf("an unsigned callback is a hint, got %v", err)
	}
	tampered := strings.Replace(string(signed), `"amount":150.0`, `"amount":1500.0`, 1)
	for name, body := range map[string]string{
		"tampered amount": tampered,
		"wrong checksum":  mulenPayTestHookStart + `,"sign":"deadbeef"}`,
		"empty checksum":  mulenPayTestHookStart + `,"sign":""}`,
		"nested value":    `{"id":777,"amount":"1","currency":"rub","status":"success","extra":{"a":1},"sign":"` + mulenPayTestHookSign + `"}`,
	} {
		if _, err := provider.VerifyWebhook(http.Header{}, []byte(body)); !errors.Is(err, ErrInvalidSignature) {
			t.Fatalf("%s: expected a signature rejection, got %v", name, err)
		}
	}
	for name, body := range map[string]string{
		"no id":       `{"amount":"1","currency":"rub","payment_status":"success"}`,
		"no amount":   `{"id":1,"currency":"rub","payment_status":"success"}`,
		"no status":   `{"id":1,"amount":"1","currency":"rub"}`,
		"negative id": `{"id":-1,"amount":"1","currency":"rub","payment_status":"success"}`,
		"garbage":     `[]`,
	} {
		if _, err := provider.VerifyWebhook(http.Header{}, []byte(body)); err == nil {
			t.Fatalf("%s: must be rejected", name)
		}
	}
	unknown, err := provider.VerifyWebhook(http.Header{}, []byte(`{"id":5,"amount":"1.00","currency":"rub","payment_status":"weird"}`))
	if err != nil || unknown.Status != "pending" {
		t.Fatalf("an unknown status must not settle: %+v %v", unknown, err)
	}
}

func TestMulenPayErrorsAndUnsupported(t *testing.T) {
	t.Parallel()
	for status, refused := range map[int]bool{400: true, 401: true, 404: true, 500: false, 503: false, 429: false} {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(status)
		}))
		_, err := mulenPayTestProvider(t, server).Poll(context.Background(), "777")
		server.Close()
		if Refused(err) != refused {
			t.Fatalf("HTTP %d: Refused=%v, want %v", status, Refused(err), refused)
		}
	}
	if _, err := mulenPayTestProvider(t, nil).Refund(context.Background(), RefundRequest{}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("refunds are not ported, got %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v2/payments" || request.URL.Query().Get("limit") != "1" {
			t.Errorf("unexpected probe %s", request.URL)
		}
		_, _ = io.WriteString(writer, `{"items":[]}`)
	}))
	defer server.Close()
	if err := Probe(context.Background(), mulenPayTestProvider(t, server)); err != nil {
		t.Fatalf("probe: %v", err)
	}
}
