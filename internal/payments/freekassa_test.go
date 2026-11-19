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

const (
	freekassaTestOrder = "6f1c1f3e-8a7b-4c2d-9e0f-112233445566"
	freekassaTestNonce = int64(1763553600000000000)
)

func freekassaTestEnv(values map[string]string) Settings {
	return Settings{Env: func(key string) string { return values[key] }, PublicURL: "https://shop.example.com"}
}

func freekassaTestConfigured(extra map[string]string) map[string]string {
	values := map[string]string{
		"APP_FREEKASSA_SHOP_ID":       "12345",
		"APP_FREEKASSA_API_KEY":       "api-key",
		"APP_FREEKASSA_SECRET_WORD_1": "word-one",
		"APP_FREEKASSA_SECRET_WORD_2": "word-two",
	}
	for key, value := range extra {
		values[key] = value
	}
	return values
}

func freekassaTestProvider(t *testing.T, extra map[string]string, handler http.HandlerFunc) *FreeKassa {
	t.Helper()
	built, err := buildFreeKassa(freekassaTestEnv(freekassaTestConfigured(extra)))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	provider := built.(*FreeKassa)
	provider.core.clock = func() time.Time { return time.Unix(0, freekassaTestNonce) }
	if handler != nil {
		server := httptest.NewServer(handler)
		t.Cleanup(server.Close)
		provider.core.baseURL = server.URL
	}
	return provider
}

func TestFreeKassaBuilderReadsItsEnvironment(t *testing.T) {
	if _, err := buildFreeKassa(freekassaTestEnv(map[string]string{})); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("no credentials must leave the adapter unregistered, got %v", err)
	}
	_, err := buildFreeKassa(freekassaTestEnv(map[string]string{"APP_FREEKASSA_SHOP_ID": "12345"}))
	if err == nil || errors.Is(err, ErrNotConfigured) || !strings.Contains(err.Error(), "APP_FREEKASSA_SECRET_WORD_2") {
		t.Fatalf("a partial configuration must name what is missing, got %v", err)
	}
	if _, err := buildFreeKassa(freekassaTestEnv(freekassaTestConfigured(map[string]string{"APP_FREEKASSA_SHOP_ID": "shop"}))); err == nil {
		t.Fatal("a non-numeric shop ID must be rejected")
	}
	if _, err := buildFreeKassa(freekassaTestEnv(freekassaTestConfigured(map[string]string{"APP_FREEKASSA_PAYMENT_SYSTEM_ID": "sbp"}))); err == nil {
		t.Fatal("API orders without a server IP must be rejected")
	}
	if _, err := buildFreeKassa(freekassaTestEnv(freekassaTestConfigured(map[string]string{"APP_FREEKASSA_PAYMENT_SYSTEM_ID": "bitcoin", "APP_FREEKASSA_SERVER_IP": "203.0.113.10"}))); err == nil {
		t.Fatal("an unknown payment method alias must be rejected")
	}
	if _, err := buildFreeKassa(freekassaTestEnv(freekassaTestConfigured(map[string]string{"APP_FREEKASSA_CURRENCY": "XYZ"}))); err == nil {
		t.Fatal("an unknown currency must be rejected")
	}
	form := freekassaTestProvider(t, nil, nil)
	if form.useAPI || form.Name() != "freekassa" {
		t.Fatalf("the default is the hosted form: %#v", form)
	}
	if !form.SupportsCurrency("rub") || form.SupportsCurrency("USD") {
		t.Fatal("only the configured currency is supported")
	}
	if caps := form.Capabilities(); caps.Refunds || caps.Recurring || !caps.Webhooks || !caps.Polling {
		t.Fatalf("unexpected capabilities %#v", caps)
	}
	for alias, id := range map[string]int{"sbp": 44, "card": 36, "42": 42} {
		provider := freekassaTestProvider(t, map[string]string{"APP_FREEKASSA_PAYMENT_SYSTEM_ID": alias, "APP_FREEKASSA_SERVER_IP": "203.0.113.10"}, nil)
		if !provider.useAPI || provider.core.paymentSystemID != id {
			t.Fatalf("%s must select payment system %d through the API, got %d", alias, id, provider.core.paymentSystemID)
		}
	}
	forced := freekassaTestProvider(t, map[string]string{"APP_FREEKASSA_USE_API": "true", "APP_FREEKASSA_SERVER_IP": "203.0.113.10"}, nil)
	if !forced.useAPI || forced.core.paymentSystemID != 1 {
		t.Fatalf("a forced API order without a method falls back to payment system 1, got %d", forced.core.paymentSystemID)
	}
}

