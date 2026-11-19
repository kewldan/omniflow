package payments

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
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

const cloudPaymentsTestOrder = "7c1f4a52-3b0e-4d4b-9a51-0f4f3b8f2a10"

func cloudPaymentsEnv(values map[string]string) Settings {
	return Settings{Env: func(key string) string { return values[key] }}
}

func cloudPaymentsAt(t *testing.T, handler http.HandlerFunc) *CloudPayments {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	provider, err := buildCloudPayments(cloudPaymentsEnv(map[string]string{
		"APP_CLOUDPAYMENTS_PUBLIC_ID":  "pk_test",
		"APP_CLOUDPAYMENTS_API_SECRET": "api-secret",
		"APP_CLOUDPAYMENTS_API_URL":    server.URL,
	}))
	if err != nil {
		t.Fatal(err)
	}
	return provider.(*CloudPayments)
}

func cloudPaymentsSign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func TestCloudPaymentsBuilder(t *testing.T) {
	if _, err := buildCloudPayments(cloudPaymentsEnv(nil)); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("no credentials must leave the adapter unregistered, got %v", err)
	}
	_, err := buildCloudPayments(cloudPaymentsEnv(map[string]string{"APP_CLOUDPAYMENTS_PUBLIC_ID": "pk"}))
	if err == nil || errors.Is(err, ErrNotConfigured) {
		t.Fatalf("half a configuration must fail loudly, got %v", err)
	}
	_, err = buildCloudPayments(cloudPaymentsEnv(map[string]string{
		"APP_CLOUDPAYMENTS_PUBLIC_ID": "pk", "APP_CLOUDPAYMENTS_API_SECRET": "s", "APP_CLOUDPAYMENTS_CURRENCY": "XXX",
	}))
	if err == nil {
		t.Fatal("an unknown settlement currency must be rejected")
	}
	provider, err := buildCloudPayments(cloudPaymentsEnv(map[string]string{
		"APP_CLOUDPAYMENTS_PUBLIC_ID": "pk", "APP_CLOUDPAYMENTS_API_SECRET": "s",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if provider.Name() != "cloudpayments" || !SupportsCurrency(provider, "RUB") || SupportsCurrency(provider, "USD") {
		t.Fatal("the default build settles RUB only")
	}
	capabilities := provider.Capabilities()
	if !capabilities.Refunds || !capabilities.PartialRefund || capabilities.Recurring || !capabilities.Webhooks || !capabilities.Polling {
		t.Fatalf("unexpected capabilities %#v", capabilities)
	}
}

func TestCloudPaymentsCreateSendsAnOrderKeyedByTheOrderID(t *testing.T) {
	var seen map[string]any
	provider := cloudPaymentsAt(t, func(writer http.ResponseWriter, request *http.Request) {
		user, password, _ := request.BasicAuth()
		if request.URL.Path != "/orders/create" || user != "pk_test" || password != "api-secret" {
			t.Errorf("unexpected request %s as %s", request.URL.Path, user)
		}
		_ = json.NewDecoder(request.Body).Decode(&seen)
		_, _ = io.WriteString(writer, `{"Success":true,"Message":null,"Model":{"Id":"f2K8LV6reGE9WBFn","Url":"https://orders.cloudpayments.ru/d/f2K8LV6reGE9WBFn"}}`)
	})
	intent, err := provider.Create(t.Context(), CreateRequest{
		OrderID: cloudPaymentsTestOrder, Amount: commerce.Money{Amount: 12345, Currency: "RUB"},
		Description: "Plan", ReturnURL: "https://shop.example/account/orders/1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen["Amount"] != 123.45 || seen["Currency"] != "RUB" || seen["InvoiceId"] != cloudPaymentsTestOrder || seen["SuccessRedirectUrl"] != "https://shop.example/account/orders/1" {
		t.Fatalf("unexpected request body %#v", seen)
	}
	if intent.ProviderReference != cloudPaymentsTestOrder || intent.Status != "pending" || intent.CheckoutURL != "https://orders.cloudpayments.ru/d/f2K8LV6reGE9WBFn" || intent.Amount.Amount != 12345 {
		t.Fatalf("unexpected intent %#v", intent)
	}
	if _, err := provider.Create(t.Context(), CreateRequest{OrderID: "o", Amount: commerce.Money{Amount: 1, Currency: "USD"}}); !errors.Is(err, commerce.ErrCurrencyMismatch) {
		t.Fatalf("a foreign currency must be refused before calling out, got %v", err)
	}
}

func TestCloudPaymentsCreateRejectsAnUnsuccessfulAnswer(t *testing.T) {
	provider := cloudPaymentsAt(t, func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, `{"Success":false,"Message":"Invalid amount"}`)
	})
	_, err := provider.Create(t.Context(), CreateRequest{OrderID: "o", Amount: commerce.Money{Amount: 100, Currency: "RUB"}})
	if !errors.Is(err, ErrProviderResponse) {
		t.Fatalf("expected provider error, got %v", err)
	}
}

func TestCloudPaymentsPollMapsStatuses(t *testing.T) {
	for _, test := range []struct {
		body   string
		status string
		amount int64
	}{
		{`{"Success":true,"Model":{"TransactionId":504,"Amount":123.45,"Currency":"RUB","Status":"Completed","InvoiceId":"` + cloudPaymentsTestOrder + `"}}`, "succeeded", 12345},
		{`{"Success":true,"Model":{"TransactionId":504,"Amount":10,"Currency":"RUB","Status":"Authorized","InvoiceId":"` + cloudPaymentsTestOrder + `"}}`, "processing", 1000},
		{`{"Success":true,"Model":{"TransactionId":504,"Amount":10,"Currency":"RUB","Status":"Declined","InvoiceId":"` + cloudPaymentsTestOrder + `"}}`, "failed", 1000},
		{`{"Success":true,"Model":{"TransactionId":504,"Amount":10,"Currency":"RUB","Status":"Cancelled"}}`, "cancelled", 1000},
		{`{"Success":false,"Message":"Not found"}`, "pending", 0},
	} {
		provider := cloudPaymentsAt(t, func(writer http.ResponseWriter, request *http.Request) {
			var body map[string]string
			_ = json.NewDecoder(request.Body).Decode(&body)
			if request.URL.Path != "/payments/find" || body["InvoiceId"] != cloudPaymentsTestOrder {
				t.Errorf("unexpected poll %s %#v", request.URL.Path, body)
			}
			_, _ = io.WriteString(writer, test.body)
		})
		intent, err := provider.Poll(t.Context(), cloudPaymentsTestOrder)
		if err != nil {
			t.Fatal(err)
		}
		if intent.Status != test.status || intent.Amount.Amount != test.amount || intent.ProviderReference != cloudPaymentsTestOrder {
			t.Fatalf("%s: unexpected intent %#v", test.body, intent)
		}
	}
	mismatch := cloudPaymentsAt(t, func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, `{"Success":true,"Model":{"TransactionId":1,"Amount":1,"Currency":"RUB","Status":"Completed","InvoiceId":"other"}}`)
	})
	if _, err := mismatch.Poll(t.Context(), cloudPaymentsTestOrder); !errors.Is(err, ErrProviderResponse) {
		t.Fatalf("a payment for another invoice must not be accepted, got %v", err)
	}
}

