package payments

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/omniflow/omniflow/internal/commerce"
)

// pal24TestSign is upper(md5("150.00:<order>:pal24-signature-token")), computed
// by the reference integration's calculate_signature.
const (
	pal24TestOrder = "0b8f3c8e-1f7a-4d4e-9a4e-2f6f0f6b9a10"
	pal24TestSign  = "EE937CAB222C6EBE24192283E8904542"
)

func pal24TestSettings(values map[string]string) Settings {
	return Settings{Env: func(key string) string { return values[key] }}
}

func pal24TestProvider(t *testing.T, server *httptest.Server, extra map[string]string) *Pal24 {
	t.Helper()
	values := map[string]string{"APP_PAL24_API_TOKEN": "pal24-api-token", "APP_PAL24_SHOP_ID": "shop-1", "APP_PAL24_SIGNATURE_TOKEN": "pal24-signature-token"}
	for key, value := range extra {
		values[key] = value
	}
	built, err := buildPal24(pal24TestSettings(values))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	provider := built.(*Pal24)
	if server != nil {
		provider.baseURL = server.URL + "/"
		provider.http = server.Client()
	}
	return provider
}

func pal24TestPostback(status, outSum, sign string) url.Values {
	return url.Values{
		"InvId": {pal24TestOrder}, "OutSum": {outSum}, "Status": {status}, "TrsId": {"trs-1"},
		"CurrencyIn": {"RUB"}, "SignatureValue": {sign}, "Commission": {"3.00"},
	}
}

func TestPal24Builder(t *testing.T) {
	t.Parallel()
	if _, err := buildPal24(pal24TestSettings(nil)); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("no credentials must be ErrNotConfigured, got %v", err)
	}
	for _, partial := range []map[string]string{{"APP_PAL24_API_TOKEN": "t"}, {"APP_PAL24_SHOP_ID": "s"}, {"APP_PAL24_SIGNATURE_TOKEN": "x"}} {
		if _, err := buildPal24(pal24TestSettings(partial)); err == nil || errors.Is(err, ErrNotConfigured) {
			t.Fatalf("%v: half a configuration must be a startup error, got %v", partial, err)
		}
	}
	if _, err := buildPal24(pal24TestSettings(map[string]string{"APP_PAL24_API_TOKEN": "t", "APP_PAL24_SHOP_ID": "s", "APP_PAL24_PAYMENT_METHOD": "crypto"})); err == nil {
		t.Fatal("an unknown payment method must be reported")
	}
	built, err := buildPal24(pal24TestSettings(map[string]string{"APP_PAL24_API_TOKEN": "t", "APP_PAL24_SHOP_ID": "s"}))
	if err != nil {
		t.Fatalf("configured: %v", err)
	}
	provider := built.(*Pal24)
	if provider.signatureToken != "t" || provider.paymentMethod != "SBP" || provider.baseURL != pal24DefaultBaseURL {
		t.Fatalf("signature token must fall back to the API token and method to SBP: %+v", provider)
	}
	card := pal24TestProvider(t, nil, map[string]string{"APP_PAL24_PAYMENT_METHOD": "card"})
	if card.paymentMethod != "BANK_CARD" || !card.SupportsCurrency("RUB") || card.SupportsCurrency("EUR") {
		t.Fatalf("unexpected card configuration %+v", card)
	}
}

