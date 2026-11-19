package payments

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/commerce"
)

const wataTestLinkID = "123e4567-e89b-12d3-a456-426614174000"

type wataTestServer struct {
	key       *rsa.PrivateKey
	keyCalls  atomic.Int32
	keyStatus atomic.Int32
	handler   http.HandlerFunc
}

func wataTestProvider(t *testing.T, extra map[string]string, handler http.HandlerFunc) (*Wata, *wataTestServer) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	fake := &wataTestServer{key: key, handler: handler}
	fake.keyStatus.Store(http.StatusOK)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/public-key" {
			fake.keyCalls.Add(1)
			if status := int(fake.keyStatus.Load()); status != http.StatusOK {
				writer.WriteHeader(status)
				return
			}
			der, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
			value := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
			_ = json.NewEncoder(writer).Encode(map[string]string{"value": value})
			return
		}
		if fake.handler == nil {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		fake.handler(writer, request)
	}))
	t.Cleanup(server.Close)
	values := map[string]string{"APP_WATA_ACCESS_TOKEN": "wata-token", "APP_WATA_BASE_URL": server.URL}
	for key, value := range extra {
		values[key] = value
	}
	built, err := buildWata(freekassaTestEnv(values))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	provider := built.(*Wata)
	provider.clock = func() time.Time { return time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC) }
	return provider, fake
}

func (fake *wataTestServer) sign(body string) http.Header {
	digest := sha512.Sum512([]byte(body))
	signature, _ := rsa.SignPKCS1v15(rand.Reader, fake.key, crypto.SHA512, digest[:])
	headers := http.Header{}
	headers.Set("X-Signature", base64.StdEncoding.EncodeToString(signature))
	return headers
}

func TestWataBuilderReadsItsEnvironment(t *testing.T) {
	if _, err := buildWata(freekassaTestEnv(map[string]string{})); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("no credentials must leave the adapter unregistered, got %v", err)
	}
	for _, values := range []map[string]string{
		{"APP_WATA_ACCESS_TOKEN": "t", "APP_WATA_BASE_URL": "not a url"},
		{"APP_WATA_ACCESS_TOKEN": "t", "APP_WATA_LINK_TTL_MINUTES": "-5"},
		{"APP_WATA_ACCESS_TOKEN": "t", "APP_WATA_REQUEST_TIMEOUT": "0"},
	} {
		if _, err := buildWata(freekassaTestEnv(values)); err == nil {
			t.Fatalf("%v must be rejected", values)
		}
	}
	built, err := buildWata(freekassaTestEnv(map[string]string{"APP_WATA_ACCESS_TOKEN": "t"}))
	if err != nil {
		t.Fatal(err)
	}
	provider := built.(*Wata)
	if provider.baseURL != "https://api.wata.pro/api/h2h" || provider.publicKeyURL != "https://api.wata.pro/api/h2h/public-key" ||
		provider.paymentType != "OneTime" || provider.Name() != "wata" {
		t.Fatalf("unexpected defaults %#v", provider)
	}
	if !provider.SupportsCurrency("RUB") || provider.SupportsCurrency("USD") {
		t.Fatal("WATA links are RUB only")
	}
	if caps := provider.Capabilities(); caps.Refunds || caps.Recurring || !caps.Webhooks || !caps.Polling {
		t.Fatalf("unexpected capabilities %#v", caps)
	}
}

