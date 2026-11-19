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
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/commerce"
)

const (
	payhotTestOrder   = "6a0f3c1e-2b4d-4e8f-9a1b-3c5d7e9f1a2b"
	payhotTestPayment = "22222222-2222-4222-8222-222222222222"
	payhotTestEvent   = "11111111-1111-4111-8111-111111111111"
	payhotTestMandate = "33333333-3333-4333-8333-333333333333"
	payhotTestCharge  = "44444444-4444-4444-8444-444444444444"
	payhotTestSecret  = "whsec_v2_synthetic"
)

var payhotTestNow = time.Unix(1_790_000_000, 0)

func payhotEnv(values map[string]string) Settings {
	return Settings{Env: func(key string) string { return values[key] }}
}

func payhotAt(t *testing.T, handler http.HandlerFunc) *PayHot {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	provider, err := buildPayHot(payhotEnv(map[string]string{
		"APP_PAYHOT_API_KEY": "phk_v2_synthetic", "APP_PAYHOT_WEBHOOK_SECRET": payhotTestSecret,
		"APP_PAYHOT_PREVIOUS_WEBHOOK_SECRET": "whsec_v2_previous",
	}))
	if err != nil {
		t.Fatal(err)
	}
	payhot := provider.(*PayHot)
	payhot.baseURL = server.URL
	payhot.clock = func() time.Time { return payhotTestNow }
	return payhot
}

func payhotHeaders(body []byte, secret string, offset int64) http.Header {
	stamp := strconv.FormatInt(payhotTestNow.Unix()+offset, 10)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(stamp + "."))
	_, _ = mac.Write(body)
	headers := make(http.Header)
	headers.Set("PayHot-Signature", "t="+stamp+",v2="+hex.EncodeToString(mac.Sum(nil)))
	headers.Set("PayHot-Event-Id", payhotTestEvent)
	headers.Set("PayHot-Delivery-Id", "delivery-1")
	headers.Set("PayHot-Delivery-Attempt", "1")
	headers.Set("PayHot-Api-Version", "v2")
	return headers
}

func payhotPaymentJSON(status string) string {
	return `{"id":"` + payhotTestPayment + `","status":"` + status + `","amount_minor":"12345","currency":"RUB","merchant_order_reference":"` + payhotTestOrder + `","payment_method":"card","environment":"production","expires_at":"2026-11-19T12:00:00Z"}`
}

func TestPayHotBuilder(t *testing.T) {
	if _, err := buildPayHot(payhotEnv(nil)); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("expected not configured, got %v", err)
	}
	for name, values := range map[string]map[string]string{
		"key only":     {"APP_PAYHOT_API_KEY": "phk_v2_x"},
		"secret only":  {"APP_PAYHOT_WEBHOOK_SECRET": "s"},
		"sandbox key":  {"APP_PAYHOT_API_KEY": "phk_test_x", "APP_PAYHOT_WEBHOOK_SECRET": "s"},
		"foreign host": {"APP_PAYHOT_API_KEY": "phk_v2_x", "APP_PAYHOT_WEBHOOK_SECRET": "s", "APP_PAYHOT_API_URL": "https://evil.example/api/v2"},
		"sberpay":      {"APP_PAYHOT_API_KEY": "phk_v2_x", "APP_PAYHOT_WEBHOOK_SECRET": "s", "APP_PAYHOT_PAYMENT_METHOD": "sberpay"},
		"http return":  {"APP_PAYHOT_API_KEY": "phk_v2_x", "APP_PAYHOT_WEBHOOK_SECRET": "s", "APP_PAYHOT_SUCCESS_URL": "http://shop.example"},
		"timeout":      {"APP_PAYHOT_API_KEY": "phk_v2_x", "APP_PAYHOT_WEBHOOK_SECRET": "s", "APP_PAYHOT_REQUEST_TIMEOUT": "120"},
	} {
		if _, err := buildPayHot(payhotEnv(values)); err == nil || errors.Is(err, ErrNotConfigured) {
			t.Fatalf("%s: expected a configuration error, got %v", name, err)
		}
	}
	provider, err := buildPayHot(payhotEnv(map[string]string{"APP_PAYHOT_API_KEY": "phk_v2_x", "APP_PAYHOT_WEBHOOK_SECRET": "s", "APP_PAYHOT_API_URL": "https://gapi.pay.hot/api/v2/"}))
	if err != nil {
		t.Fatal(err)
	}
	payhot := provider.(*PayHot)
	if payhot.method != "card" || payhot.baseURL != "https://gapi.pay.hot/api/v2" || len(payhot.secrets) != 1 {
		t.Fatalf("unexpected defaults %#v", payhot)
	}
	if capabilities := provider.Capabilities(); capabilities.Refunds || !capabilities.Recurring || !capabilities.Webhooks || !capabilities.Polling {
		t.Fatalf("unexpected capabilities %#v", capabilities)
	}
	if !NeedsWebhookPolling(provider) || !SupportsCurrency(provider, "RUB") || SupportsCurrency(provider, "USD") {
		t.Fatal("PayHot settles RUB and re-reads every notified payment")
	}
}

