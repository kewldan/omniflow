package payments

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/omniflow/omniflow/internal/commerce"
)

var (
	antilopayTestKeysOnce sync.Once
	antilopayTestMerchant *rsa.PrivateKey
	antilopayTestGateway  *rsa.PrivateKey
)

// antilopayTestKeys returns the merchant's request-signing key and the key
// Antilopay signs notifications with, generated once per test binary.
func antilopayTestKeys(t *testing.T) (*rsa.PrivateKey, *rsa.PrivateKey) {
	t.Helper()
	antilopayTestKeysOnce.Do(func() {
		antilopayTestMerchant, _ = rsa.GenerateKey(rand.Reader, 2048)
		antilopayTestGateway, _ = rsa.GenerateKey(rand.Reader, 2048)
	})
	return antilopayTestMerchant, antilopayTestGateway
}

func antilopayTestEnv(t *testing.T) map[string]string {
	merchant, gateway := antilopayTestKeys(t)
	// The private key is stored as Bedolaga stores it: base64 of a PEM block.
	privatePEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(merchant)})
	publicDER, _ := x509.MarshalPKIXPublicKey(&gateway.PublicKey)
	return map[string]string{
		"APP_ANTILOPAY_SECRET_ID":   "masked-secret-id",
		"APP_ANTILOPAY_PROJECT_ID":  "masked-project",
		"APP_ANTILOPAY_PRIVATE_KEY": base64.StdEncoding.EncodeToString(privatePEM),
		"APP_ANTILOPAY_PUBLIC_KEY":  base64.StdEncoding.EncodeToString(publicDER),
	}
}

func antilopayTestProvider(t *testing.T, handler http.HandlerFunc) *Antilopay {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	built, err := buildAntilopay(Settings{Env: func(key string) string { return antilopayTestEnv(t)[key] }})
	if err != nil {
		t.Fatal(err)
	}
	provider := built.(*Antilopay)
	provider.baseURL = server.URL
	return provider
}

// antilopayTestServer verifies the request signature with the merchant's
// public key, as Antilopay does, before answering.
func antilopayTestServer(t *testing.T, status int, reply string, seen *map[string]any) http.HandlerFunc {
	merchant, _ := antilopayTestKeys(t)
	return func(writer http.ResponseWriter, request *http.Request) {
		raw, _ := io.ReadAll(request.Body)
		signature, _ := base64.StdEncoding.DecodeString(request.Header.Get("X-Apay-Sign"))
		digest := sha256.Sum256(raw)
		if rsa.VerifyPKCS1v15(&merchant.PublicKey, crypto.SHA256, digest[:], signature) != nil {
			t.Error("request signature does not verify against the sent bytes")
		}
		if request.Header.Get("X-Apay-Secret-Id") != "masked-secret-id" || request.Header.Get("X-Apay-Sign-Version") != "1" {
			t.Error("missing Antilopay headers")
		}
		if seen != nil {
			*seen = map[string]any{"path": request.URL.Path}
			var body map[string]any
			_ = json.Unmarshal(raw, &body)
			for key, value := range body {
				(*seen)[key] = value
			}
			(*seen)["raw"] = string(raw)
		}
		writer.WriteHeader(status)
		_, _ = io.WriteString(writer, reply)
	}
}