func TestWataCreateAndPoll(t *testing.T) {
	var body map[string]any
	linkStatus := "Opened"
	transactions := `{"items":[{"id":"tx-001","status":"Paid","amount":1499.00,"currency":"RUB"}],"totalCount":1}`
	provider, _ := wataTestProvider(t, map[string]string{"APP_WATA_LINK_TTL_MINUTES": "5", "APP_WATA_FAIL_REDIRECT_URL": "https://shop.example.com/failed"}, func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer wata-token" {
			t.Errorf("missing bearer token")
		}
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/links":
			_ = json.NewDecoder(request.Body).Decode(&body)
			_, _ = writer.Write([]byte(`{"id":"` + wataTestLinkID + `","url":"https://payment.wata.pro/pay/abc","status":"Opened","expirationDateTime":"2026-11-19T12:15:00Z"}`))
		case request.Method == http.MethodGet && request.URL.Path == "/links/"+wataTestLinkID:
			_, _ = writer.Write([]byte(`{"id":"` + wataTestLinkID + `","status":"` + linkStatus + `","orderId":"` + freekassaTestOrder + `","amount":1499.0}`))
		case request.Method == http.MethodGet && request.URL.Path == "/transactions":
			query := request.URL.Query()
			if query.Get("maxResultCount") == "1" {
				_, _ = writer.Write([]byte(`{"items":[],"totalCount":0}`))
				return
			}
			if query.Get("paymentLinkId") != wataTestLinkID || query.Get("orderId") != freekassaTestOrder || query.Get("statuses") != "Paid" {
				t.Errorf("unexpected transaction search %v", query)
			}
			_, _ = writer.Write([]byte(transactions))
		default:
			t.Errorf("unexpected request %s %s", request.Method, request.URL.Path)
			writer.WriteHeader(http.StatusNotFound)
		}
	})
	intent, err := provider.Create(t.Context(), CreateRequest{
		OrderID: freekassaTestOrder, Amount: commerce.Money{Amount: 149900, Currency: "RUB"},
		Description: "Subscription", ReturnURL: "https://shop.example.com/account/orders/1",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	want := map[string]any{
		"amount": 1499.0, "currency": "RUB", "description": "Subscription", "orderId": freekassaTestOrder, "type": "OneTime",
		"successRedirectUrl": "https://shop.example.com/account/orders/1", "failRedirectUrl": "https://shop.example.com/failed",
		// A five-minute TTL is raised to fifteen, past WATA's ten-minute floor.
		"expirationDateTime": "2026-11-19T12:15:00Z",
	}
	for key, value := range want {
		if body[key] != value {
			t.Fatalf("%s: want %v, got %v", key, value, body[key])
		}
	}
	if intent.ProviderReference != wataTestLinkID || intent.Status != "pending" || intent.CheckoutURL != "https://payment.wata.pro/pay/abc" || intent.ExpiresAt == nil {
		t.Fatalf("unexpected intent %#v", intent)
	}
	if polled, err := provider.Poll(t.Context(), wataTestLinkID); err != nil || polled.Status != "pending" {
		t.Fatalf("an open link is pending: %#v %v", polled, err)
	}
	linkStatus = "Closed"
	polled, err := provider.Poll(t.Context(), wataTestLinkID)
	if err != nil || polled.Status != "succeeded" || polled.Amount != (commerce.Money{Amount: 149900, Currency: "RUB"}) {
		t.Fatalf("a closed link with a paid transaction settles: %#v %v", polled, err)
	}
	transactions = `{"items":[{"id":"tx-002","status":"Declined","amount":1499.00}]}`
	if polled, err := provider.Poll(t.Context(), wataTestLinkID); err != nil || polled.Status != "pending" {
		t.Fatalf("a closed link without a paid transaction does not settle: %#v %v", polled, err)
	}
	if err := provider.Probe(t.Context()); err != nil {
		t.Fatalf("probe: %v", err)
	}
}

func TestWataWebhookVerifiesTheRSASignature(t *testing.T) {
	provider, fake := wataTestProvider(t, nil, nil)
	// Bedolaga's own test payload: the signature verifies, and the body is
	// then refused only because it is not a transaction notification.
	if _, err := provider.VerifyWebhook(fake.sign(`{"status": "Paid"}`), []byte(`{"status": "Paid"}`)); !errors.Is(err, ErrProviderResponse) {
		t.Fatalf("the Bedolaga vector must pass the signature check, got %v", err)
	}
	body := `{"transactionType":"CardCrypto","transactionId":"tx-001","transactionStatus":"Paid","amount":1499.00,"currency":"RUB","orderId":"` + freekassaTestOrder + `","paymentLinkId":"` + wataTestLinkID + `","paymentTime":"2026-11-19T12:03:00Z"}`
	headers := fake.sign(body)
	event, err := provider.VerifyWebhook(headers, []byte(body))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if event.ID != "Paid:tx-001" || event.Status != "succeeded" || event.ProviderReference != wataTestLinkID ||
		event.MerchantReference != freekassaTestOrder || event.Amount != (commerce.Money{Amount: 149900, Currency: "RUB"}) {
		t.Fatalf("unexpected event %#v", event)
	}
	repeat, err := provider.VerifyWebhook(headers, []byte(body))
	if err != nil || repeat.ID != event.ID {
		t.Fatalf("a redelivery must keep its event ID: %v", err)
	}
	if calls := fake.keyCalls.Load(); calls != 1 {
		t.Fatalf("the public key must be cached, fetched %d times", calls)
	}
	if _, err := provider.VerifyWebhook(headers, []byte(strings.Replace(body, "1499.00", "14.99", 1))); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("a tampered amount must be rejected, got %v", err)
	}
	if _, err := provider.VerifyWebhook(http.Header{"X-Signature": {"bm90LWEtc2lnbmF0dXJl"}}, []byte(body)); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("a bad signature must be rejected, got %v", err)
	}
	if _, err := provider.VerifyWebhook(http.Header{}, []byte(body)); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("a missing signature must be rejected, got %v", err)
	}
	declined := strings.Replace(strings.Replace(body, `"Paid"`, `"Declined"`, 1), "tx-001", "tx-002", 1)
	if event, err := provider.VerifyWebhook(fake.sign(declined), []byte(declined)); err != nil || event.Status != "failed" {
		t.Fatalf("a declined transaction fails: %#v %v", event, err)
	}
	unknown := strings.Replace(body, `"Paid"`, `"Refunded"`, 1)
	if event, err := provider.VerifyWebhook(fake.sign(unknown), []byte(unknown)); err != nil || event.Status != "pending" {
		t.Fatalf("an unknown status must not settle: %#v %v", event, err)
	}
	if status, _, ack := provider.WebhookAck("paid"); status != http.StatusOK || string(ack) != `{"status":"ok"}` {
		t.Fatalf("unexpected ack %d %s", status, ack)
	}
}