func TestPal24CreateRequestShape(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/bill/create" || request.Header.Get("Authorization") != "Bearer pal24-api-token" {
			t.Errorf("unexpected %s %s auth=%q", request.Method, request.URL.Path, request.Header.Get("Authorization"))
		}
		var body map[string]any
		_ = json.NewDecoder(request.Body).Decode(&body)
		want := map[string]any{"amount": "150.00", "shop_id": "shop-1", "currency_in": "RUB", "type": "normal", "order_id": pal24TestOrder, "payment_method": "SBP", "description": "Top up"}
		for key, value := range want {
			if body[key] != value {
				t.Errorf("%s: got %v, want %v", key, body[key], value)
			}
		}
		_, _ = io.WriteString(writer, `{"success":"true","link_url":"https://pal24.pro/link/abc","link_page_url":"https://pal24.pro/page/abc","bill_id":"abc"}`)
	}))
	defer server.Close()
	intent, err := pal24TestProvider(t, server, nil).Create(context.Background(), CreateRequest{
		OrderID: pal24TestOrder, Amount: commerce.Money{Amount: 15000, Currency: "RUB"}, Description: "Top up",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if intent.ProviderReference != "abc" || intent.CheckoutURL != "https://pal24.pro/page/abc" || intent.Status != "pending" || intent.Amount.Amount != 15000 {
		t.Fatalf("unexpected intent %+v", intent)
	}
}

func TestPal24CreateRejectsSuccessFalse(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, `{"success":false,"message":"shop is not active"}`)
	}))
	defer server.Close()
	_, err := pal24TestProvider(t, server, nil).Create(context.Background(), CreateRequest{OrderID: pal24TestOrder, Amount: commerce.Money{Amount: 100, Currency: "RUB"}})
	if !errors.Is(err, ErrProviderResponse) {
		t.Fatalf("expected a provider response error, got %v", err)
	}
}

func TestPal24PollMapsBillStatuses(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"NEW": "pending", "PROCESS": "processing", "UNDERPAID": "pending", "SUCCESS": "succeeded",
		"OVERPAID": "succeeded", "FAIL": "failed", "CANCELLED": "cancelled", "SOMETHING": "pending",
	}
	for remote, want := range cases {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if request.Method != http.MethodGet || request.URL.Path != "/bill/status" || request.URL.Query().Get("id") != "abc" {
				t.Errorf("unexpected poll %s %s", request.Method, request.URL)
			}
			_, _ = io.WriteString(writer, `{"success":"true","id":"abc","status":"`+remote+`","amount":"150.00","currency_in":"RUB"}`)
		}))
		intent, err := pal24TestProvider(t, server, nil).Poll(context.Background(), "abc")
		server.Close()
		if err != nil {
			t.Fatalf("%s: %v", remote, err)
		}
		if intent.Status != want || intent.Amount != (commerce.Money{Amount: 15000, Currency: "RUB"}) {
			t.Fatalf("%s: got %+v, want %s", remote, intent, want)
		}
	}
}

func TestPal24WebhookSignature(t *testing.T) {
	t.Parallel()
	provider := pal24TestProvider(t, nil, nil)
	form := []byte(pal24TestPostback("SUCCESS", "150.00", pal24TestSign).Encode())
	headers := http.Header{"Content-Type": {"application/x-www-form-urlencoded"}}
	event, err := provider.VerifyWebhook(headers, form)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if event.Status != "succeeded" || event.MerchantReference != pal24TestOrder || event.Amount != (commerce.Money{Amount: 15000, Currency: "RUB"}) {
		t.Fatalf("unexpected event %+v", event)
	}
	repeat, _ := provider.VerifyWebhook(headers, form)
	if repeat.ID != event.ID || event.ID != "SUCCESS:"+pal24TestOrder+":trs-1" {
		t.Fatalf("a redelivery must keep its ID: %q vs %q", repeat.ID, event.ID)
	}
	// A lower-case signature is the same signature.
	lower, err := provider.VerifyWebhook(headers, []byte(pal24TestPostback("SUCCESS", "150.00", strings.ToLower(pal24TestSign)).Encode()))
	if err != nil || lower.ID != event.ID {
		t.Fatalf("a lower-case signature must verify: %v", err)
	}
	// The same postback as JSON is accepted too.
	jsonBody, _ := json.Marshal(map[string]any{"InvId": pal24TestOrder, "OutSum": "150.00", "Status": "SUCCESS", "TrsId": "trs-1", "SignatureValue": pal24TestSign})
	if _, err := provider.VerifyWebhook(http.Header{"Content-Type": {"application/json"}}, jsonBody); err != nil {
		t.Fatalf("a JSON postback must verify: %v", err)
	}
	failed, err := provider.VerifyWebhook(headers, []byte(pal24TestPostback("FAIL", "150.00", pal24TestSign).Encode()))
	if err != nil || failed.Status != "failed" || failed.ID == event.ID {
		t.Fatalf("a FAIL postback must not settle and must have its own ID: %+v %v", failed, err)
	}
	unknown, err := provider.VerifyWebhook(headers, []byte(pal24TestPostback("MYSTERY", "150.00", pal24TestSign).Encode()))
	if err != nil || unknown.Status != "pending" {
		t.Fatalf("an unknown status must not settle: %+v %v", unknown, err)
	}
}

