package payments

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/omniflow/omniflow/internal/commerce"
)

// The vectors below were produced by the reference integration's own
// serialiser and signer (json.dumps with ensure_ascii=False, compact
// separators, slashes escaped; md5(base64(body) + key)).
const (
	heleketTestKey          = "heleket-test-api-key"
	heleketTestOrder        = "0b8f3c8e-1f7a-4d4e-9a4e-2f6f0f6b9a10"
	heleketTestRequestBody  = `{"amount":"150.00","currency":"RUB","lifetime":3600,"order_id":"0b8f3c8e-1f7a-4d4e-9a4e-2f6f0f6b9a10","to_currency":"USDT","url_callback":"https:\/\/omniflow.example\/v1\/payments\/webhooks\/heleket","url_return":"https:\/\/omniflow.example\/account","url_success":"https:\/\/omniflow.example\/account"}`
	heleketTestRequestSign  = "473b32751aa28577c667fce4b67455b1"
	heleketTestWebhookStart = `{"type":"payment","uuid":"62f88b36-a9d5-4fa6-aa26-e040c3dbf26d","order_id":"0b8f3c8e-1f7a-4d4e-9a4e-2f6f0f6b9a10","amount":"150.00","payment_amount":"1.65","payment_amount_usd":"1.65","merchant_amount":"1.62","commission":"0.03","is_final":true,"status":"paid","from":"TXYZ","wallet_address_uuid":null,"network":"tron","currency":"RUB","payer_currency":"USDT","additional_data":null,"convert":{"to_currency":"USDT","commission":null,"rate":"0.011","amount":"1.62"},"txid":"abc/def","note":"Оплата","fee":1.50`
	heleketTestWebhookSign  = "0fbd4bc06f698aed8825f3c7dfd071ce"
)

func heleketTestEnv(values map[string]string) Settings {
	return Settings{Env: func(key string) string { return values[key] }, PublicURL: "https://omniflow.example/"}
}

func heleketTestProvider(t *testing.T, server *httptest.Server) *Heleket {
	t.Helper()
	built, err := buildHeleket(heleketTestEnv(map[string]string{
		"APP_HELEKET_MERCHANT_ID": "merchant-uuid",
		"APP_HELEKET_API_KEY":     heleketTestKey,
	}))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	provider := built.(*Heleket)
	if server != nil {
		provider.baseURL = server.URL
		provider.http = server.Client()
	}
	return provider
}

func heleketTestWebhook(sign string) []byte {
	return []byte(heleketTestWebhookStart + `,"sign":"` + sign + `"}`)
}

func TestHeleketBuilder(t *testing.T) {
	t.Parallel()
	if _, err := buildHeleket(heleketTestEnv(nil)); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("no credentials must be ErrNotConfigured, got %v", err)
	}
	if _, err := buildHeleket(heleketTestEnv(map[string]string{"APP_HELEKET_MERCHANT_ID": "m"})); err == nil || errors.Is(err, ErrNotConfigured) {
		t.Fatalf("half a configuration must be a startup error, got %v", err)
	}
	if _, err := buildHeleket(heleketTestEnv(map[string]string{"APP_HELEKET_API_KEY": "k"})); err == nil || errors.Is(err, ErrNotConfigured) {
		t.Fatalf("half a configuration must be a startup error, got %v", err)
	}
	noPublic := Settings{Env: func(key string) string {
		return map[string]string{"APP_HELEKET_MERCHANT_ID": "m", "APP_HELEKET_API_KEY": "k"}[key]
	}}
	if _, err := buildHeleket(noPublic); err == nil {
		t.Fatal("a missing APP_PUBLIC_URL must be reported: every invoice carries url_callback")
	}
	if _, err := buildHeleket(heleketTestEnv(map[string]string{"APP_HELEKET_MERCHANT_ID": "m", "APP_HELEKET_API_KEY": "k", "APP_HELEKET_MARKUP_PERCENT": "ten"})); err == nil {
		t.Fatal("a malformed markup must be reported")
	}
	built, err := buildHeleket(heleketTestEnv(map[string]string{
		"APP_HELEKET_MERCHANT_ID": "m", "APP_HELEKET_API_KEY": "k",
		"APP_HELEKET_INVOICE_LIFETIME": "60", "APP_HELEKET_MARKUP_PERCENT": "2.5",
	}))
	if err != nil {
		t.Fatalf("configured builder: %v", err)
	}
	provider := built.(*Heleket)
	if provider.Name() != "heleket" || provider.lifetime != heleketMinLifetime || provider.toCurrency != "USDT" {
		t.Fatalf("unexpected defaults: %+v", provider)
	}
	// 2.5 rounds half-to-even to 2, sent as a negative discount.
	if provider.discountPercent == nil || *provider.discountPercent != -2 {
		t.Fatalf("markup must become discount_percent -2, got %v", provider.discountPercent)
	}
	if !provider.SupportsCurrency("rub") || provider.SupportsCurrency("XTR") {
		t.Fatal("unexpected currency support")
	}
	caps := provider.Capabilities()
	if caps.Refunds || caps.Recurring || !caps.Webhooks || !caps.Polling {
		t.Fatalf("unexpected capabilities %+v", caps)
	}
}