func TestWataPublicKeyOutageKeepsTheCachedKey(t *testing.T) {
	provider, fake := wataTestProvider(t, map[string]string{"APP_WATA_PUBLIC_KEY_CACHE_SECONDS": "1"}, nil)
	body := `{"transactionId":"tx-1","transactionStatus":"Paid","amount":"10.00","orderId":"` + freekassaTestOrder + `"}`
	if _, err := provider.VerifyWebhook(fake.sign(body), []byte(body)); err != nil {
		t.Fatalf("verify: %v", err)
	}
	fake.keyStatus.Store(http.StatusServiceUnavailable)
	provider.clock = func() time.Time { return time.Date(2026, 11, 19, 13, 0, 0, 0, time.UTC) }
	if _, err := provider.VerifyWebhook(fake.sign(body), []byte(body)); err != nil {
		t.Fatalf("a stale key must be used while the endpoint is down: %v", err)
	}
	cold, coldFake := wataTestProvider(t, nil, nil)
	coldFake.keyStatus.Store(http.StatusServiceUnavailable)
	if _, err := cold.VerifyWebhook(coldFake.sign(body), []byte(body)); err == nil {
		t.Fatal("without any key nothing can be verified")
	}
}

func TestWataErrorsSeparateRefusalsFromOutagesAndRefundsAreUnsupported(t *testing.T) {
	status := http.StatusUnauthorized
	provider, _ := wataTestProvider(t, nil, func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(status)
	})
	if err := provider.Probe(t.Context()); !Refused(err) {
		t.Fatalf("a 4xx is a refusal, got %v", err)
	}
	status = http.StatusTooManyRequests
	if _, err := provider.Poll(t.Context(), wataTestLinkID); err == nil || Refused(err) {
		t.Fatalf("a rate limit is not a refusal, got %v", err)
	}
	status = http.StatusInternalServerError
	if _, err := provider.Poll(t.Context(), wataTestLinkID); err == nil || Refused(err) {
		t.Fatalf("a 5xx is not a refusal, got %v", err)
	}
	if _, err := provider.Refund(t.Context(), RefundRequest{}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("refunds are unsupported, got %v", err)
	}
}
