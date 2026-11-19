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
	"strings"
	"testing"

	"github.com/omniflow/omniflow/internal/commerce"
)

const lavaTestOrder = "5d2c1e0a-8f3b-4c55-9a77-1b2c3d4e5f60"

func lavaSettings(values map[string]string, publicURL string) Settings {
	return Settings{Env: func(key string) string { return values[key] }, PublicURL: publicURL}
}

func lavaAt(t *testing.T, handler http.HandlerFunc) *Lava {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	provider, err := buildLava(lavaSettings(map[string]string{
		"APP_LAVA_SHOP_ID": "shop-xyz", "APP_LAVA_SECRET_KEY": "outgoing-secret",
		"APP_LAVA_WEBHOOK_SECRET": "webhook-secret", "APP_LAVA_BASE_URL": server.URL,
	}, "https://shop.example"))
	if err != nil {
		t.Fatal(err)
	}
	return provider.(*Lava)
}

func lavaSignHex(key string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestLavaBuilder(t *testing.T) {
	complete := map[string]string{"APP_LAVA_SHOP_ID": "s", "APP_LAVA_SECRET_KEY": "k", "APP_LAVA_WEBHOOK_SECRET": "w"}
	if _, err := buildLava(lavaSettings(nil, "https://shop.example")); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("expected not configured, got %v", err)
	}
	if _, err := buildLava(lavaSettings(map[string]string{"APP_LAVA_SHOP_ID": "s", "APP_LAVA_SECRET_KEY": "k"}, "https://shop.example")); err == nil || errors.Is(err, ErrNotConfigured) {
		t.Fatalf("a missing webhook secret must fail loudly, got %v", err)
	}
	if _, err := buildLava(lavaSettings(complete, "")); err == nil || errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Lava without a public URL cannot receive notifications, got %v", err)
	}
	provider, err := buildLava(lavaSettings(complete, "https://shop.example/"))
	if err != nil {
		t.Fatal(err)
	}
	lava := provider.(*Lava)
	if lava.hookURL != "https://shop.example/v1/payments/webhooks/lava" || lava.lifetime != 60 || lava.baseURL != "https://api.lava.ru" {
		t.Fatalf("unexpected defaults %#v", lava)
	}
	capabilities := provider.Capabilities()
	if capabilities.Refunds || capabilities.Recurring || !capabilities.Webhooks || !capabilities.Polling {
		t.Fatalf("unexpected capabilities %#v", capabilities)
	}
	if !SupportsCurrency(provider, "RUB") || SupportsCurrency(provider, "USD") {
		t.Fatal("Lava settles RUB only")
	}
	complete["APP_LAVA_PAYMENT_LIFETIME_MINUTES"] = "99999"
	provider, _ = buildLava(lavaSettings(complete, "https://shop.example"))
	if provider.(*Lava).lifetime != 7200 {
		t.Fatal("the lifetime is clamped to Lava's 7200-minute maximum")
	}
}

