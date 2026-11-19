package payments

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/commerce"
)

func kassaAITestConfigured(extra map[string]string) map[string]string {
	values := map[string]string{
		"APP_KASSA_AI_SHOP_ID":       "12345",
		"APP_KASSA_AI_API_KEY":       "kassa-key",
		"APP_KASSA_AI_SECRET_WORD_2": "word-two",
		"APP_KASSA_AI_SERVER_IP":     "203.0.113.10",
	}
	for key, value := range extra {
		values[key] = value
	}
	return values
}

func kassaAITestProvider(t *testing.T, extra map[string]string, handler http.HandlerFunc) *KassaAI {
	t.Helper()
	built, err := buildKassaAI(freekassaTestEnv(kassaAITestConfigured(extra)))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	provider := built.(*KassaAI)
	provider.core.clock = func() time.Time { return time.Unix(0, freekassaTestNonce) }
	if handler != nil {
		server := httptest.NewServer(handler)
		t.Cleanup(server.Close)
		provider.core.baseURL = server.URL
	}
	return provider
}

func TestKassaAIBuilderReadsItsEnvironment(t *testing.T) {
	if _, err := buildKassaAI(freekassaTestEnv(map[string]string{})); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("no credentials must leave the adapter unregistered, got %v", err)
	}
	_, err := buildKassaAI(freekassaTestEnv(map[string]string{"APP_KASSA_AI_API_KEY": "kassa-key"}))
	if err == nil || errors.Is(err, ErrNotConfigured) || !strings.Contains(err.Error(), "APP_KASSA_AI_SERVER_IP") {
		t.Fatalf("a partial configuration must name what is missing, got %v", err)
	}
	provider := kassaAITestProvider(t, nil, nil)
	if provider.core.paymentSystemID != 44 || provider.Name() != "kassa_ai" {
		t.Fatalf("SBP (44) is the default method, got %d", provider.core.paymentSystemID)
	}
	if provider.core.notificationURL != "https://shop.example.com/v1/payments/webhooks/kassa_ai" {
		t.Fatalf("unexpected notification URL %q", provider.core.notificationURL)
	}
	for alias, id := range map[string]int{"card": 36, "sberpay": 43, "sbp": 44} {
		if got := kassaAITestProvider(t, map[string]string{"APP_KASSA_AI_PAYMENT_SYSTEM_ID": alias}, nil).core.paymentSystemID; got != id {
			t.Fatalf("%s must select %d, got %d", alias, id, got)
		}
	}
	if _, err := buildKassaAI(freekassaTestEnv(kassaAITestConfigured(map[string]string{"APP_KASSA_AI_PAYMENT_SYSTEM_ID": "-1"}))); err == nil {
		t.Fatal("a negative payment system must be rejected")
	}
	unpublished, err := buildKassaAI(Settings{Env: func(key string) string { return kassaAITestConfigured(nil)[key] }})
	if err != nil || unpublished.(*KassaAI).core.notificationURL != "" {
		t.Fatalf("without a public URL the cabinet notification URL applies: %v", err)
	}
}

func TestKassaAICreateSendsTheMethodAndCallbackURLs(t *testing.T) {
	var body map[string]json.RawMessage
	provider := kassaAITestProvider(t, nil, func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/orders/create" {
			t.Errorf("unexpected request %s %s", request.Method, request.URL.Path)
		}
		raw, _ := io.ReadAll(request.Body)
		_ = json.Unmarshal(raw, &body)
		_, _ = writer.Write([]byte(`{"type":"success","orderId":555,"paymentId":"` + freekassaTestOrder + `","location":"https://pay.example/qr/555"}`))
	})
	intent, err := provider.Create(t.Context(), CreateRequest{
		OrderID: freekassaTestOrder, Amount: commerce.Money{Amount: 150000, Currency: "RUB"},
		ReturnURL: "https://shop.example.com/account/orders/x",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	want := map[string]string{
		"i": `44`, "amount": `1500`,
		"success_url":      `"https://shop.example.com/account/orders/x"`,
		"fail_url":         `"https://shop.example.com/account/orders/x"`,
		"notification_url": `"https://shop.example.com/v1/payments/webhooks/kassa_ai"`,
		// HMAC-SHA256("kassa-key", values sorted by key joined with "|"), Bedolaga's formula.
		"signature": `"c30d5a2cf87eada1d78c5b35dc758228baf3425e6728c6cbf1382827f06f8e39"`,
	}
	for key, value := range want {
		if string(body[key]) != value {
			t.Fatalf("%s: want %s, got %s", key, value, body[key])
		}
	}
	if intent.CheckoutURL != "https://pay.example/qr/555" || intent.ProviderReference != freekassaTestOrder {
		t.Fatalf("unexpected intent %#v", intent)
	}
}

func TestKassaAIWebhookAndPoll(t *testing.T) {
	provider := kassaAITestProvider(t, nil, func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/orders" {
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = writer.Write([]byte(`{"type":"success","orders":[{"paymentId":"` + freekassaTestOrder + `","status":1,"amount":"100","currency":"RUB"}]}`))
	})
	// md5("12345:100:word-two:<order>"), the same vector as FreeKassa.
	body := []byte(url.Values{
		"MERCHANT_ID": {"12345"}, "AMOUNT": {"100"}, "intid": {"5001"},
		"MERCHANT_ORDER_ID": {freekassaTestOrder}, "SIGN": {"65d506d781cecce963d9a08cf7ae9ae9"},
	}.Encode())
	event, err := provider.VerifyWebhook(http.Header{}, body)
	if err != nil || event.ID != "paid:5001" || event.Amount.Amount != 10000 || event.Status != "succeeded" {
		t.Fatalf("unexpected event %#v %v", event, err)
	}
	tampered := []byte(strings.Replace(string(body), "AMOUNT=100", "AMOUNT=10", 1))
	if _, err := provider.VerifyWebhook(http.Header{}, tampered); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("a tampered amount must be rejected, got %v", err)
	}
	if _, _, ack := provider.WebhookAck("duplicate"); string(ack) != "YES" {
		t.Fatalf("KassaAI must be answered with YES, got %q", ack)
	}
	intent, err := provider.Poll(t.Context(), freekassaTestOrder)
	if err != nil || intent.Status != "succeeded" || intent.Amount.Amount != 10000 {
		t.Fatalf("unexpected poll %#v %v", intent, err)
	}
	if err := provider.Probe(t.Context()); err == nil || Refused(err) {
		t.Fatalf("a 5xx probe is an outage, got %v", err)
	}
	if _, err := provider.Refund(t.Context(), RefundRequest{}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("refunds are unsupported, got %v", err)
	}
}