func TestPayHotCreateRequestShape(t *testing.T) {
	var body map[string]any
	var key, auth string
	provider := payhotAt(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/payments" {
			t.Errorf("unexpected request %s %s", request.Method, request.URL.Path)
		}
		key, auth = request.Header.Get("Idempotency-Key"), request.Header.Get("Authorization")
		_ = json.NewDecoder(request.Body).Decode(&body)
		writer.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(writer, `{"payment":`+payhotPaymentJSON("requires_payment_method")+`,"checkout":{"url":"https://checkout.pay.hot/s/abc#secret"}}`)
	})
	intent, err := provider.Create(t.Context(), CreateRequest{
		OrderID: payhotTestOrder, IdempotencyKey: "checkout-key-000001", Amount: commerce.Money{Amount: 12345, Currency: "RUB"},
		ReturnURL: "https://shop.example/account/orders/1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer phk_v2_synthetic" || key != "checkout-key-000001" {
		t.Fatalf("unexpected headers %q %q", auth, key)
	}
	customer, _ := body["customer"].(map[string]any)
	if body["merchant_order_reference"] != payhotTestOrder || body["amount_minor"] != "12345" || body["currency"] != "RUB" ||
		body["payment_method"] != "card" || customer["id"] != payhotTestOrder ||
		body["success_return_url"] != "https://shop.example/account/orders/1" || body["failure_return_url"] != "https://shop.example/account/orders/1" {
		t.Fatalf("unexpected body %#v", body)
	}
	if _, consent := body["recurring_enrollment"]; consent {
		t.Fatal("an ordinary checkout never asks for recurring consent")
	}
	if intent.ProviderReference != payhotTestPayment || intent.Status != "pending" || intent.CheckoutURL != "https://checkout.pay.hot/s/abc#secret" ||
		intent.Amount != (commerce.Money{Amount: 12345, Currency: "RUB"}) || intent.ExpiresAt == nil {
		t.Fatalf("unexpected intent %#v", intent)
	}

	// A key HOT Pay would reject is mapped to a stable digest, not dropped.
	if _, err := provider.Create(t.Context(), CreateRequest{OrderID: payhotTestOrder, IdempotencyKey: "short-1", Amount: commerce.Money{Amount: 12345, Currency: "RUB"}}); err != nil {
		t.Fatal(err)
	}
	first := key
	_, _ = provider.Create(t.Context(), CreateRequest{OrderID: payhotTestOrder, IdempotencyKey: "short-1", Amount: commerce.Money{Amount: 12345, Currency: "RUB"}})
	if !strings.HasPrefix(first, "omniflow-") || key != first {
		t.Fatalf("a short key must map to the same accepted key every time, got %q then %q", first, key)
	}
	if _, err := provider.Create(t.Context(), CreateRequest{OrderID: "o", Amount: commerce.Money{Amount: 1, Currency: "USD"}}); !errors.Is(err, commerce.ErrCurrencyMismatch) {
		t.Fatalf("expected currency rejection, got %v", err)
	}
}

func TestPayHotCreateReissuesALostCheckoutAndRejectsMismatches(t *testing.T) {
	var reissued bool
	provider := payhotAt(t, func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/payments":
			_, _ = io.WriteString(writer, `{"payment":`+payhotPaymentJSON("requires_payment_method")+`,"checkout":{"access_token_available":false,"reissue_required":true}}`)
		case "/payments/" + payhotTestPayment + "/checkout-sessions":
			reissued = request.Header.Get("Idempotency-Key") == ""
			_, _ = io.WriteString(writer, `{"payment":`+payhotPaymentJSON("requires_payment_method")+`,"checkout":{"url":"https://checkout.pay.hot/s/new"}}`)
		}
	})
	intent, err := provider.Create(t.Context(), CreateRequest{OrderID: payhotTestOrder, IdempotencyKey: "checkout-key-000001", Amount: commerce.Money{Amount: 12345, Currency: "RUB"}})
	if err != nil || !reissued || intent.CheckoutURL != "https://checkout.pay.hot/s/new" {
		t.Fatalf("a repeated create must reissue the checkout: %#v %v", intent, err)
	}

	mismatched := payhotAt(t, func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, `{"payment":`+payhotPaymentJSON("requires_payment_method")+`,"checkout":{"url":"https://checkout.pay.hot/s/abc"}}`)
	})
	if _, err := mismatched.Create(t.Context(), CreateRequest{OrderID: payhotTestOrder, IdempotencyKey: "checkout-key-000001", Amount: commerce.Money{Amount: 100, Currency: "RUB"}}); !errors.Is(err, ErrProviderResponse) {
		t.Fatalf("a payment for another principal must be rejected, got %v", err)
	}
	sandbox := payhotAt(t, func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, `{"payment":`+strings.Replace(payhotPaymentJSON("requires_payment_method"), `"production"`, `"sandbox"`, 1)+`,"checkout":{"url":"https://checkout.pay.hot/s/abc"}}`)
	})
	if _, err := sandbox.Create(t.Context(), CreateRequest{OrderID: payhotTestOrder, IdempotencyKey: "checkout-key-000001", Amount: commerce.Money{Amount: 12345, Currency: "RUB"}}); !errors.Is(err, ErrProviderResponse) {
		t.Fatalf("a sandbox payment must be rejected, got %v", err)
	}
	for status, refused := range map[int]bool{400: true, 401: true, 409: false, 429: false, 500: false} {
		provider := payhotAt(t, func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(status) })
		_, err := provider.Create(t.Context(), CreateRequest{OrderID: payhotTestOrder, IdempotencyKey: "checkout-key-000001", Amount: commerce.Money{Amount: 12345, Currency: "RUB"}})
		if Refused(err) != refused {
			t.Fatalf("HTTP %d: refused=%v want %v", status, Refused(err), refused)
		}
	}
}

