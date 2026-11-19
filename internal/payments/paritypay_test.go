package payments

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/omniflow/omniflow/internal/commerce"
)

const parityPayTestOrder = "3f2a1b0c-9d8e-4f7a-8b6c-5d4e3f2a1b0c"

// parityPayBedolagaPayload is the notification from Bedolaga's webhook tests,
// serialised as Python's json.dumps writes it. Its signature under
// "callback-secret-key-2" was computed by the reference implementation.
const parityPayBedolagaPayload = `{"id": "9beea835-0937-4b5c-8f5a-c3a0d0e60346", "order_id": "pp123_abcdef12", "shop_id": "874dfb1e-dbdb-4747-a1c0-005969725b74", "amount": "1250.00", "credited": 1209.01, "comment": null, "service": "sbp", "custom_fields": null, "expires": "2026-08-24 13:40:00", "created": "2026-08-24 12:40:00", "status": "PAID"}`

const parityPayBedolagaSignature = "de0ee200d020746f77d7a1cba4409c67bd170c6e06c64601dae44d38874a9ec7"

func parityPaySettings(values map[string]string, publicURL string) Settings {
	return Settings{Env: func(key string) string { return values[key] }, PublicURL: publicURL}
}

func parityPayAt(t *testing.T, handler http.HandlerFunc) *ParityPay {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	provider, err := buildParityPay(parityPaySettings(map[string]string{
		"APP_PARITYPAY_SHOP_ID": "874dfb1e-dbdb-4747-a1c0-005969725b74", "APP_PARITYPAY_SECRET_KEY": "secret-key-1",
		"APP_PARITYPAY_CALLBACK_SECRET": "callback-secret-key-2", "APP_PARITYPAY_BASE_URL": server.URL,
	}, "https://shop.example"))
	if err != nil {
		t.Fatal(err)
	}
	return provider.(*ParityPay)
}

// parityPaySign signs a body the way ParityPay does: values in key order,
// concatenated, with numbers as sent.
func parityPaySign(t *testing.T, secret string, body []byte) string {
	t.Helper()
	fields, ok := parityPayFields(body)
	if !ok {
		t.Fatalf("unreadable body %s", body)
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	var payload strings.Builder
	sort.Strings(keys)
	for _, key := range keys {
		payload.WriteString(fields[key].text)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(payload.String()))
	return hex.EncodeToString(mac.Sum(nil))
}

func TestParityPayBuilder(t *testing.T) {
	complete := map[string]string{"APP_PARITYPAY_SHOP_ID": "s", "APP_PARITYPAY_SECRET_KEY": "k", "APP_PARITYPAY_CALLBACK_SECRET": "c"}
	if _, err := buildParityPay(parityPaySettings(nil, "")); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("expected not configured, got %v", err)
	}
	if _, err := buildParityPay(parityPaySettings(map[string]string{"APP_PARITYPAY_SHOP_ID": "s", "APP_PARITYPAY_SECRET_KEY": "k"}, "")); err == nil || errors.Is(err, ErrNotConfigured) {
		t.Fatalf("a missing callback secret must fail loudly, got %v", err)
	}
	complete["APP_PARITYPAY_SERVICE"] = "crypto"
	if _, err := buildParityPay(parityPaySettings(complete, "")); err == nil {
		t.Fatal("an unknown service must be rejected")
	}
	complete["APP_PARITYPAY_SERVICE"] = "SBP"
	provider, err := buildParityPay(parityPaySettings(complete, ""))
	if err != nil {
		t.Fatal(err)
	}
	parity := provider.(*ParityPay)
	if parity.service != "sbp" || parity.lifetime != 60 || parity.baseURL != "https://api.paritypay.net" || parity.callbackURL != "" {
		t.Fatalf("unexpected defaults %#v", parity)
	}
	if capabilities := provider.Capabilities(); capabilities.Refunds || capabilities.Recurring || !capabilities.Webhooks || !capabilities.Polling {
		t.Fatalf("unexpected capabilities %#v", capabilities)
	}
	if !SupportsCurrency(provider, "RUB") || SupportsCurrency(provider, "USD") {
		t.Fatal("ParityPay settles RUB only")
	}
}