func TestFreeKassaCreateSignsTheHostedFormLink(t *testing.T) {
	provider := freekassaTestProvider(t, nil, nil)
	intent, err := provider.Create(t.Context(), CreateRequest{OrderID: freekassaTestOrder, Amount: commerce.Money{Amount: 10050, Currency: "RUB"}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	link, err := url.Parse(intent.CheckoutURL)
	if err != nil || link.Host != "pay.fk.money" {
		t.Fatalf("unexpected checkout URL %q", intent.CheckoutURL)
	}
	query := link.Query()
	// md5("12345:100.5:word-one:RUB:<order>"), computed with Bedolaga's formula.
	if query.Get("m") != "12345" || query.Get("oa") != "100.5" || query.Get("currency") != "RUB" ||
		query.Get("o") != freekassaTestOrder || query.Get("s") != "576e61378491e09984eccc96ba577fd9" || query.Get("lang") != "ru" {
		t.Fatalf("unexpected form parameters %v", query)
	}
	if intent.ProviderReference != freekassaTestOrder || intent.Status != "pending" || intent.Amount.Amount != 10050 {
		t.Fatalf("unexpected intent %#v", intent)
	}
	if _, err := provider.Create(t.Context(), CreateRequest{OrderID: freekassaTestOrder, Amount: commerce.Money{Amount: 100, Currency: "USD"}}); !errors.Is(err, commerce.ErrCurrencyMismatch) {
		t.Fatalf("a foreign currency must be refused, got %v", err)
	}
}

func TestFreeKassaCreateSignsTheAPIOrder(t *testing.T) {
	var body map[string]json.RawMessage
	provider := freekassaTestProvider(t, map[string]string{"APP_FREEKASSA_PAYMENT_SYSTEM_ID": "sbp", "APP_FREEKASSA_SERVER_IP": "203.0.113.10"}, func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/orders/create" {
			t.Errorf("unexpected request %s %s", request.Method, request.URL.Path)
		}
		raw, _ := io.ReadAll(request.Body)
		_ = json.Unmarshal(raw, &body)
		_, _ = writer.Write([]byte(`{"type":"success","orderId":987,"orderHash":"abc","location":"https://pay.fk.money/form/987"}`))
	})
	intent, err := provider.Create(t.Context(), CreateRequest{OrderID: freekassaTestOrder, Amount: commerce.Money{Amount: 10050, Currency: "RUB"}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	want := map[string]string{
		"shopId": `12345`, "nonce": `1763553600000000000`, "paymentId": `"` + freekassaTestOrder + `"`,
		"i": `44`, "ip": `"203.0.113.10"`, "amount": `100.5`, "currency": `"RUB"`,
		"email": `"user_` + freekassaTestOrder + `@telegram.org"`,
		// HMAC-SHA256("api-key", values sorted by key joined with "|"), Bedolaga's formula.
		"signature": `"fa7ed31cc4b3142823768bf0a4f3432cd7d22879e9b39c8f90ede7b057fde57f"`,
	}
	for key, value := range want {
		if string(body[key]) != value {
			t.Fatalf("%s: want %s, got %s (body %v)", key, value, body[key], body)
		}
	}
	if len(body) != len(want) {
		t.Fatalf("unexpected extra fields in %v", body)
	}
	if intent.CheckoutURL != "https://pay.fk.money/form/987" || intent.ProviderReference != freekassaTestOrder || intent.Status != "pending" {
		t.Fatalf("unexpected intent %#v", intent)
	}
}

func TestFreeKassaCreateRejectsAnErrorEnvelope(t *testing.T) {
	provider := freekassaTestProvider(t, map[string]string{"APP_FREEKASSA_USE_API": "true", "APP_FREEKASSA_SERVER_IP": "203.0.113.10"}, func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"type":"error","message":"Wrong signature"}`))
	})
	if _, err := provider.Create(t.Context(), CreateRequest{OrderID: freekassaTestOrder, Amount: commerce.Money{Amount: 10000, Currency: "RUB"}}); !errors.Is(err, ErrProviderResponse) {
		t.Fatalf("an error envelope must fail, got %v", err)
	}
}

func TestFreeKassaPollMapsTheOrderStatus(t *testing.T) {
	var response string
	provider := freekassaTestProvider(t, nil, func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(request.Body).Decode(&body)
		if request.URL.Path != "/orders" || body["paymentId"] != freekassaTestOrder || body["signature"] == "" {
			t.Errorf("unexpected poll %s %v", request.URL.Path, body)
		}
		_, _ = writer.Write([]byte(response))
	})
	response = `{"type":"success","pages":1,"orders":[{"merchant_order_id":"` + freekassaTestOrder + `","fk_order_id":987,"amount":100.5,"currency":"RUB","status":1}]}`
	intent, err := provider.Poll(t.Context(), freekassaTestOrder)
	if err != nil || intent.Status != "succeeded" || intent.Amount != (commerce.Money{Amount: 10050, Currency: "RUB"}) {
		t.Fatalf("a paid order must succeed with its amount: %#v %v", intent, err)
	}
	response = `{"type":"success","orders":[{"merchant_order_id":"` + freekassaTestOrder + `","amount":"100.50","currency":"RUB","status":"0"}]}`
	if intent, err := provider.Poll(t.Context(), freekassaTestOrder); err != nil || intent.Status != "pending" {
		t.Fatalf("an unpaid order is pending: %#v %v", intent, err)
	}
	response = `{"type":"success","orders":[{"merchant_order_id":"other","status":1,"amount":5}]}`
	if intent, err := provider.Poll(t.Context(), freekassaTestOrder); err != nil || intent.Status != "pending" {
		t.Fatalf("an order the API does not list is pending: %#v %v", intent, err)
	}
	response = `{"type":"success","orders":[{"merchant_order_id":"` + freekassaTestOrder + `","status":1}]}`
	if _, err := provider.Poll(t.Context(), freekassaTestOrder); !errors.Is(err, ErrProviderResponse) {
		t.Fatalf("a paid order without an amount cannot be settled, got %v", err)
	}
}

func freekassaTestNotification(amount, sign string) []byte {
	return []byte(url.Values{
		"MERCHANT_ID": {"12345"}, "AMOUNT": {amount}, "intid": {"77001"},
		"MERCHANT_ORDER_ID": {freekassaTestOrder}, "P_EMAIL": {"payer@example.com"},
		"CUR_ID": {"44"}, "SIGN": {sign},
	}.Encode())
}

func TestFreeKassaWebhookVerifiesTheSecondSecretWord(t *testing.T) {
	provider := freekassaTestProvider(t, nil, nil)
	// md5("12345:100:word-two:<order>") as Bedolaga signs AMOUNT=100.00 after a float round trip.
	bedolaga := freekassaTestNotification("100.00", "65D506D781CECCE963D9A08CF7AE9AE9")
	event, err := provider.VerifyWebhook(http.Header{}, bedolaga)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if event.ID != "paid:77001" || event.Status != "succeeded" || event.ProviderReference != freekassaTestOrder ||
		event.MerchantReference != freekassaTestOrder || event.Amount != (commerce.Money{Amount: 10000, Currency: "RUB"}) {
		t.Fatalf("unexpected event %#v", event)
	}
	// md5("12345:100.00:word-two:<order>"), the amount exactly as sent.
	raw := freekassaTestNotification("100.00", "9a0e44eaa4cb9ed60e638d1f2f85dc48")
	repeat, err := provider.VerifyWebhook(http.Header{}, raw)
	if err != nil || repeat.ID != event.ID {
		t.Fatalf("a redelivery must keep its event ID: %#v %v", repeat, err)
	}
	for name, body := range map[string][]byte{
		"tampered amount": freekassaTestNotification("1000.00", "9a0e44eaa4cb9ed60e638d1f2f85dc48"),
		"wrong signature": freekassaTestNotification("100.00", "00000000000000000000000000000000"),
		"foreign shop":    []byte(strings.Replace(string(raw), "MERCHANT_ID=12345", "MERCHANT_ID=54321", 1)),
	} {
		if _, err := provider.VerifyWebhook(http.Header{}, body); !errors.Is(err, ErrInvalidSignature) {
			t.Fatalf("%s must be rejected as unsigned, got %v", name, err)
		}
	}
	if _, err := provider.VerifyWebhook(http.Header{}, []byte("MERCHANT_ID=12345&AMOUNT=100")); !errors.Is(err, ErrProviderResponse) {
		t.Fatalf("a notification without its fields must be rejected, got %v", err)
	}
	status, contentType, ack := provider.WebhookAck("paid")
	if status != http.StatusOK || !strings.HasPrefix(contentType, "text/plain") || string(ack) != "YES" {
		t.Fatalf("FreeKassa must be answered with YES, got %d %q %q", status, contentType, ack)
	}
}

func TestFreeKassaErrorsSeparateRefusalsFromOutagesAndRefundsAreUnsupported(t *testing.T) {
	status := http.StatusBadRequest
	provider := freekassaTestProvider(t, nil, func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/balance" && status == http.StatusOK {
			_, _ = writer.Write([]byte(`{"type":"success","balance":[{"currency":"RUB","value":"0.00"}]}`))
			return
		}
		writer.WriteHeader(status)
		_, _ = writer.Write([]byte(`{"type":"error","message":"nope"}`))
	})
	_, err := provider.Poll(t.Context(), freekassaTestOrder)
	if !Refused(err) {
		t.Fatalf("a 4xx is a refusal, got %v", err)
	}
	status = http.StatusBadGateway
	_, err = provider.Poll(t.Context(), freekassaTestOrder)
	if err == nil || Refused(err) {
		t.Fatalf("a 5xx is not a refusal, got %v", err)
	}
	status = http.StatusOK
	if err := provider.Probe(t.Context()); err != nil {
		t.Fatalf("probe: %v", err)
	}
	if _, err := provider.Refund(t.Context(), RefundRequest{}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("refunds are unsupported, got %v", err)
	}
}
