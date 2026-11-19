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

const (
	casheraTestOrder = "0b8f3c8e-1f7a-4d4e-9a4e-2f6f0f6b9a10"
	casheraTestUUID  = "9b1f2c4e-7a01-4b9d-8f1c-2eab57d90c11"
)

func casheraTestSettings(values map[string]string, publicURL string) Settings {
	return Settings{Env: func(key string) string { return values[key] }, PublicURL: publicURL}
}

func casheraTestProvider(t *testing.T, server *httptest.Server, extra map[string]string) *Cashera {
	t.Helper()
	values := map[string]string{"APP_CASHERA_API_KEY": "pk_test", "APP_CASHERA_API_SECRET": "sk_test"}
	for key, value := range extra {
		values[key] = value
	}
	built, err := buildCashera(casheraTestSettings(values, "https://omniflow.example"))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	provider := built.(*Cashera)
	if server != nil {
		provider.baseURL = server.URL
		provider.http = server.Client()
	}
	return provider
}

func casheraTestHeaders() http.Header {
	return http.Header{"X-Api-Key": {"pk_test"}, "X-Secret": {"sk_test"}}
}

// casheraTestEvent mirrors the notification shape of the reference tests.
func casheraTestEvent(status string, amount any) []byte {
	body, _ := json.Marshal(map[string]any{
		"event": "transaction.status_updated",
		"transaction": map[string]any{
			"uuid": casheraTestUUID, "external_id": casheraTestOrder, "status": status, "amount": amount,
			"currency": "RUB", "payment_method": "sbp", "paid_at": "2026-06-02T18:11:42+00:00",
		},
	})
	return body
}

func TestCasheraBuilder(t *testing.T) {
	t.Parallel()
	if _, err := buildCashera(casheraTestSettings(nil, "")); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("no credentials must be ErrNotConfigured, got %v", err)
	}
	for _, partial := range []map[string]string{{"APP_CASHERA_API_KEY": "pk"}, {"APP_CASHERA_API_SECRET": "sk"}} {
		if _, err := buildCashera(casheraTestSettings(partial, "")); err == nil || errors.Is(err, ErrNotConfigured) {
			t.Fatalf("%v: half a configuration must be a startup error, got %v", partial, err)
		}
	}
	if _, err := buildCashera(casheraTestSettings(map[string]string{"APP_CASHERA_API_KEY": "pk", "APP_CASHERA_API_SECRET": "sk", "APP_CASHERA_PAYMENT_METHOD": "paypal"}, "")); err == nil {
		t.Fatal("an unknown payment method must be reported")
	}
	built, err := buildCashera(casheraTestSettings(map[string]string{"APP_CASHERA_API_KEY": "pk", "APP_CASHERA_API_SECRET": "sk"}, ""))
	if err != nil {
		t.Fatalf("configured without a public URL: %v", err)
	}
	if provider := built.(*Cashera); provider.callbackURL != "" || provider.baseURL != casheraDefaultBaseURL {
		t.Fatalf("without APP_PUBLIC_URL the cabinet's callback address is used: %+v", provider)
	}
	provider := casheraTestProvider(t, nil, nil)
	if provider.callbackURL != "https://omniflow.example/v1/payments/webhooks/cashera" || !provider.SupportsCurrency("RUB") || provider.SupportsCurrency("USD") {
		t.Fatalf("unexpected configuration %+v", provider)
	}
}

func TestCasheraCreateRequestShape(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/integration/transactions" || request.Header.Get("X-Api-Key") != "pk_test" {
			t.Errorf("unexpected %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("X-Secret") != "" {
			t.Error("the secret authenticates webhooks only and must never be sent")
		}
		var body map[string]any
		decoder := json.NewDecoder(request.Body)
		decoder.UseNumber()
		_ = decoder.Decode(&body)
		want := map[string]any{
			"amount": json.Number("49900"), "currency": "RUB", "external_id": casheraTestOrder, "payment_method": "sbp",
			"callback_url": "https://omniflow.example/v1/payments/webhooks/cashera", "success_url": "https://omniflow.example/back",
			"fail_url": "https://omniflow.example/back",
		}
		for key, value := range want {
			if body[key] != value {
				t.Errorf("%s: got %v, want %v", key, body[key], value)
			}
		}
		if description, _ := body["description"].(string); len([]rune(description)) != casheraDescriptionLimit {
			t.Errorf("description must be cut to %d characters, got %d", casheraDescriptionLimit, len([]rune(description)))
		}
		writer.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(writer, `{"uuid":"`+casheraTestUUID+`","status":"pending","payment_method":"sbp","payment_url":"pay.cashera.cash/9b1f2c4e","expires_at":"2026-06-02T18:20:00+00:00"}`)
	}))
	defer server.Close()
	provider := casheraTestProvider(t, server, map[string]string{"APP_CASHERA_PAYMENT_METHOD": "SBP"})
	intent, err := provider.Create(context.Background(), CreateRequest{
		OrderID: casheraTestOrder, Amount: commerce.Money{Amount: 49900, Currency: "RUB"},
		Description: strings.Repeat("д", 300), ReturnURL: "https://omniflow.example/back",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if intent.ProviderReference != casheraTestUUID || intent.Status != "pending" || intent.CheckoutURL != "https://pay.cashera.cash/9b1f2c4e" ||
		intent.ExpiresAt == nil || intent.ExpiresAt.Format("15:04") != "18:20" {
		t.Fatalf("unexpected intent %+v", intent)
	}
}

func TestCasheraPollMapsStatuses(t *testing.T) {
	t.Parallel()
	cases := map[string]string{"pending": "pending", "paid": "succeeded", "failed": "failed", "expired": "expired", "refunded": "refunded", "chargeback": "chargeback", "brand_new": "pending"}
	for remote, want := range cases {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if request.Method != http.MethodGet || request.URL.Path != "/integration/transactions/"+casheraTestUUID {
				t.Errorf("unexpected poll %s %s", request.Method, request.URL.Path)
			}
			_, _ = io.WriteString(writer, `{"uuid":"`+casheraTestUUID+`","status":"`+remote+`","amount":49900,"currency":"RUB"}`)
		}))
		intent, err := casheraTestProvider(t, server, nil).Poll(context.Background(), casheraTestUUID)
		server.Close()
		if err != nil {
			t.Fatalf("%s: %v", remote, err)
		}
		if intent.Status != want || intent.Amount != (commerce.Money{Amount: 49900, Currency: "RUB"}) {
			t.Fatalf("%s: got %+v, want %s", remote, intent, want)
		}
	}
}