func TestParityPayCreateRequestShape(t *testing.T) {
	var body map[string]any
	provider := parityPayAt(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v2/invoice/create" ||
			request.Header.Get("X-ShopId") != "874dfb1e-dbdb-4747-a1c0-005969725b74" || request.Header.Get("X-SecretKey") != "secret-key-1" {
			t.Errorf("unexpected request %s %s", request.Method, request.URL.Path)
		}
		_ = json.NewDecoder(request.Body).Decode(&body)
		_, _ = io.WriteString(writer, `{"id":"9beea835-0937-4b5c-8f5a-c3a0d0e60346","order_id":"`+parityPayTestOrder+`","amount":1500.5,"status":"NEW","link":"https://pay.paritypay.net/9beea835"}`)
	})
	intent, err := provider.Create(t.Context(), CreateRequest{
		OrderID: parityPayTestOrder, Amount: commerce.Money{Amount: 150050, Currency: "RUB"}, Description: "Plan", ReturnURL: "https://shop.example/r",
	})
	if err != nil {
		t.Fatal(err)
	}
	if body["order_id"] != parityPayTestOrder || body["amount"] != 1500.5 || body["expire"] != float64(60) || body["comment"] != "Plan" ||
		body["success_url"] != "https://shop.example/r" || body["callback_url"] != "https://shop.example/v1/payments/webhooks/paritypay" {
		t.Fatalf("unexpected body %#v", body)
	}
	if _, sent := body["subscription"]; sent {
		t.Fatal("the subscription block is never sent")
	}
	if _, sent := body["service"]; sent {
		t.Fatal("no service is forced unless configured")
	}
	if intent.ProviderReference != "9beea835-0937-4b5c-8f5a-c3a0d0e60346" || intent.CheckoutURL != "https://pay.paritypay.net/9beea835" ||
		intent.Status != "pending" || intent.Amount != (commerce.Money{Amount: 150050, Currency: "RUB"}) || intent.ExpiresAt == nil {
		t.Fatalf("unexpected intent %#v", intent)
	}
	if _, err := provider.Create(t.Context(), CreateRequest{OrderID: "o", Amount: commerce.Money{Amount: 1, Currency: "USD"}}); !errors.Is(err, commerce.ErrCurrencyMismatch) {
		t.Fatalf("expected currency rejection, got %v", err)
	}
}

func TestParityPayCreateReusesAnInvoiceAfterADuplicateOrderID(t *testing.T) {
	var lookups []string
	provider := parityPayAt(t, func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v2/invoice/create":
			writer.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = io.WriteString(writer, `{"error":"Order id is not unique"}`)
		case "/v2/invoice/status":
			lookups = append(lookups, request.URL.RawQuery)
			_, _ = io.WriteString(writer, `{"id":"inv-1","order_id":"`+parityPayTestOrder+`","amount":"10.00","status":"NEW","link":"https://pay.paritypay.net/inv-1"}`)
		}
	})
	intent, err := provider.Create(t.Context(), CreateRequest{OrderID: parityPayTestOrder, Amount: commerce.Money{Amount: 1000, Currency: "RUB"}})
	if err != nil {
		t.Fatal(err)
	}
	if intent.ProviderReference != "inv-1" || intent.CheckoutURL != "https://pay.paritypay.net/inv-1" || len(lookups) != 1 || lookups[0] != "order_id="+parityPayTestOrder {
		t.Fatalf("expected the existing invoice to be reused: %#v %v", intent, lookups)
	}

	refused := parityPayAt(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v2/invoice/status" {
			writer.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(writer, `{"error":"Not found"}`)
			return
		}
		writer.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = io.WriteString(writer, `{"error":"Amount too small"}`)
	})
	if _, err := refused.Create(t.Context(), CreateRequest{OrderID: parityPayTestOrder, Amount: commerce.Money{Amount: 1, Currency: "RUB"}}); !Refused(err) {
		t.Fatalf("a 422 with no existing invoice is a refusal, got %v", err)
	}
	for status, want := range map[int]bool{400: true, 500: false, 503: false} {
		provider := parityPayAt(t, func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(status) })
		_, err := provider.Create(t.Context(), CreateRequest{OrderID: "o", Amount: commerce.Money{Amount: 100, Currency: "RUB"}})
		if Refused(err) != want {
			t.Fatalf("HTTP %d: refused=%v want %v", status, Refused(err), want)
		}
	}
}

func TestParityPayPollMapsStatuses(t *testing.T) {
	for raw, want := range map[string]string{"NEW": "pending", "PAID": "succeeded", "EXPIRED": "expired", "ERROR": "failed", "REFUNDED": "refunded", "HOLD": "pending"} {
		provider := parityPayAt(t, func(writer http.ResponseWriter, request *http.Request) {
			if request.Method != http.MethodGet || request.URL.Query().Get("id") != "inv-1" {
				t.Errorf("unexpected poll %s %s", request.Method, request.URL)
			}
			_, _ = io.WriteString(writer, `{"id":"inv-1","order_id":"`+parityPayTestOrder+`","amount":1250,"status":"`+raw+`"}`)
		})
		intent, err := provider.Poll(t.Context(), "inv-1")
		if err != nil {
			t.Fatal(err)
		}
		if intent.Status != want || intent.Amount.Amount != 125000 {
			t.Fatalf("%s: unexpected intent %#v", raw, intent)
		}
	}
	missing := parityPayAt(t, func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusNotFound) })
	if _, err := missing.Poll(t.Context(), "inv-1"); err == nil {
		t.Fatal("an invoice the provider does not know is an error, not a status")
	}
}

