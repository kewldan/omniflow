package payments

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/commerce"
)

const overpayTestOrder = "0b0e6c0a-4a63-4f63-9d0e-3f6f1d2a9b11"

func overpayEnv(values map[string]string) Settings {
	return Settings{Env: func(key string) string { return values[key] }}
}

func overpayAt(t *testing.T, handler http.HandlerFunc) *Overpay {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	provider, err := buildOverpay(overpayEnv(map[string]string{
		"APP_OVERPAY_USERNAME": "merchant", "APP_OVERPAY_PASSWORD": "secret",
		"APP_OVERPAY_PROJECT_ID": "project-1", "APP_OVERPAY_API_URL": server.URL,
	}))
	if err != nil {
		t.Fatal(err)
	}
	return provider.(*Overpay)
}

func overpayTestPEM(t *testing.T) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "merchant"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
}

func TestOverpayBuilder(t *testing.T) {
	complete := map[string]string{"APP_OVERPAY_USERNAME": "u", "APP_OVERPAY_PASSWORD": "p", "APP_OVERPAY_PROJECT_ID": "proj"}
	if _, err := buildOverpay(overpayEnv(nil)); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("expected not configured, got %v", err)
	}
	if _, err := buildOverpay(overpayEnv(map[string]string{"APP_OVERPAY_USERNAME": "u"})); err == nil || errors.Is(err, ErrNotConfigured) {
		t.Fatalf("half a configuration must fail, got %v", err)
	}
	provider, err := buildOverpay(overpayEnv(complete))
	if err != nil {
		t.Fatal(err)
	}
	overpay := provider.(*Overpay)
	if overpay.baseURL != "https://api.overpay.io" || overpay.lifetime != 1440 || strings.Join(overpay.methods, ",") != "card,fps" || overpay.currency != "RUB" {
		t.Fatalf("unexpected defaults %#v", overpay)
	}
	if capabilities := provider.Capabilities(); capabilities.Refunds || capabilities.Recurring || !capabilities.Webhooks || !capabilities.Polling {
		t.Fatalf("unexpected capabilities %#v", capabilities)
	}
	if !NeedsWebhookPolling(provider) {
		t.Fatal("Overpay notifications are authenticated only by re-reading the order")
	}
	if !SupportsCurrency(provider, "RUB") || SupportsCurrency(provider, "EUR") {
		t.Fatal("Overpay settles its configured currency only")
	}

	certificate, key := overpayTestPEM(t)
	withCert := map[string]string{"APP_OVERPAY_CLIENT_CERT": certificate, "APP_OVERPAY_CLIENT_KEY": key}
	for name, value := range complete {
		withCert[name] = value
	}
	if _, err := buildOverpay(overpayEnv(withCert)); err != nil {
		t.Fatalf("a PEM certificate and key must be accepted: %v", err)
	}
	withCert["APP_OVERPAY_CLIENT_CERT"] = strings.ReplaceAll(certificate, "\n", `\n`)
	withCert["APP_OVERPAY_CLIENT_KEY"] = strings.ReplaceAll(key, "\n", `\n`)
	if _, err := buildOverpay(overpayEnv(withCert)); err != nil {
		t.Fatalf("a single-line PEM with escaped newlines must be accepted: %v", err)
	}
	_, otherKey := overpayTestPEM(t)
	withCert["APP_OVERPAY_CLIENT_KEY"] = otherKey
	if _, err := buildOverpay(overpayEnv(withCert)); err == nil || strings.Contains(err.Error(), "PRIVATE KEY") {
		t.Fatalf("a mismatched key must fail without quoting it, got %v", err)
	}
	delete(withCert, "APP_OVERPAY_CLIENT_KEY")
	if _, err := buildOverpay(overpayEnv(withCert)); err == nil {
		t.Fatal("a certificate without a key must fail")
	}
}

