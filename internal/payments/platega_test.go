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
	"time"

	"github.com/omniflow/omniflow/internal/commerce"
)

const plategaTestOrder = "0b8f3c8e-1f7a-4d4e-9a4e-2f6f0f6b9a10"

func plategaTestSettings(values map[string]string) Settings {
	return Settings{Env: func(key string) string { return values[key] }}
}

func plategaTestProvider(t *testing.T, server *httptest.Server, extra map[string]string) *Platega {
	t.Helper()
	values := map[string]string{"APP_PLATEGA_MERCHANT_ID": "merchant-1", "APP_PLATEGA_SECRET": "platega-secret"}
	for key, value := range extra {
		values[key] = value
	}
	built, err := buildPlatega(plategaTestSettings(values))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	provider := built.(*Platega)
	provider.clock = func() time.Time { return time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC) }
	if server != nil {
		provider.baseURL = server.URL
		provider.http = server.Client()
	}
	return provider
}

func plategaTestHeaders() http.Header {
	return http.Header{"X-Merchantid": {"merchant-1"}, "X-Secret": {"platega-secret"}}
}

var plategaTestPayer = map[string]string{"customer_telegram_id": "555", "customer_name": "@payer"}

func TestPlategaBuilder(t *testing.T) {
	t.Parallel()
	if _, err := buildPlatega(plategaTestSettings(nil)); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("no credentials must be ErrNotConfigured, got %v", err)
	}
	for _, partial := range []map[string]string{{"APP_PLATEGA_MERCHANT_ID": "m"}, {"APP_PLATEGA_SECRET": "s"}} {
		if _, err := buildPlatega(plategaTestSettings(partial)); err == nil || errors.Is(err, ErrNotConfigured) {
			t.Fatalf("%v: half a configuration must be a startup error, got %v", partial, err)
		}
	}
	for name, extra := range map[string]map[string]string{
		"bad version":         {"APP_PLATEGA_API_VERSION": "v3"},
		"subscription method": {"APP_PLATEGA_PAYMENT_METHOD": "6"},
		"bad currency":        {"APP_PLATEGA_CURRENCY": "XYZ"},
	} {
		values := map[string]string{"APP_PLATEGA_MERCHANT_ID": "m", "APP_PLATEGA_SECRET": "s"}
		for key, value := range extra {
			values[key] = value
		}
		if _, err := buildPlatega(plategaTestSettings(values)); err == nil {
			t.Fatalf("%s must be reported", name)
		}
	}
	provider := plategaTestProvider(t, nil, nil)
	if provider.apiVersion != "v1" || provider.paymentMethod != 2 || provider.currency != "RUB" || provider.baseURL != plategaDefaultBaseURL {
		t.Fatalf("unexpected defaults %+v", provider)
	}
	suffixed := plategaTestProvider(t, nil, map[string]string{"APP_PLATEGA_BASE_URL": "https://app.platega.io/V2/"})
	if suffixed.apiVersion != "v2" || suffixed.baseURL != "https://app.platega.io" {
		t.Fatalf("a version suffix on the base URL must select that version: %+v", suffixed)
	}
	if !provider.SupportsCurrency("rub") || provider.SupportsCurrency("USD") {
		t.Fatal("only the configured currency is supported")
	}
}