func TestPayHotPollMapsStatuses(t *testing.T) {
	for raw, want := range map[string]string{
		"requires_payment_method": "pending", "requires_action": "requires_action", "processing": "processing",
		"authorized": "processing", "partially_captured": "processing", "succeeded": "succeeded", "failed": "failed",
		"cancelled": "cancelled", "refunded": "cancelled", "manual_reconciliation": "processing",
	} {
		provider := payhotAt(t, func(writer http.ResponseWriter, request *http.Request) {
			if request.Method != http.MethodGet || request.URL.Path != "/payments/"+payhotTestPayment {
				t.Errorf("unexpected poll %s %s", request.Method, request.URL.Path)
			}
			_, _ = io.WriteString(writer, `{"payment":`+payhotPaymentJSON(raw)+`}`)
		})
		intent, err := provider.Poll(t.Context(), payhotTestPayment)
		if err != nil {
			t.Fatal(err)
		}
		if intent.Status != want || intent.Amount.Amount != 12345 {
			t.Fatalf("%s: unexpected intent %#v", raw, intent)
		}
	}
	unknown := payhotAt(t, func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, `{"payment":`+payhotPaymentJSON("teleported")+`}`)
	})
	if _, err := unknown.Poll(t.Context(), payhotTestPayment); !errors.Is(err, ErrProviderResponse) {
		t.Fatalf("an unknown status must not be guessed at, got %v", err)
	}
	if _, err := unknown.Poll(t.Context(), "not-a-uuid"); !errors.Is(err, ErrProviderResponse) {
		t.Fatalf("a malformed reference must not be sent, got %v", err)
	}
}