func TestOverpayCreateRequestShape(t *testing.T) {
	var body map[string]any
	provider := overpayAt(t, func(writer http.ResponseWriter, request *http.Request) {
		user, password, _ := request.BasicAuth()
		if request.Method != http.MethodPost || request.URL.Path != "/orders/" || user != "merchant" || password != "secret" {
			t.Errorf("unexpected request %s %s", request.Method, request.URL.Path)
		}
		_ = json.NewDecoder(request.Body).Decode(&body)
		writer.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(writer, `{"id":"ord-77","resultUrl":"https://pay.overpay.io/ord-77"}`)
	})
	intent, err := provider.Create(t.Context(), CreateRequest{
		OrderID: overpayTestOrder, Amount: commerce.Money{Amount: 50000, Currency: "RUB"}, Description: "Plan", ReturnURL: "https://shop.example/r",
	})
	if err != nil {
		t.Fatal(err)
	}
	if body["amount"] != "500.00" || body["currency"] != "RUB" || body["livetimeMinutes"] != float64(1440) || body["projectId"] != "project-1" ||
		body["merchantTransactionId"] != overpayTestOrder || body["returnUrl"] != "https://shop.example/r" || body["description"] != "Plan" {
		t.Fatalf("unexpected body %#v", body)
	}
	if methods, _ := body["paymentMethods"].([]any); len(methods) != 2 || methods[0] != "card" || methods[1] != "fps" {
		t.Fatalf("unexpected payment methods %#v", body["paymentMethods"])
	}
	if intent.ProviderReference != "ord-77" || intent.CheckoutURL != "https://pay.overpay.io/ord-77" || intent.Status != "pending" || intent.ExpiresAt == nil {
		t.Fatalf("unexpected intent %#v", intent)
	}
	if _, err := provider.Create(t.Context(), CreateRequest{OrderID: "o", Amount: commerce.Money{Amount: 1, Currency: "EUR"}}); !errors.Is(err, commerce.ErrCurrencyMismatch) {
		t.Fatalf("expected currency rejection, got %v", err)
	}
}

func TestOverpayPollMapsStatusesAndConfirmsTheAmount(t *testing.T) {
	for _, test := range []struct {
		body   string
		status string
		amount int64
	}{
		{`{"orders":[{"id":"ord-77","status":"charged","amount":"500.00","currency":"RUB"}]}`, "succeeded", 50000},
		{`{"id":"ord-77","status":"charged","amount":500,"currency":"RUB"}`, "succeeded", 50000},
		{`{"orders":[{"id":"ord-77","status":"authorized","amount":"500.00","currency":"RUB"}]}`, "processing", 50000},
		{`{"orders":[{"id":"ord-77","status":"new"}]}`, "pending", 0},
		{`{"orders":[{"id":"ord-77","status":"declined"}]}`, "failed", 0},
		{`{"orders":[{"id":"ord-77","status":"refunded"}]}`, "refunded", 0},
		{`{"orders":[{"id":"ord-77","status":"something-new"}]}`, "pending", 0},
	} {
		provider := overpayAt(t, func(writer http.ResponseWriter, request *http.Request) {
			if request.Method != http.MethodGet || request.URL.Path != "/orders/ord-77" {
				t.Errorf("unexpected poll %s %s", request.Method, request.URL.Path)
			}
			_, _ = io.WriteString(writer, test.body)
		})
		intent, err := provider.Poll(t.Context(), "ord-77")
		if err != nil {
			t.Fatalf("%s: %v", test.body, err)
		}
		if intent.Status != test.status || intent.Amount.Amount != test.amount {
			t.Fatalf("%s: unexpected intent %#v", test.body, intent)
		}
	}
	unconfirmed := overpayAt(t, func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, `{"orders":[{"id":"ord-77","status":"charged"}]}`)
	})
	if _, err := unconfirmed.Poll(t.Context(), "ord-77"); !errors.Is(err, ErrProviderResponse) {
		t.Fatalf("a charged order without an amount must not settle, got %v", err)
	}
	other := overpayAt(t, func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, `{"orders":[{"id":"ord-1","status":"charged","amount":"1.00"}]}`)
	})
	if _, err := other.Poll(t.Context(), "ord-77"); !errors.Is(err, ErrProviderResponse) {
		t.Fatalf("another order's state must not be applied, got %v", err)
	}
}