func TestPlategaCreateRequestShape(t *testing.T) {
	t.Parallel()
	for version, path := range map[string]string{"v1": "/transaction/process", "v2": "/v2/transaction/process"} {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if request.Method != http.MethodPost || request.URL.Path != path {
				t.Errorf("%s: unexpected %s %s", version, request.Method, request.URL.Path)
			}
			if request.Header.Get("X-MerchantId") != "merchant-1" || request.Header.Get("X-Secret") != "platega-secret" {
				t.Errorf("missing auth headers")
			}
			var body map[string]any
			decoder := json.NewDecoder(request.Body)
			decoder.UseNumber()
			_ = decoder.Decode(&body)
			details, _ := body["paymentDetails"].(map[string]any)
			metadata, _ := body["metadata"].(map[string]any)
			if body["paymentMethod"] != json.Number("2") || details["amount"] != json.Number("500.00") || details["currency"] != "RUB" ||
				body["payload"] != plategaTestOrder || body["return"] != "https://omniflow.example/back" || body["failedUrl"] != "https://omniflow.example/back" {
				t.Errorf("unexpected body %v", body)
			}
			if metadata["user_email"] != "555@telegram.org" || metadata["userId"] != "555" || metadata["userName"] != "@payer" {
				t.Errorf("unexpected payer metadata %v", metadata)
			}
			if description, _ := body["description"].(string); len(description) > plategaDescriptionLimit || !strings.HasPrefix(description, "ж") {
				t.Errorf("description must be cut to %d bytes on a rune boundary: %q", plategaDescriptionLimit, description)
			}
			if version == "v1" {
				_, _ = io.WriteString(writer, `{"transactionId":"trx-001","redirect":"https://pay.platega.io?id=trx-001","status":"PENDING","expiresIn":"00:15:00"}`)
				return
			}
			_, _ = io.WriteString(writer, `{"transactionId":"trx-001","url":"https://pay.platega.io?id=trx-001","status":"PENDING"}`)
		}))
		provider := plategaTestProvider(t, server, map[string]string{"APP_PLATEGA_API_VERSION": version})
		intent, err := provider.Create(context.Background(), CreateRequest{
			OrderID: plategaTestOrder, Amount: commerce.Money{Amount: 50000, Currency: "RUB"},
			Description: strings.Repeat("ж", 40), ReturnURL: "https://omniflow.example/back", Metadata: plategaTestPayer,
		})
		server.Close()
		if err != nil {
			t.Fatalf("%s: create: %v", version, err)
		}
		if intent.ProviderReference != "trx-001" || intent.CheckoutURL != "https://pay.platega.io?id=trx-001" || intent.Status != "pending" {
			t.Fatalf("%s: unexpected intent %+v", version, intent)
		}
		if version == "v1" && (intent.ExpiresAt == nil || !intent.ExpiresAt.Equal(time.Date(2026, 11, 19, 12, 15, 0, 0, time.UTC))) {
			t.Fatalf("expiresIn must set the expiry, got %v", intent.ExpiresAt)
		}
	}
}

func TestPlategaCreateRequiresAPayer(t *testing.T) {
	t.Parallel()
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		called = true
		_, _ = io.WriteString(writer, `{"transactionId":"x"}`)
	}))
	defer server.Close()
	provider := plategaTestProvider(t, server, nil)
	for _, metadata := range []map[string]string{nil, {"order_id": plategaTestOrder}, {"customer_email": "not-an-email"}, {"customer_telegram_id": "abc"}} {
		if _, err := provider.Create(context.Background(), CreateRequest{OrderID: plategaTestOrder, Amount: commerce.Money{Amount: 100, Currency: "RUB"}, Metadata: metadata}); err == nil {
			t.Fatalf("%v: a payment without a buyer identity must not be sent", metadata)
		}
	}
	if called {
		t.Fatal("Platega must not be called without user_email")
	}
	if _, err := provider.Create(context.Background(), CreateRequest{OrderID: plategaTestOrder, Amount: commerce.Money{Amount: 100, Currency: "RUB"}, Metadata: map[string]string{"customer_email": "payer@example.com"}}); err != nil {
		t.Fatalf("an email is enough: %v", err)
	}
}