func TestCasheraWebhookAuthentication(t *testing.T) {
	t.Parallel()
	provider := casheraTestProvider(t, nil, nil)
	body := casheraTestEvent("paid", 49900)
	event, err := provider.VerifyWebhook(casheraTestHeaders(), body)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if event.Status != "succeeded" || event.ProviderReference != casheraTestUUID || event.MerchantReference != casheraTestOrder ||
		event.Amount != (commerce.Money{Amount: 49900, Currency: "RUB"}) {
		t.Fatalf("unexpected event %+v", event)
	}
	repeat, _ := provider.VerifyWebhook(casheraTestHeaders(), body)
	if repeat.ID != event.ID {
		t.Fatalf("a redelivery must keep its ID: %q vs %q", repeat.ID, event.ID)
	}
	pending, _ := provider.VerifyWebhook(casheraTestHeaders(), casheraTestEvent("pending", 49900))
	if pending.ID == event.ID {
		t.Fatal("a different status is a different event")
	}
	// The amount is carried as sent: a tampered amount reaches settlement as
	// the mismatch it is, rather than as the expected sum.
	tampered, err := provider.VerifyWebhook(casheraTestHeaders(), casheraTestEvent("paid", 100))
	if err != nil || tampered.Amount.Amount != 100 {
		t.Fatalf("the amount must be reported as sent: %+v %v", tampered, err)
	}
	for name, headers := range map[string]http.Header{
		"wrong secret": {"X-Api-Key": {"pk_test"}, "X-Secret": {"sk_guess"}},
		"wrong key":    {"X-Api-Key": {"pk_other"}, "X-Secret": {"sk_test"}},
		"no headers":   {},
	} {
		if _, err := provider.VerifyWebhook(headers, body); !errors.Is(err, ErrInvalidSignature) {
			t.Fatalf("%s: expected an authentication rejection, got %v", name, err)
		}
	}
	if _, err := provider.VerifyWebhook(casheraTestHeaders(), casheraTestEvent("paid", nil)); err == nil {
		t.Fatal("paid without an amount must be refused so it is retried")
	}
	if _, err := provider.VerifyWebhook(casheraTestHeaders(), casheraTestEvent("paid", 499.5)); err == nil {
		t.Fatal("a fractional amount is not minor units and must be rejected")
	}
	unknown, err := provider.VerifyWebhook(casheraTestHeaders(), casheraTestEvent("mystery", 49900))
	if err != nil || unknown.Status != "pending" {
		t.Fatalf("an unknown status must not settle: %+v %v", unknown, err)
	}
	for _, other := range []string{
		`{"event":"webhook.test","test":{"sent_at":"2026-06-02T18:00:00+00:00"}}`,
		`{"event":"payout.status_updated","payout":{"uuid":"x","status":"completed"}}`,
	} {
		event, err := provider.VerifyWebhook(casheraTestHeaders(), []byte(other))
		if err != nil || event.Status == "succeeded" || event.ID == "" {
			t.Fatalf("%s must be acknowledged without effect: %+v %v", other, event, err)
		}
	}
}

func TestCasheraErrorsAndUnsupported(t *testing.T) {
	t.Parallel()
	for status, refused := range map[int]bool{400: true, 401: true, 422: true, 500: false, 503: false, 429: false} {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(status)
			_, _ = io.WriteString(writer, `{"message":"nope"}`)
		}))
		_, err := casheraTestProvider(t, server, nil).Create(context.Background(), CreateRequest{OrderID: casheraTestOrder, Amount: commerce.Money{Amount: 100, Currency: "RUB"}})
		server.Close()
		if Refused(err) != refused {
			t.Fatalf("HTTP %d: Refused=%v, want %v", status, Refused(err), refused)
		}
	}
	if _, err := casheraTestProvider(t, nil, nil).Refund(context.Background(), RefundRequest{}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("refunds are not ported, got %v", err)
	}
}