func TestPayHotWebhookContract(t *testing.T) {
	provider := payhotAt(t, func(http.ResponseWriter, *http.Request) {})
	payload := `{"api_version":"v2","event_id":"` + payhotTestEvent + `","event_type":"payment.succeeded","data":{"object":"payment","id":"` + payhotTestPayment + `","status":"succeeded","amount_minor":"12345","currency":"RUB","merchant_order_reference":"` + payhotTestOrder + `"}}`
	body := []byte(payload)
	event, err := provider.VerifyWebhook(payhotHeaders(body, payhotTestSecret, 0), body)
	if err != nil {
		t.Fatal(err)
	}
	if event.ID != payhotTestEvent || event.Type != "payment.succeeded" || event.ProviderReference != payhotTestPayment ||
		event.MerchantReference != payhotTestOrder || event.Status != "succeeded" || event.Amount != (commerce.Money{Amount: 12345, Currency: "RUB"}) {
		t.Fatalf("unexpected event %#v", event)
	}
	again, _ := provider.VerifyWebhook(payhotHeaders(body, payhotTestSecret, 1), body)
	if again.ID != event.ID {
		t.Fatal("a redelivery must keep the event ID")
	}
	if _, err := provider.VerifyWebhook(payhotHeaders(body, "whsec_v2_previous", 0), body); err != nil {
		t.Fatalf("the previous secret must still verify during rotation: %v", err)
	}

	signatureFailures := map[string]func() (http.Header, []byte){
		"missing": func() (http.Header, []byte) {
			headers := payhotHeaders(body, payhotTestSecret, 0)
			headers.Del("PayHot-Signature")
			return headers, body
		},
		"forged": func() (http.Header, []byte) {
			headers := payhotHeaders(body, payhotTestSecret, 0)
			headers.Set("PayHot-Signature", "t="+strconv.FormatInt(payhotTestNow.Unix(), 10)+",v2="+strings.Repeat("0", 64))
			return headers, body
		},
		"tampered": func() (http.Header, []byte) {
			return payhotHeaders(body, payhotTestSecret, 0), []byte(strings.Replace(payload, `"12345"`, `"1"`, 1))
		},
		"expired": func() (http.Header, []byte) { return payhotHeaders(body, payhotTestSecret, -301), body },
		"future":  func() (http.Header, []byte) { return payhotHeaders(body, payhotTestSecret, 301), body },
		"wrong":   func() (http.Header, []byte) { return payhotHeaders(body, "whsec_v2_other", 0), body },
	}
	for name, build := range signatureFailures {
		headers, raw := build()
		if _, err := provider.VerifyWebhook(headers, raw); !errors.Is(err, ErrInvalidSignature) {
			t.Fatalf("%s: expected invalid signature, got %v", name, err)
		}
	}

	invalid := map[string]func() (http.Header, []byte){
		"duplicate header": func() (http.Header, []byte) {
			headers := payhotHeaders(body, payhotTestSecret, 0)
			headers.Add("PayHot-Signature", headers.Get("PayHot-Signature"))
			return headers, body
		},
		"event id": func() (http.Header, []byte) {
			headers := payhotHeaders(body, payhotTestSecret, 0)
			headers.Set("PayHot-Event-Id", "different")
			return headers, body
		},
		"version": func() (http.Header, []byte) {
			headers := payhotHeaders(body, payhotTestSecret, 0)
			headers.Set("PayHot-Api-Version", "v1")
			return headers, body
		},
		"attempt": func() (http.Header, []byte) {
			headers := payhotHeaders(body, payhotTestSecret, 0)
			headers.Set("PayHot-Delivery-Attempt", "0")
			return headers, body
		},
		"sandbox": func() (http.Header, []byte) {
			raw := []byte(strings.Replace(payload, `"event_type"`, `"environment":"sandbox","event_type"`, 1))
			return payhotHeaders(raw, payhotTestSecret, 0), raw
		},
		"sandbox event": func() (http.Header, []byte) {
			raw := []byte(strings.Replace(payload, `"payment.succeeded"`, `"sandbox.payment.succeeded"`, 1))
			return payhotHeaders(raw, payhotTestSecret, 0), raw
		},
		"duplicate json": func() (http.Header, []byte) {
			raw := []byte(strings.Replace(payload, `"api_version":"v2",`, `"api_version":"v2","api_version":"v2",`, 1))
			return payhotHeaders(raw, payhotTestSecret, 0), raw
		},
		"array": func() (http.Header, []byte) { return payhotHeaders([]byte(`[]`), payhotTestSecret, 0), []byte(`[]`) },
		"nan": func() (http.Header, []byte) {
			return payhotHeaders([]byte(`{"v":NaN}`), payhotTestSecret, 0), []byte(`{"v":NaN}`)
		},
		"oversize": func() (http.Header, []byte) {
			raw := make([]byte, 65537)
			return payhotHeaders(raw, payhotTestSecret, 0), raw
		},
		"object": func() (http.Header, []byte) {
			raw := []byte(strings.Replace(payload, `"object":"payment"`, `"object":"refund"`, 1))
			return payhotHeaders(raw, payhotTestSecret, 0), raw
		},
	}
	for name, build := range invalid {
		headers, raw := build()
		if _, err := provider.VerifyWebhook(headers, raw); err == nil {
			t.Fatalf("%s: expected rejection", name)
		}
	}

	status, contentType, ack := provider.WebhookAck("paid")
	if status != http.StatusOK || contentType != "application/json" || string(ack) != `{"success":true}` {
		t.Fatalf("unexpected acknowledgement %d %s %s", status, contentType, ack)
	}
}