func TestPlategaPollMapsStatuses(t *testing.T) {
	t.Parallel()
	cases := map[string]string{"CONFIRMED": "succeeded", "PENDING": "pending", "INPROGRESS": "processing", "FAILED": "failed", "CANCELED": "cancelled", "EXPIRED": "expired", "NEW_THING": "pending"}
	for remote, want := range cases {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if request.Method != http.MethodGet || request.URL.Path != "/transaction/trx-001" {
				t.Errorf("unexpected poll %s %s", request.Method, request.URL.Path)
			}
			_, _ = io.WriteString(writer, `{"id":"trx-001","status":"`+remote+`","paymentDetails":{"amount":500,"currency":"RUB"}}`)
		}))
		intent, err := plategaTestProvider(t, server, nil).Poll(context.Background(), "trx-001")
		server.Close()
		if err != nil {
			t.Fatalf("%s: %v", remote, err)
		}
		if intent.Status != want || intent.Amount != (commerce.Money{Amount: 50000, Currency: "RUB"}) {
			t.Fatalf("%s: got %+v, want %s", remote, intent, want)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, `{"id":"trx-001","status":"CONFIRMED"}`)
	}))
	defer server.Close()
	if _, err := plategaTestProvider(t, server, nil).Poll(context.Background(), "trx-001"); !errors.Is(err, ErrProviderResponse) {
		t.Fatalf("a confirmation without an amount cannot settle, got %v", err)
	}
}

func TestPlategaWebhookAuthentication(t *testing.T) {
	t.Parallel()
	provider := plategaTestProvider(t, nil, nil)
	body := []byte(`{"id":"trx-001","amount":500.5,"currency":"RUB","status":"CONFIRMED","paymentMethod":2,"payload":"` + plategaTestOrder + `"}`)
	event, err := provider.VerifyWebhook(plategaTestHeaders(), body)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if event.Status != "succeeded" || event.ProviderReference != "trx-001" || event.MerchantReference != plategaTestOrder ||
		event.Amount != (commerce.Money{Amount: 50050, Currency: "RUB"}) {
		t.Fatalf("unexpected event %+v", event)
	}
	repeat, _ := provider.VerifyWebhook(plategaTestHeaders(), body)
	if repeat.ID != event.ID || event.ID != "CONFIRMED:trx-001" {
		t.Fatalf("a redelivery must keep its ID: %q vs %q", repeat.ID, event.ID)
	}
	for name, headers := range map[string]http.Header{
		"wrong secret":   {"X-Merchantid": {"merchant-1"}, "X-Secret": {"guess"}},
		"wrong merchant": {"X-Merchantid": {"merchant-2"}, "X-Secret": {"platega-secret"}},
		"no headers":     {},
	} {
		if _, err := provider.VerifyWebhook(headers, body); !errors.Is(err, ErrInvalidSignature) {
			t.Fatalf("%s: expected an authentication rejection, got %v", name, err)
		}
	}
	ping, err := provider.VerifyWebhook(http.Header{}, nil)
	if err != nil || ping.Status == "succeeded" || ping.ID != "ping" {
		t.Fatalf("the cabinet's empty ping must be acknowledged without effect: %+v %v", ping, err)
	}
	unknown, err := provider.VerifyWebhook(plategaTestHeaders(), []byte(`{"id":"trx-002","status":"WHATEVER"}`))
	if err != nil || unknown.Status != "pending" {
		t.Fatalf("an unknown status must not settle: %+v %v", unknown, err)
	}
	if _, err := provider.VerifyWebhook(plategaTestHeaders(), []byte(`{"id":"trx-003","status":"CONFIRMED"}`)); err == nil {
		t.Fatal("a confirmation without an amount must be rejected")
	}
	if _, err := provider.VerifyWebhook(plategaTestHeaders(), []byte(`{"id":"trx-003"}`)); err == nil {
		t.Fatal("a callback without a status must be rejected")
	}
}

func TestPlategaErrorsAndUnsupported(t *testing.T) {
	t.Parallel()
	for status, refused := range map[int]bool{400: true, 401: true, 404: true, 500: false, 504: false, 429: false} {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(status)
		}))
		_, err := plategaTestProvider(t, server, nil).Create(context.Background(), CreateRequest{OrderID: plategaTestOrder, Amount: commerce.Money{Amount: 100, Currency: "RUB"}, Metadata: plategaTestPayer})
		server.Close()
		if Refused(err) != refused {
			t.Fatalf("HTTP %d: Refused=%v, want %v", status, Refused(err), refused)
		}
	}
	if _, err := plategaTestProvider(t, nil, nil).Refund(context.Background(), RefundRequest{}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("refunds are not ported, got %v", err)
	}
}