func antilopayTestCallback(t *testing.T, body []byte) http.Header {
	_, gateway := antilopayTestKeys(t)
	digest := sha256.Sum256(body)
	signature, err := rsa.SignPKCS1v15(nil, gateway, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	headers := make(http.Header)
	headers.Set("X-Apay-Callback", base64.StdEncoding.EncodeToString(signature))
	return headers
}

func TestAntilopayBuilder(t *testing.T) {
	empty := Settings{Env: func(string) string { return "" }}
	if _, err := buildAntilopay(empty); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("expected ErrNotConfigured, got %v", err)
	}
	partial := Settings{Env: func(key string) string {
		if key == "APP_ANTILOPAY_SECRET_ID" {
			return "id"
		}
		return ""
	}}
	if _, err := buildAntilopay(partial); err == nil || errors.Is(err, ErrNotConfigured) {
		t.Fatalf("a partial configuration must be reported, got %v", err)
	}
	badKey := antilopayTestEnv(t)
	badKey["APP_ANTILOPAY_PRIVATE_KEY"] = "bm90IGEga2V5"
	if _, err := buildAntilopay(Settings{Env: func(key string) string { return badKey[key] }}); err == nil {
		t.Fatal("an unreadable key must be reported")
	}
	provider := antilopayTestProvider(t, nil)
	if provider.Name() != "antilopay" || provider.productName != "VPN подписка" || provider.productType != "services" {
		t.Fatalf("unexpected defaults: %#v", provider)
	}
	if !provider.SupportsCurrency("RUB") || provider.SupportsCurrency("EUR") {
		t.Fatal("Antilopay settles RUB only")
	}
	if capabilities := provider.Capabilities(); capabilities.Refunds || capabilities.Recurring || !capabilities.Webhooks || !capabilities.Polling {
		t.Fatalf("unexpected capabilities: %#v", capabilities)
	}
	if NeedsWebhookPolling(provider) {
		t.Fatal("Antilopay notifications are signed")
	}
	if _, ok := Provider(provider).(WebhookAcknowledger); ok {
		t.Fatal("Antilopay needs only HTTP 200, not a custom body")
	}
}