func TestCloudPaymentsWebhookSignatureAndClassification(t *testing.T) {
	provider := cloudPaymentsAt(t, func(http.ResponseWriter, *http.Request) {})
	pay := url.Values{
		"TransactionId": {"504"}, "Amount": {"123.45"}, "Currency": {"RUB"}, "InvoiceId": {cloudPaymentsTestOrder},
		"AccountId": {"1"}, "Status": {"Completed"}, "Reason": {"Approved"}, "AuthCode": {"A1B2C3"}, "TestMode": {"0"},
		"DateTime": {"2026-11-19 10:00:00"},
	}
	body := []byte(pay.Encode())
	headers := http.Header{"Content-Hmac": {cloudPaymentsSign("api-secret", body)}}
	event, err := provider.VerifyWebhook(headers, body)
	if err != nil {
		t.Fatal(err)
	}
	if event.ID != "pay:504" || event.Status != "succeeded" || event.ProviderReference != cloudPaymentsTestOrder || event.MerchantReference != cloudPaymentsTestOrder || event.Amount != (commerce.Money{Amount: 12345, Currency: "RUB"}) {
		t.Fatalf("unexpected event %#v", event)
	}
	again, err := provider.VerifyWebhook(headers, body)
	if err != nil || again.ID != event.ID {
		t.Fatalf("a redelivery must keep its event ID: %#v %v", again, err)
	}

	// X-Content-HMAC is computed over the URL-decoded body.
	decoded, _ := url.QueryUnescape(string(body))
	if _, err := provider.VerifyWebhook(http.Header{"X-Content-Hmac": {cloudPaymentsSign("api-secret", []byte(decoded))}}, body); err != nil {
		t.Fatalf("the decoded-body signature must be accepted: %v", err)
	}

	for name, headers := range map[string]http.Header{
		"missing": {},
		"forged":  {"Content-Hmac": {cloudPaymentsSign("other-secret", body)}},
	} {
		if _, err := provider.VerifyWebhook(headers, body); !errors.Is(err, ErrInvalidSignature) {
			t.Fatalf("%s signature: expected rejection, got %v", name, err)
		}
	}
	tampered := []byte(strings.Replace(string(body), "Amount=123.45", "Amount=1.00", 1))
	if _, err := provider.VerifyWebhook(http.Header{"Content-Hmac": {cloudPaymentsSign("api-secret", body)}}, tampered); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("a tampered amount must fail verification, got %v", err)
	}

	classify := func(values url.Values) WebhookEvent {
		t.Helper()
		raw := []byte(values.Encode())
		event, err := provider.VerifyWebhook(http.Header{"Content-Hmac": {cloudPaymentsSign("api-secret", raw)}}, raw)
		if err != nil {
			t.Fatal(err)
		}
		return event
	}
	check := url.Values{"TransactionId": {"504"}, "Amount": {"123.45"}, "Currency": {"RUB"}, "InvoiceId": {cloudPaymentsTestOrder}, "Status": {"Completed"}}
	if event := classify(check); event.ID != "check:504" || event.Status != "pending" {
		t.Fatalf("a Check notification must never settle: %#v", event)
	}
	fail := url.Values{"TransactionId": {"505"}, "Amount": {"123.45"}, "Currency": {"RUB"}, "InvoiceId": {cloudPaymentsTestOrder}, "Status": {"Declined"}, "Reason": {"InsufficientFunds"}, "ReasonCode": {"5051"}}
	if event := classify(fail); event.ID != "fail:505" || event.Status != "failed" {
		t.Fatalf("unexpected fail event %#v", event)
	}
	held := url.Values{"TransactionId": {"506"}, "Amount": {"1"}, "Currency": {"RUB"}, "InvoiceId": {cloudPaymentsTestOrder}, "Status": {"Authorized"}, "AuthCode": {"X"}}
	if event := classify(held); event.ID != "authorized:506" || event.Status != "processing" {
		t.Fatalf("an uncaptured hold is not a payment: %#v", event)
	}
	unknown := url.Values{"TransactionId": {"507"}, "Amount": {"1"}, "Currency": {"RUB"}, "Status": {"Mystery"}, "Reason": {"?"}}
	if event := classify(unknown); event.Status != "pending" {
		t.Fatalf("an unknown status must not settle: %#v", event)
	}
	test := url.Values{"TransactionId": {"508"}, "Amount": {"1"}, "Currency": {"RUB"}, "Status": {"Completed"}, "Reason": {"Approved"}, "TestMode": {"1"}}
	if event := classify(test); event.Status == "succeeded" {
		t.Fatalf("a test payment on a live site must not settle: %#v", event)
	}

	jsonBody := []byte(`{"TransactionId":509,"Amount":50.5,"Currency":"RUB","InvoiceId":"` + cloudPaymentsTestOrder + `","Status":"Completed","Reason":"Approved"}`)
	event, err = provider.VerifyWebhook(http.Header{"Content-Hmac": {cloudPaymentsSign("api-secret", jsonBody)}}, jsonBody)
	if err != nil || event.ID != "pay:509" || event.Amount.Amount != 5050 {
		t.Fatalf("a JSON notification must parse the same way: %#v %v", event, err)
	}

	status, contentType, ack := provider.WebhookAck("paid")
	if status != http.StatusOK || contentType != "application/json" || string(ack) != `{"code":0}` {
		t.Fatalf("unexpected acknowledgement %d %s %s", status, contentType, ack)
	}
}