func TestParityPayWebhookVerifiesTheReferenceVector(t *testing.T) {
	provider := parityPayAt(t, func(http.ResponseWriter, *http.Request) {})
	body := []byte(parityPayBedolagaPayload)
	headers := http.Header{"X-Signature": {parityPayBedolagaSignature}}
	event, err := provider.VerifyWebhook(headers, body)
	if err != nil {
		t.Fatal(err)
	}
	if event.ID != "paid:9beea835-0937-4b5c-8f5a-c3a0d0e60346" || event.Status != "succeeded" ||
		event.ProviderReference != "9beea835-0937-4b5c-8f5a-c3a0d0e60346" || event.MerchantReference != "pp123_abcdef12" ||
		event.Amount != (commerce.Money{Amount: 125000, Currency: "RUB"}) {
		t.Fatalf("unexpected event %#v", event)
	}
	again, _ := provider.VerifyWebhook(headers, body)
	if again.ID != event.ID {
		t.Fatal("a redelivery must keep its event ID")
	}
	upper := http.Header{"X-Signature": {strings.ToUpper(parityPayBedolagaSignature)}}
	if _, err := provider.VerifyWebhook(upper, body); err != nil {
		t.Fatalf("the signature is compared case-insensitively: %v", err)
	}

	for name, headers := range map[string]http.Header{
		"missing": {},
		"forged":  {"X-Signature": {strings.Repeat("0", 64)}},
	} {
		if _, err := provider.VerifyWebhook(headers, body); !errors.Is(err, ErrInvalidSignature) {
			t.Fatalf("%s: expected rejection, got %v", name, err)
		}
	}
	tampered := []byte(strings.Replace(parityPayBedolagaPayload, `"1250.00"`, `"1.00"`, 1))
	if _, err := provider.VerifyWebhook(headers, tampered); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("a tampered amount must fail verification, got %v", err)
	}
	// Python would rewrite 1209.01 when re-encoding; the signature must use
	// the number exactly as sent, so 1200 and 1200.0 sign differently.
	integral := []byte(`{"id":"inv-3","order_id":"o","amount":1200,"status":"NEW"}`)
	if _, err := provider.VerifyWebhook(http.Header{"X-Signature": {parityPaySign(t, "callback-secret-key-2", integral)}}, integral); err != nil {
		t.Fatalf("an integral amount must verify as sent: %v", err)
	}
	if _, err := provider.VerifyWebhook(http.Header{"X-Signature": {parityPaySign(t, "callback-secret-key-2", []byte(`{"id":"inv-3","order_id":"o","amount":1200.0,"status":"NEW"}`))}}, integral); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("a re-encoded number must not verify, got %v", err)
	}
	if _, err := provider.VerifyWebhook(headers, []byte(`{"id": broken`)); !errors.Is(err, ErrProviderResponse) {
		t.Fatalf("a broken body must be rejected, got %v", err)
	}
}

func TestParityPayWebhookIgnoresSubscriptionsAndUnknownStatuses(t *testing.T) {
	provider := parityPayAt(t, func(http.ResponseWriter, *http.Request) {})
	verify := func(body string) (WebhookEvent, error) {
		raw := []byte(body)
		return provider.VerifyWebhook(http.Header{"X-Signature": {parityPaySign(t, "callback-secret-key-2", raw)}}, raw)
	}
	for _, body := range []string{
		`{"id":"sub-1","shop_subscription_id":"s-1","status":"ACTIVE"}`,
		`{"id":"inv-9","order_id":"sub-1_2","subscription_id":"sub-1","amount":"10.00","status":"PAID"}`,
	} {
		event, err := verify(body)
		if err != nil || event.Status != "pending" || event.ProviderReference != "" || !strings.HasPrefix(event.ID, "subscription:") {
			t.Fatalf("%s: subscription traffic must be acknowledged and ignored: %#v %v", body, event, err)
		}
	}
	event, err := verify(`{"id":"inv-4","order_id":"` + parityPayTestOrder + `","amount":"10.00","status":"ON_HOLD"}`)
	if err != nil || event.Status != "pending" {
		t.Fatalf("an unknown status must not settle: %#v %v", event, err)
	}
	if _, err := verify(`{"id":"inv-5","order_id":"` + parityPayTestOrder + `","status":"PAID"}`); err == nil {
		t.Fatal("a PAID notification without an amount must not be accepted")
	}
}

func TestParityPayRefundIsUnsupported(t *testing.T) {
	provider := parityPayAt(t, func(http.ResponseWriter, *http.Request) { t.Error("a refund must not reach ParityPay") })
	if _, err := provider.Refund(t.Context(), RefundRequest{IdempotencyKey: "k"}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("expected unsupported refund, got %v", err)
	}
}