func TestHeleketCreateSignsTheExactBody(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		if request.Method != http.MethodPost || request.URL.Path != "/payment" {
			t.Errorf("unexpected %s %s", request.Method, request.URL.Path)
		}
		if string(body) != heleketTestRequestBody {
			t.Errorf("body differs from the reference serialisation:\n got %s\nwant %s", body, heleketTestRequestBody)
		}
		if request.Header.Get("merchant") != "merchant-uuid" || request.Header.Get("sign") != heleketTestRequestSign {
			t.Errorf("unexpected auth headers merchant=%q sign=%q", request.Header.Get("merchant"), request.Header.Get("sign"))
		}
		_, _ = io.WriteString(writer, `{"state":0,"result":{"uuid":"62f88b36-a9d5-4fa6-aa26-e040c3dbf26d","order_id":"`+heleketTestOrder+`","amount":"150.00","currency":"RUB","url":"https://pay.heleket.com/pay/62f88b36","status":"check","expired_at":1750000000}}`)
	}))
	defer server.Close()
	provider := heleketTestProvider(t, server)
	intent, err := provider.Create(context.Background(), CreateRequest{
		OrderID: heleketTestOrder, Amount: commerce.Money{Amount: 15000, Currency: "RUB"},
		ReturnURL: "https://omniflow.example/account",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if intent.ProviderReference != "62f88b36-a9d5-4fa6-aa26-e040c3dbf26d" || intent.Status != "pending" ||
		intent.CheckoutURL != "https://pay.heleket.com/pay/62f88b36" || intent.Amount.Amount != 15000 ||
		intent.ExpiresAt == nil || intent.ExpiresAt.Unix() != 1750000000 {
		t.Fatalf("unexpected intent %+v", intent)
	}
}

func TestHeleketCreateRejectsANonZeroState(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, `{"state":1,"message":"Merchant not found"}`)
	}))
	defer server.Close()
	_, err := heleketTestProvider(t, server).Create(context.Background(), CreateRequest{OrderID: heleketTestOrder, Amount: commerce.Money{Amount: 100, Currency: "RUB"}})
	if !errors.Is(err, ErrProviderResponse) {
		t.Fatalf("expected a provider response error, got %v", err)
	}
}

func TestHeleketPollMapsStatuses(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"paid": "succeeded", "paid_over": "succeeded", "cancel": "cancelled", "fail": "failed",
		"system_fail": "failed", "check": "pending", "wrong_amount": "pending", "process": "processing",
		"refund_paid": "refunded", "something_new": "pending",
	}
	for remote, want := range cases {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			body, _ := io.ReadAll(request.Body)
			if request.URL.Path != "/payment/info" || string(body) != `{"uuid":"u-1"}` {
				t.Errorf("unexpected poll %s %s", request.URL.Path, body)
			}
			_, _ = io.WriteString(writer, `{"state":0,"result":{"uuid":"u-1","order_id":"o","amount":"10.5","currency":"RUB","status":"`+remote+`"}}`)
		}))
		intent, err := heleketTestProvider(t, server).Poll(context.Background(), "u-1")
		server.Close()
		if err != nil {
			t.Fatalf("%s: %v", remote, err)
		}
		if intent.Status != want || intent.Amount.Amount != 1050 {
			t.Fatalf("%s: got %s %d, want %s 1050", remote, intent.Status, intent.Amount.Amount, want)
		}
	}
}