func TestCloudPaymentsRefundUsesTheTransactionAndRequestID(t *testing.T) {
	var requestIDs []string
	var refundBody map[string]any
	provider := cloudPaymentsAt(t, func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/payments/find":
			_, _ = io.WriteString(writer, `{"Success":true,"Model":{"TransactionId":504,"Amount":123.45,"Currency":"RUB","Status":"Completed","InvoiceId":"`+cloudPaymentsTestOrder+`"}}`)
		case "/payments/refund":
			requestIDs = append(requestIDs, request.Header.Get("X-Request-ID"))
			_ = json.NewDecoder(request.Body).Decode(&refundBody)
			_, _ = io.WriteString(writer, `{"Success":true,"Message":null,"Model":{"TransactionId":568}}`)
		}
	})
	request := RefundRequest{PaymentReference: cloudPaymentsTestOrder, IdempotencyKey: "refund-key-0001", Amount: commerce.Money{Amount: 4500, Currency: "RUB"}}
	refund, err := provider.Refund(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if refund.ProviderReference != "568" || refund.Status != "succeeded" || refund.Amount.Amount != 4500 {
		t.Fatalf("unexpected refund %#v", refund)
	}
	if refundBody["TransactionId"] != float64(504) || refundBody["Amount"] != float64(45) {
		t.Fatalf("unexpected refund body %#v", refundBody)
	}
	if _, err := provider.Refund(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if len(requestIDs) != 2 || requestIDs[0] != "refund-key-0001" || requestIDs[1] != requestIDs[0] {
		t.Fatalf("a repeated refund must carry the same request ID, got %v", requestIDs)
	}
}

func TestCloudPaymentsClassifiesRefusalsAndOutages(t *testing.T) {
	for status, refused := range map[int]bool{401: true, 400: true, 500: false, 503: false} {
		provider := cloudPaymentsAt(t, func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(status)
		})
		_, err := provider.Create(t.Context(), CreateRequest{OrderID: "o", Amount: commerce.Money{Amount: 100, Currency: "RUB"}})
		if Refused(err) != refused {
			t.Fatalf("HTTP %d: refused=%v, want %v (%v)", status, Refused(err), refused, err)
		}
	}
	probe := cloudPaymentsAt(t, func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, `{"Success":false,"Message":"Not found"}`)
	})
	if err := probe.Probe(t.Context()); err != nil {
		t.Fatalf("valid credentials must pass the probe: %v", err)
	}
	denied := cloudPaymentsAt(t, func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusUnauthorized) })
	if err := denied.Probe(t.Context()); !Refused(err) {
		t.Fatalf("rejected credentials must fail the probe, got %v", err)
	}
}