// Ported from Bedolaga's contract tests: the signature travels in the
// Signature header and covers the exact bytes sent.
func TestLavaCreateSignsTheExactBodyInAHeader(t *testing.T) {
	var raw []byte
	var signature string
	provider := lavaAt(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/business/invoice/create" {
			t.Errorf("unexpected path %s", request.URL.Path)
		}
		raw, _ = io.ReadAll(request.Body)
		signature = request.Header.Get("Signature")
		_, _ = io.WriteString(writer, `{"status":"success","data":{"id":"7ea82675-4ded-4133-95a7-a6efbaf165cc","url":"https://pay.lava.ru/invoice/7ea82675","expire":"2026-11-19T11:00:00+03:00"}}`)
	})
	intent, err := provider.Create(t.Context(), CreateRequest{
		OrderID: lavaTestOrder, Amount: commerce.Money{Amount: 1550, Currency: "RUB"}, Description: "Plan",
		ReturnURL: "https://shop.example/account/orders/1?method=lava#x",
	})
	if err != nil {
		t.Fatal(err)
	}
	if signature != lavaSignHex("outgoing-secret", raw) {
		t.Fatal("the Signature header must be the HMAC of the bytes sent")
	}
	var sent map[string]any
	if err := json.Unmarshal(raw, &sent); err != nil {
		t.Fatal(err)
	}
	if _, embedded := sent["signature"]; embedded {
		t.Fatal("the signature must not be embedded in the body")
	}
	if sent["sum"] != 15.5 || sent["orderId"] != lavaTestOrder || sent["shopId"] != "shop-xyz" ||
		sent["hookUrl"] != "https://shop.example/v1/payments/webhooks/lava" || sent["successUrl"] != "https://shop.example/account/orders/1" ||
		sent["failUrl"] != "https://shop.example/account/orders/1" || sent["expire"] != float64(60) || sent["comment"] != "Plan" {
		t.Fatalf("unexpected body %s", raw)
	}
	if strings.Index(string(raw), `"sum"`) > strings.Index(string(raw), `"hookUrl"`) {
		t.Fatal("the body keeps Lava's field order")
	}
	if intent.ProviderReference != "7ea82675-4ded-4133-95a7-a6efbaf165cc" || intent.CheckoutURL != "https://pay.lava.ru/invoice/7ea82675" || intent.Status != "pending" || intent.ExpiresAt == nil {
		t.Fatalf("unexpected intent %#v", intent)
	}
	if _, err := provider.Create(t.Context(), CreateRequest{OrderID: "o", Amount: commerce.Money{Amount: 1, Currency: "EUR"}}); !errors.Is(err, commerce.ErrCurrencyMismatch) {
		t.Fatalf("expected currency rejection, got %v", err)
	}
}

func TestLavaCreateRejectsErrorsAndClassifiesThem(t *testing.T) {
	declined := lavaAt(t, func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, `{"status":"error","error":"Shop not found"}`)
	})
	if _, err := declined.Create(t.Context(), CreateRequest{OrderID: "o", Amount: commerce.Money{Amount: 100, Currency: "RUB"}}); !errors.Is(err, ErrProviderResponse) {
		t.Fatalf("an error envelope must fail, got %v", err)
	}
	for status, refused := range map[int]bool{401: true, 422: true, 500: false, 502: false} {
		provider := lavaAt(t, func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(status)
			_, _ = io.WriteString(writer, `{"error":"Invalid signature"}`)
		})
		_, err := provider.Create(t.Context(), CreateRequest{OrderID: "o", Amount: commerce.Money{Amount: 100, Currency: "RUB"}})
		if Refused(err) != refused {
			t.Fatalf("HTTP %d: refused=%v want %v", status, Refused(err), refused)
		}
	}
}

func TestLavaPollMapsStatuses(t *testing.T) {
	for raw, want := range map[string]string{"success": "succeeded", "created": "pending", "expired": "expired", "cancel": "cancelled", "error": "failed", "brand-new": "pending"} {
		provider := lavaAt(t, func(writer http.ResponseWriter, request *http.Request) {
			var body map[string]string
			_ = json.NewDecoder(request.Body).Decode(&body)
			if request.URL.Path != "/business/invoice/status" || body["invoiceId"] != "inv-1" || body["shopId"] != "shop-xyz" {
				t.Errorf("unexpected poll %s %#v", request.URL.Path, body)
			}
			_, _ = io.WriteString(writer, `{"data":{"id":"inv-1","order_id":"`+lavaTestOrder+`","status":"`+raw+`","amount":100.5},"status":200}`)
		})
		intent, err := provider.Poll(t.Context(), "inv-1")
		if err != nil {
			t.Fatal(err)
		}
		if intent.Status != want || intent.Amount != (commerce.Money{Amount: 10050, Currency: "RUB"}) {
			t.Fatalf("%s: unexpected intent %#v", raw, intent)
		}
	}
}