func TestOverpayWebhookIsAnUntrustedHint(t *testing.T) {
	provider := overpayAt(t, func(http.ResponseWriter, *http.Request) {})
	body := []byte(`{"id":"ord-77","merchantTransactionId":"` + overpayTestOrder + `","status":"charged"}`)
	event, err := provider.VerifyWebhook(http.Header{}, body)
	if err != nil {
		t.Fatal(err)
	}
	if event.ID != "charged:ord-77" || event.ProviderReference != "ord-77" || event.MerchantReference != overpayTestOrder || event.Status != "succeeded" {
		t.Fatalf("unexpected event %#v", event)
	}
	if event.Amount.Amount != 0 {
		t.Fatal("the notification carries no trusted amount; Poll supplies it")
	}
	again, _ := provider.VerifyWebhook(http.Header{}, body)
	if again.ID != event.ID {
		t.Fatal("a redelivery must keep its event ID")
	}
	numeric, err := provider.VerifyWebhook(http.Header{}, []byte(`{"id":77,"merchantTransactionId":"m","status":"something"}`))
	if err != nil || numeric.ProviderReference != "77" || numeric.Status != "pending" {
		t.Fatalf("unexpected numeric-id event %#v %v", numeric, err)
	}
	for _, invalid := range []string{`{"id":"ord-77","status":"charged"}`, `{"merchantTransactionId":"m","status":"charged"}`, `not json`} {
		if _, err := provider.VerifyWebhook(http.Header{}, []byte(invalid)); !errors.Is(err, ErrProviderResponse) {
			t.Fatalf("%s: expected rejection, got %v", invalid, err)
		}
	}
}

func TestOverpayRefusalsAndRefunds(t *testing.T) {
	for status, refused := range map[int]bool{400: true, 401: true, 500: false, 504: false} {
		provider := overpayAt(t, func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(status)
			_, _ = io.WriteString(writer, `{"message":"nope"}`)
		})
		_, err := provider.Create(t.Context(), CreateRequest{OrderID: "o", Amount: commerce.Money{Amount: 100, Currency: "RUB"}})
		if Refused(err) != refused {
			t.Fatalf("HTTP %d: refused=%v want %v", status, Refused(err), refused)
		}
	}
	provider := overpayAt(t, func(http.ResponseWriter, *http.Request) { t.Error("a refund must not reach Overpay") })
	if _, err := provider.Refund(t.Context(), RefundRequest{PaymentReference: "ord-77", IdempotencyKey: "k"}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("expected unsupported refund, got %v", err)
	}
}

func overpayInternationalEnv(server string, extra map[string]string) Settings {
	values := map[string]string{
		"APP_OVERPAY_USERNAME": "merchant", "APP_OVERPAY_PASSWORD": "secret",
		"APP_OVERPAY_PROJECT_ID": "project-1", "APP_OVERPAY_API_URL": server,
		"APP_OVERPAY_INT_PROJECT_ID": "project-int", "APP_OVERPAY_INT_RATE": "98.50",
	}
	for key, value := range extra {
		values[key] = value
	}
	return overpayEnv(values)
}