func TestAntilopayCreateSignsTheExactBody(t *testing.T) {
	var seen map[string]any
	provider := antilopayTestProvider(t, antilopayTestServer(t, 200, `{"code":0,"payment_id":"masked-payment","payment_url":"https://pay.example/invoice"}`, &seen))
	intent, err := provider.Create(t.Context(), CreateRequest{
		OrderID: "order-1", Amount: commerce.Money{Amount: 10001, Currency: "RUB"}, Description: "Plan",
		ReturnURL: "https://shop.example/result", Metadata: map[string]string{"customer_email": "user@example.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen["path"] != "/payment/create" || seen["currency"] != "RUB" || seen["order_id"] != "order-1" || seen["project_identificator"] != "masked-project" {
		t.Fatalf("unexpected request: %#v", seen)
	}
	if !strings.Contains(seen["raw"].(string), `"amount":100.01`) {
		t.Fatalf("amount must be roubles with kopek precision: %s", seen["raw"])
	}
	if customer, _ := seen["customer"].(map[string]any); customer["email"] != "user@example.com" {
		t.Fatalf("customer contact missing: %#v", seen["customer"])
	}
	if intent.ProviderReference != "order-1" || intent.Status != "pending" || intent.CheckoutURL != "https://pay.example/invoice" || intent.Metadata["antilopay_payment_id"] != "masked-payment" {
		t.Fatalf("unexpected intent: %#v", intent)
	}
}

func TestAntilopayCreateNeverInventsAContact(t *testing.T) {
	calls := 0
	provider := antilopayTestProvider(t, func(http.ResponseWriter, *http.Request) { calls++ })
	_, err := provider.Create(t.Context(), CreateRequest{OrderID: "order-1", Amount: commerce.Money{Amount: 10000, Currency: "RUB"}})
	if !Refused(err) || calls != 0 {
		t.Fatalf("a missing contact must be refused locally: %v, %d calls", err, calls)
	}
}

func TestAntilopayCreateRejectsIncompleteAnswers(t *testing.T) {
	for _, reply := range []string{`[]`, `{"code":0}`, `{"code":0,"payment_id":"masked","payment_url":"javascript:alert(1)"}`} {
		provider := antilopayTestProvider(t, antilopayTestServer(t, 200, reply, nil))
		_, err := provider.Create(t.Context(), CreateRequest{OrderID: "order-1", Amount: commerce.Money{Amount: 10000, Currency: "RUB"}, Metadata: map[string]string{"customer_phone": "+70000000000"}})
		if err == nil {
			t.Fatalf("%s must be rejected", reply)
		}
	}
}

func TestAntilopayClassifiesRefusals(t *testing.T) {
	cases := []struct {
		status  int
		reply   string
		refused bool
	}{
		{200, `{"code":3,"message":"bad amount"}`, true},
		{400, `{"code":6}`, true},
		{200, `{"code":5,"message":"duplicate order"}`, false},
		{200, `{"code":77}`, false},
		{500, `{"code":3}`, false},
		{502, `<html>`, false},
		{403, `oops`, true},
	}
	for _, testCase := range cases {
		provider := antilopayTestProvider(t, antilopayTestServer(t, testCase.status, testCase.reply, nil))
		_, err := provider.Create(t.Context(), CreateRequest{OrderID: "order-1", Amount: commerce.Money{Amount: 10000, Currency: "RUB"}, Metadata: map[string]string{"customer_email": "a@example.com"}})
		if err == nil || Refused(err) != testCase.refused {
			t.Fatalf("HTTP %d %s: refused=%v err=%v", testCase.status, testCase.reply, Refused(err), err)
		}
	}
}

func TestAntilopayPollUsesTheOrderIDAndOriginalAmount(t *testing.T) {
	for status, expected := range map[string]string{"SUCCESS": "succeeded", "PENDING": "pending", "FAIL": "failed", "CANCEL": "cancelled", "EXPIRED": "expired", "CHARGEBACK": "failed"} {
		var seen map[string]any
		reply := `{"code":0,"order_id":"order-1","payment_id":"masked-payment","status":"` + status + `","amount":97.01,"original_amount":100.01,"currency":"RUB"}`
		provider := antilopayTestProvider(t, antilopayTestServer(t, 200, reply, &seen))
		intent, err := provider.Poll(t.Context(), "order-1")
		if err != nil {
			t.Fatalf("%s: %v", status, err)
		}
		if seen["path"] != "/payment/check" || seen["order_id"] != "order-1" {
			t.Fatalf("check must use the merchant order ID: %#v", seen)
		}
		if intent.Status != expected || intent.Amount != (commerce.Money{Amount: 10001, Currency: "RUB"}) {
			t.Fatalf("%s: unexpected intent %#v", status, intent)
		}
	}
}

func TestAntilopayRecover(t *testing.T) {
	missing := antilopayTestProvider(t, antilopayTestServer(t, 200, `{"code":8,"error":"Payment not found"}`, nil))
	if _, found, err := missing.Recover(t.Context(), "order-1"); err != nil || found {
		t.Fatalf("code 8 means no payment: found=%v err=%v", found, err)
	}
	existing := antilopayTestProvider(t, antilopayTestServer(t, 200, `{"code":0,"order_id":"order-1","payment_id":"p","status":"PENDING"}`, nil))
	intent, found, err := existing.Recover(t.Context(), "order-1")
	if err != nil || !found || intent.ProviderReference != "order-1" {
		t.Fatalf("an existing payment must be recovered: %#v %v %v", intent, found, err)
	}
	broken := antilopayTestProvider(t, antilopayTestServer(t, 503, `{"code":1}`, nil))
	if _, _, err := broken.Recover(t.Context(), "order-1"); err == nil {
		t.Fatal("an outage must not read as 'no payment'")
	}
}

func TestAntilopayWebhook(t *testing.T) {
	provider := antilopayTestProvider(t, nil)
	body := []byte(`{"type":"payment","payment_id":"masked-invoice","order_id":"order-1","status":"SUCCESS","amount":97.01,"original_amount":100.01,"fee":3,"currency":"RUB"}`)
	event, err := provider.VerifyWebhook(antilopayTestCallback(t, body), body)
	if err != nil {
		t.Fatal(err)
	}
	if event.ID != "success:masked-invoice" || event.Status != "succeeded" || event.ProviderReference != "order-1" || event.MerchantReference != "order-1" || event.Amount != (commerce.Money{Amount: 10001, Currency: "RUB"}) {
		t.Fatalf("unexpected event: %#v", event)
	}
	again, _ := provider.VerifyWebhook(antilopayTestCallback(t, body), body)
	if again.ID != event.ID {
		t.Fatal("a redelivery must carry the same ID")
	}
	tampered := []byte(strings.Replace(string(body), "100.01", "1000.01", 1))
	if _, err := provider.VerifyWebhook(antilopayTestCallback(t, body), tampered); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("a tampered body must fail, got %v", err)
	}
	if _, err := provider.VerifyWebhook(http.Header{}, body); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("a missing signature must fail, got %v", err)
	}
	merchant, _ := antilopayTestKeys(t)
	digest := sha256.Sum256(body)
	forged, _ := rsa.SignPKCS1v15(nil, merchant, crypto.SHA256, digest[:])
	if _, err := provider.VerifyWebhook(http.Header{"X-Apay-Callback": {base64.StdEncoding.EncodeToString(forged)}}, body); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("a signature by another key must fail, got %v", err)
	}

	for _, bad := range []string{
		`{"type":"payment","payment_id":"masked-invoice","order_id":"order-1","status":"SUCCESS","currency":"RUB"}`,
		`{"type":"payment","payment_id":"masked-invoice","order_id":"order-1","status":"SUCCESS","original_amount":100.001,"currency":"RUB"}`,
		`{"type":"payment","payment_id":"masked-invoice","order_id":"order-1","status":"SUCCESS","original_amount":100.01,"currency":"USD"}`,
		`{"type":"payment","order_id":"order-1","status":"SUCCESS","original_amount":100.01,"currency":"RUB"}`,
	} {
		if _, err := provider.VerifyWebhook(antilopayTestCallback(t, []byte(bad)), []byte(bad)); err == nil {
			t.Fatalf("an unverifiable settlement must be rejected: %s", bad)
		}
	}
	unknown := []byte(`{"type":"payment","payment_id":"masked-invoice","order_id":"order-1","status":"UNKNOWN","original_amount":100.01,"currency":"RUB"}`)
	if event, err := provider.VerifyWebhook(antilopayTestCallback(t, unknown), unknown); err != nil || event.Status == "succeeded" {
		t.Fatalf("an unknown status is acknowledged but never settles: %#v %v", event, err)
	}
	other := []byte(`{"type":"refund","payment_id":"masked-invoice","order_id":"order-1"}`)
	if event, err := provider.VerifyWebhook(antilopayTestCallback(t, other), other); err != nil || event.Status == "succeeded" {
		t.Fatalf("a non-payment notification is ignored: %#v %v", event, err)
	}
}

func TestAntilopayRefundIsUnsupported(t *testing.T) {
	provider := antilopayTestProvider(t, nil)
	if _, err := provider.Refund(t.Context(), RefundRequest{PaymentReference: "order-1", IdempotencyKey: "refund-key"}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported, got %v", err)
	}
}

func TestAntilopayProbe(t *testing.T) {
	var seen map[string]any
	provider := antilopayTestProvider(t, antilopayTestServer(t, 200, `{"code":8,"error":"Payment not found"}`, &seen))
	if err := Probe(t.Context(), provider); err != nil {
		t.Fatal(err)
	}
	if seen["path"] != "/payment/check" {
		t.Fatalf("probe must only check: %#v", seen)
	}
	rejected := antilopayTestProvider(t, antilopayTestServer(t, 401, `{"code":2,"error":"bad sign"}`, nil))
	if err := Probe(t.Context(), rejected); err == nil {
		t.Fatal("rejected credentials must fail the probe")
	}
}