func TestPal24WebhookRejectsForgeries(t *testing.T) {
	t.Parallel()
	provider := pal24TestProvider(t, nil, nil)
	headers := http.Header{"Content-Type": {"application/x-www-form-urlencoded"}}
	for name, body := range map[string]string{
		"tampered amount": pal24TestPostback("SUCCESS", "1500.00", pal24TestSign).Encode(),
		"wrong signature": pal24TestPostback("SUCCESS", "150.00", "00000000000000000000000000000000").Encode(),
	} {
		if _, err := provider.VerifyWebhook(headers, []byte(body)); !errors.Is(err, ErrInvalidSignature) {
			t.Fatalf("%s: expected a signature rejection, got %v", name, err)
		}
	}
	// A provider configured without a signature token signs with the API token.
	fallback := pal24TestProvider(t, nil, map[string]string{"APP_PAL24_SIGNATURE_TOKEN": ""})
	if _, err := fallback.VerifyWebhook(headers, []byte(pal24TestPostback("SUCCESS", "150.00", pal24TestSign).Encode())); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("a signature made with another token must be rejected, got %v", err)
	}
	if _, err := fallback.VerifyWebhook(headers, []byte(pal24TestPostback("SUCCESS", "150.00", pal24Sign("150.00", pal24TestOrder, "pal24-api-token")).Encode())); err != nil {
		t.Fatalf("the API token is the fallback signing key: %v", err)
	}
	missing := url.Values{"InvId": {pal24TestOrder}, "OutSum": {"150.00"}, "Status": {"SUCCESS"}}
	if _, err := provider.VerifyWebhook(headers, []byte(missing.Encode())); !errors.Is(err, ErrProviderResponse) {
		t.Fatalf("a postback without a signature must be rejected, got %v", err)
	}
}

func TestPal24CancelProbeAndErrors(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/bill/toggle_activity":
			var body map[string]any
			_ = json.NewDecoder(request.Body).Decode(&body)
			if body["id"] != "abc" || body["active"] != float64(0) {
				t.Errorf("unexpected toggle body %v", body)
			}
			_, _ = io.WriteString(writer, `{"success":true}`)
		case "/merchant/balance":
			_, _ = io.WriteString(writer, `{"success":true,"balances":[]}`)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	provider := pal24TestProvider(t, server, nil)
	if err := provider.CancelIntent(context.Background(), "abc"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if err := Probe(context.Background(), provider); err != nil {
		t.Fatalf("probe: %v", err)
	}
	for status, refused := range map[int]bool{400: true, 401: true, 500: false, 502: false} {
		failing := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(status)
		}))
		_, err := pal24TestProvider(t, failing, nil).Create(context.Background(), CreateRequest{OrderID: pal24TestOrder, Amount: commerce.Money{Amount: 100, Currency: "RUB"}})
		failing.Close()
		if Refused(err) != refused {
			t.Fatalf("HTTP %d: Refused=%v, want %v", status, Refused(err), refused)
		}
	}
	if _, err := provider.Refund(context.Background(), RefundRequest{}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("refunds are not ported, got %v", err)
	}
}