func TestLavaWebhookSignatures(t *testing.T) {
	provider := lavaAt(t, func(http.ResponseWriter, *http.Request) {})

	// Bedolaga vectors: a raw-body signature and a sorted-keys one.
	raw := []byte(`{"order_id":"x","status":"success"}`)
	if _, err := provider.VerifyWebhook(http.Header{"Authorization": {"adf22d497995c153dd256df7d6309d4424c06c5e71c21f3f560ca50817e96511"}}, raw); err != nil {
		t.Fatalf("raw-body signature: %v", err)
	}
	unsorted := []byte(`{"status":"success","order_id":"x"}`)
	if _, err := provider.VerifyWebhook(http.Header{"Authorization": {lavaSignHex("webhook-secret", raw)}}, unsorted); err != nil {
		t.Fatalf("canonical signature: %v", err)
	}

	// The canonical form drops every signature field, folds 100.0 to 100, and
	// escapes non-ASCII the way the PHP-compatible serialiser does; the digest
	// below was computed by the reference implementation.
	body := []byte(`{"status":"success","order_id":"` + lavaTestOrder + `","invoice_id":"7ea82675-4ded-4133-95a7-a6efbaf165cc","amount":100.0,"credited":96.5,"pay_service":"card","custom_fields":"Пополнение <&>","signature":"ignored","payer_details":{"masked":"2200 **** 0001","signature":"x"}}`)
	event, err := provider.VerifyWebhook(http.Header{"Authorization": {"b239c38560bbf45446e52fc175528376deeaf0fedf5d0708074227053d1c2388"}}, body)
	if err != nil {
		t.Fatalf("canonical vector: %v", err)
	}
	if event.ID != "success:7ea82675-4ded-4133-95a7-a6efbaf165cc" || event.Status != "succeeded" ||
		event.ProviderReference != "7ea82675-4ded-4133-95a7-a6efbaf165cc" || event.MerchantReference != lavaTestOrder ||
		event.Amount != (commerce.Money{Amount: 10000, Currency: "RUB"}) {
		t.Fatalf("unexpected event %#v", event)
	}
	again, _ := provider.VerifyWebhook(http.Header{"Authorization": {"b239c38560bbf45446e52fc175528376deeaf0fedf5d0708074227053d1c2388"}}, body)
	if again.ID != event.ID {
		t.Fatal("a redelivery must keep its event ID")
	}

	for name, signature := range map[string]string{"empty": "", "forged": strings.Repeat("deadbeef", 8), "wrong key": lavaSignHex("outgoing-secret", raw)} {
		if _, err := provider.VerifyWebhook(http.Header{"Authorization": {signature}}, raw); !errors.Is(err, ErrInvalidSignature) {
			t.Fatalf("%s: expected rejection, got %v", name, err)
		}
	}
	tampered := []byte(strings.Replace(string(body), `"amount":100.0`, `"amount":1.0`, 1))
	if _, err := provider.VerifyWebhook(http.Header{"Authorization": {"b239c38560bbf45446e52fc175528376deeaf0fedf5d0708074227053d1c2388"}}, tampered); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("a tampered amount must fail verification, got %v", err)
	}
	garbage := []byte("\x00\x01not json")
	if _, err := provider.VerifyWebhook(http.Header{"Authorization": {lavaSignHex("webhook-secret", garbage)}}, garbage); !errors.Is(err, ErrProviderResponse) {
		t.Fatalf("a signed but unreadable body is not an event, got %v", err)
	}

	unknown := []byte(`{"invoice_id":"inv-2","order_id":"` + lavaTestOrder + `","status":"on_hold","amount":"10.00"}`)
	event, err = provider.VerifyWebhook(http.Header{"Authorization": {lavaSignHex("webhook-secret", unknown)}}, unknown)
	if err != nil || event.Status != "pending" || event.ID != "on_hold:inv-2" {
		t.Fatalf("an unknown status must not settle: %#v %v", event, err)
	}
}

func TestLavaRefundIsUnsupportedAndProbeReadsTariffs(t *testing.T) {
	var path string
	provider := lavaAt(t, func(writer http.ResponseWriter, request *http.Request) {
		path = request.URL.Path
		_, _ = io.WriteString(writer, `{"data":[{"service":"card"}],"status":200}`)
	})
	if _, err := provider.Refund(t.Context(), RefundRequest{IdempotencyKey: "refund-key"}); !errors.Is(err, ErrUnsupported) || !Refused(err) {
		t.Fatalf("expected unsupported refund, got %v", err)
	}
	if err := provider.Probe(t.Context()); err != nil || path != "/business/invoice/get-available-tariffs" {
		t.Fatalf("probe: %v at %s", err, path)
	}
}