func TestHeleketWebhookMatchesTheReferenceSignature(t *testing.T) {
	t.Parallel()
	provider := heleketTestProvider(t, nil)
	body := heleketTestWebhook(heleketTestWebhookSign)
	event, err := provider.VerifyWebhook(http.Header{}, body)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if event.Status != "succeeded" || event.ProviderReference != "62f88b36-a9d5-4fa6-aa26-e040c3dbf26d" ||
		event.MerchantReference != heleketTestOrder || event.Amount != (commerce.Money{Amount: 15000, Currency: "RUB"}) {
		t.Fatalf("unexpected event %+v", event)
	}
	repeat, err := provider.VerifyWebhook(http.Header{}, body)
	if err != nil || repeat.ID != event.ID {
		t.Fatalf("a redelivery must keep its ID: %q vs %q (%v)", repeat.ID, event.ID, err)
	}
}

func TestHeleketWebhookRejectsForgeries(t *testing.T) {
	t.Parallel()
	provider := heleketTestProvider(t, nil)
	tampered := []byte(strings.Replace(string(heleketTestWebhook(heleketTestWebhookSign)), `"amount":"150.00"`, `"amount":"1500.00"`, 1))
	for name, body := range map[string][]byte{
		"tampered amount": tampered,
		"wrong signature": heleketTestWebhook("00000000000000000000000000000000"),
		"no signature":    []byte(heleketTestWebhookStart + `}`),
	} {
		if _, err := provider.VerifyWebhook(http.Header{}, body); !errors.Is(err, ErrInvalidSignature) {
			t.Fatalf("%s: expected a signature rejection, got %v", name, err)
		}
	}
	if _, err := provider.VerifyWebhook(http.Header{}, []byte(`not json`)); err == nil {
		t.Fatal("garbage must be rejected")
	}
}

func TestHeleketWebhookWithAnUnknownStatusDoesNotSettle(t *testing.T) {
	t.Parallel()
	provider := heleketTestProvider(t, nil)
	payload := map[string]any{"uuid": "u-2", "order_id": heleketTestOrder, "amount": "5.00", "currency": "RUB", "status": "brand_new_state"}
	// The request serialiser yields a body already in canonical form, so its
	// signature is the one Heleket would attach.
	unsigned, _ := heleketRequestBody(payload)
	body := append(unsigned[:len(unsigned)-1:len(unsigned)-1], []byte(`,"sign":"`+heleketSign(unsigned, heleketTestKey)+`"}`)...)
	event, err := provider.VerifyWebhook(http.Header{}, body)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if event.Status != "pending" {
		t.Fatalf("an unknown status must not settle, got %q", event.Status)
	}
}

func TestHeleketErrorsSeparateRefusalFromOutage(t *testing.T) {
	t.Parallel()
	for status, refused := range map[int]bool{400: true, 401: true, 422: true, 500: false, 502: false, 429: false} {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(status)
		}))
		_, err := heleketTestProvider(t, server).Create(context.Background(), CreateRequest{OrderID: heleketTestOrder, Amount: commerce.Money{Amount: 100, Currency: "RUB"}})
		server.Close()
		if Refused(err) != refused {
			t.Fatalf("HTTP %d: Refused=%v, want %v (%v)", status, Refused(err), refused, err)
		}
	}
	if _, err := heleketTestProvider(t, nil).Refund(context.Background(), RefundRequest{}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("refunds are not ported, got %v", err)
	}
}

func TestHeleketProbeListsPayments(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/payment/list" || request.Header.Get("sign") != heleketSign([]byte(`{}`), heleketTestKey) {
			t.Errorf("unexpected probe %s", request.URL.Path)
		}
		_, _ = io.WriteString(writer, `{"state":0,"result":{"items":[],"paginate":{}}}`)
	}))
	defer server.Close()
	if err := Probe(context.Background(), heleketTestProvider(t, server)); err != nil {
		t.Fatalf("probe: %v", err)
	}
}

func TestHeleketPythonFloatMatchesRepr(t *testing.T) {
	t.Parallel()
	for literal, want := range map[string]string{
		"1.50": "1.5", "100.0": "100.0", "1e2": "100.0", "0.0001": "0.0001", "0.00001": "1e-05",
		"1e16": "1e+16", "1e15": "1000000000000000.0", "42": "42", "-0.5": "-0.5",
	} {
		if got := heleketPythonNumber(literal); got != want {
			t.Fatalf("%s: got %s, want %s", literal, got, want)
		}
	}
}