func TestPayHotWebhookRoutesRecurringChargesToTheirOwnReference(t *testing.T) {
	provider := payhotAt(t, func(http.ResponseWriter, *http.Request) {})
	for _, payload := range []string{
		`{"api_version":"v2","event_id":"` + payhotTestEvent + `","event_type":"payment.succeeded","data":{"object":"payment","id":"` + payhotTestPayment + `","origin":"recurring","recurring_charge_id":"` + payhotTestCharge + `","status":"succeeded","amount_minor":"12345","currency":"RUB"}}`,
		`{"api_version":"v2","event_id":"` + payhotTestEvent + `","event_type":"recurring_charge.skipped","data":{"object":"recurring_charge","id":"` + payhotTestCharge + `","status":"cancelled"}}`,
	} {
		body := []byte(payload)
		event, err := provider.VerifyWebhook(payhotHeaders(body, payhotTestSecret, 0), body)
		if err != nil {
			t.Fatal(err)
		}
		if event.ProviderReference != "recurring:"+payhotTestCharge || event.MerchantReference != "" {
			t.Fatalf("a recurring event must point at its charge: %#v", event)
		}
	}
}

func TestPayHotChargeSavedIsIdempotent(t *testing.T) {
	var keys, references []string
	provider := payhotAt(t, func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/mandates/"+payhotTestMandate:
			_, _ = io.WriteString(writer, `{"mandate":{"id":"`+payhotTestMandate+`","status":"active","currency":"RUB","payment_method":"sbp","merchant_mandate_reference":"mandate-abc"}}`)
		case request.Method == http.MethodPost && request.URL.Path == "/recurring-charges":
			var body map[string]string
			_ = json.NewDecoder(request.Body).Decode(&body)
			keys = append(keys, request.Header.Get("Idempotency-Key"))
			references = append(references, body["merchant_recurring_charge_reference"])
			if body["mandate_id"] != payhotTestMandate || body["amount_minor"] != "12345" || body["currency"] != "RUB" || body["payment_method"] != "sbp" {
				t.Errorf("unexpected charge body %#v", body)
			}
			writer.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(writer, `{"recurring_charge":{"id":"`+payhotTestCharge+`","status":"processing","amount_minor":"12345","currency":"RUB","mandate_id":"`+payhotTestMandate+`","payment_method":"sbp","merchant_recurring_charge_reference":"`+body["merchant_recurring_charge_reference"]+`"}}`)
		case request.Method == http.MethodGet && request.URL.Path == "/recurring-charges/"+payhotTestCharge:
			_, _ = io.WriteString(writer, `{"recurring_charge":{"id":"`+payhotTestCharge+`","status":"succeeded","amount_minor":"12345","currency":"RUB","mandate_id":"`+payhotTestMandate+`","payment_method":"sbp"}}`)
		default:
			t.Errorf("unexpected request %s %s", request.Method, request.URL.Path)
		}
	})
	request := SavedChargeRequest{OrderID: payhotTestOrder, IdempotencyKey: "renewal:cycle-1:1", MethodToken: payhotTestMandate, Amount: commerce.Money{Amount: 12345, Currency: "RUB"}}
	intent, err := provider.ChargeSaved(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if intent.ProviderReference != "recurring:"+payhotTestCharge || intent.Status != "succeeded" || intent.Amount.Amount != 12345 {
		t.Fatalf("unexpected intent %#v", intent)
	}
	if _, err := provider.ChargeSaved(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys[0] != keys[1] || references[0] != references[1] {
		t.Fatalf("a retried charge must repeat the same key and reference: %v %v", keys, references)
	}
	reference := regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]{0,31}[-_:][A-Za-z0-9][A-Za-z0-9._:-]{0,94}$`)
	if !reference.MatchString(references[0]) || regexp.MustCompile(`[0-9]`).MatchString(references[0]) || regexp.MustCompile(`[A-Za-z0-9]{20}`).MatchString(references[0]) {
		t.Fatalf("the charge reference must satisfy HOT Pay's opaque-reference rule: %q", references[0])
	}

	polled, err := provider.Poll(t.Context(), "recurring:"+payhotTestCharge)
	if err != nil || polled.Status != "succeeded" || polled.ProviderReference != "recurring:"+payhotTestCharge {
		t.Fatalf("a recurring reference must poll the charge: %#v %v", polled, err)
	}

	revoked := payhotAt(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPost {
			t.Error("a revoked mandate must not be charged")
		}
		_, _ = io.WriteString(writer, `{"mandate":{"id":"`+payhotTestMandate+`","status":"revoked","currency":"RUB","payment_method":"card"}}`)
	})
	if _, err := revoked.ChargeSaved(t.Context(), request); err == nil {
		t.Fatal("a revoked mandate must not be charged")
	}
	if _, err := revoked.ChargeSaved(t.Context(), SavedChargeRequest{IdempotencyKey: "k", MethodToken: "4111111111111111", Amount: commerce.Money{Amount: 1, Currency: "RUB"}}); err == nil {
		t.Fatal("a token that is not a mandate ID must not be sent")
	}
}

func TestPayHotRefundIsUnsupported(t *testing.T) {
	provider := payhotAt(t, func(http.ResponseWriter, *http.Request) { t.Error("the deprecated refund route must not be called") })
	if _, err := provider.Refund(t.Context(), RefundRequest{PaymentReference: payhotTestPayment, IdempotencyKey: "refund-key-000001"}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("expected unsupported refund, got %v", err)
	}
}