func TestOverpayInternationalConfiguration(t *testing.T) {
	provider, err := buildOverpay(overpayInternationalEnv("https://api.example", nil))
	if err != nil {
		t.Fatal(err)
	}
	overpay := provider.(*Overpay)
	if strings.Join(ProviderMethods(provider), ",") != ",intl" {
		t.Fatalf("methods = %q, want the default and intl", ProviderMethods(provider))
	}
	if overpay.international.currency != "EUR" || overpay.international.rate != 9850 {
		t.Fatalf("international = %+v", overpay.international)
	}

	// Without the foreign-card terminal there is one button, as before.
	plain, err := buildOverpay(overpayEnv(map[string]string{"APP_OVERPAY_USERNAME": "u", "APP_OVERPAY_PASSWORD": "p", "APP_OVERPAY_PROJECT_ID": "proj"}))
	if err != nil {
		t.Fatal(err)
	}
	if methods := ProviderMethods(plain); len(methods) != 1 || methods[0] != "" {
		t.Fatalf("plain methods = %q", methods)
	}

	for name, extra := range map[string]map[string]string{
		"terminal without a rate": {"APP_OVERPAY_INT_RATE": ""},
		"rate without a terminal": {"APP_OVERPAY_INT_PROJECT_ID": ""},
		"zero rate":               {"APP_OVERPAY_INT_RATE": "0"},
		"not a number":            {"APP_OVERPAY_INT_RATE": "ninety"},
		"the shop's own currency": {"APP_OVERPAY_INT_CURRENCY": "RUB"},
		"unknown currency":        {"APP_OVERPAY_INT_CURRENCY": "ZZZ"},
	} {
		if _, err := buildOverpay(overpayInternationalEnv("https://api.example", extra)); err == nil {
			t.Errorf("%s: the build must fail", name)
		}
	}
}

func TestOverpayInternationalChargesTheConvertedAmount(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		raw, _ := io.ReadAll(request.Body)
		_ = json.Unmarshal(raw, &body)
		_, _ = writer.Write([]byte(`{"id":"ord-77","resultUrl":"https://pay.example/77"}`))
	}))
	t.Cleanup(server.Close)
	provider, err := buildOverpay(overpayInternationalEnv(server.URL, map[string]string{"APP_OVERPAY_INT_MINIMUM": "5"}))
	if err != nil {
		t.Fatal(err)
	}

	// 299.00 RUB at 98.50 a euro is 3.0355… EUR, which rounds up to 3.04:
	// rounding down would leave the payment a cent short of the order.
	intent, err := provider.Create(t.Context(), CreateRequest{
		OrderID: overpayTestOrder, Amount: commerce.Money{Amount: 29900, Currency: "RUB"}, Method: "intl",
	})
	if !errors.Is(err, ErrBelowProviderMinimum) {
		t.Fatalf("3.04 EUR is under the 5 EUR minimum, got %v %+v", err, intent)
	}

	intent, err = provider.Create(t.Context(), CreateRequest{
		OrderID: overpayTestOrder, Amount: commerce.Money{Amount: 59900, Currency: "RUB"}, Method: "intl",
	})
	if err != nil {
		t.Fatal(err)
	}
	// 599.00 / 98.50 = 6.0812… → 6.09 EUR.
	if body["amount"] != "6.09" || body["currency"] != "EUR" || body["projectId"] != "project-int" {
		t.Fatalf("request body = %v", body)
	}
	if methods, _ := body["paymentMethods"].([]any); len(methods) != 1 || methods[0] != "card" {
		t.Fatalf("the international terminal takes cards only: %v", body["paymentMethods"])
	}
	if intent.Charged == nil || *intent.Charged != (commerce.Money{Amount: 609, Currency: "EUR"}) {
		t.Fatalf("charged = %+v", intent.Charged)
	}
	if intent.Amount != (commerce.Money{Amount: 59900, Currency: "RUB"}) {
		t.Fatalf("the intent still prices the order in rubles, got %+v", intent.Amount)
	}

	// The default button keeps the ruble terminal and reports no conversion.
	intent, err = provider.Create(t.Context(), CreateRequest{
		OrderID: overpayTestOrder, Amount: commerce.Money{Amount: 59900, Currency: "RUB"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if body["currency"] != "RUB" || body["projectId"] != "project-1" || intent.Charged != nil {
		t.Fatalf("default checkout body = %v, charged = %+v", body, intent.Charged)
	}
}

func TestOverpayInternationalPollReportsEuros(t *testing.T) {
	provider := overpayAt(t, func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"id":"ord-77","status":"charged","amount":"6.09","currency":"EUR"}`))
	})
	intent, err := provider.Poll(t.Context(), "ord-77")
	if err != nil {
		t.Fatal(err)
	}
	if intent.Status != "succeeded" || intent.Amount != (commerce.Money{Amount: 609, Currency: "EUR"}) {
		t.Fatalf("poll = %+v", intent)
	}
}
