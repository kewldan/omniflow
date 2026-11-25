package nervixy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/goods"
)

type fakeNervixy struct {
	t  *testing.T
	mu sync.Mutex

	validLogin  bool
	checkDrop   bool
	orderStatus int
	orderBody   string
	orderDrop   bool
	orderStates map[string]string
	orders      []map[string]any
	checks      int
}

func newFake(t *testing.T) (*fakeNervixy, *Provider) {
	t.Helper()
	fake := &fakeNervixy{t: t, validLogin: true, orderStates: map[string]string{}}
	server := httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(server.Close)
	provider, err := New(Config{
		BaseURL: server.URL + "/api.php", APIKey: "key", HTTP: server.Client(),
		Clock: func() time.Time { return time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC) },
	}, func(_ context.Context, currency string) (int64, error) {
		if currency != "RUB" {
			return 0, errors.New("no rate")
		}
		return 8_000, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return fake, provider
}

func (fake *fakeNervixy) drop(writer http.ResponseWriter) {
	connection, _, err := writer.(http.Hijacker).Hijack()
	if err != nil {
		fake.t.Fatal(err)
	}
	_ = connection.Close()
}

func (fake *fakeNervixy) serve(writer http.ResponseWriter, request *http.Request) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if request.Header.Get("X-API-Key") != "key" {
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write([]byte(`{"ok":false,"error":"bad key"}`))
		return
	}
	var body map[string]any
	_ = json.NewDecoder(request.Body).Decode(&body)
	switch request.URL.Query().Get("action") {
	case "api_check_login":
		fake.checks++
		if fake.checkDrop {
			fake.drop(writer)
			return
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{"ok": true, "steam_login": body["steam_login"], "valid": fake.validLogin})
	case "api_order":
		fake.orders = append(fake.orders, body)
		if fake.orderDrop {
			fake.drop(writer)
			return
		}
		if fake.orderStatus != 0 {
			writer.WriteHeader(fake.orderStatus)
			_, _ = writer.Write([]byte(fake.orderBody))
			return
		}
		id := "nx-" + body["partner_id"].(string)
		fake.orderStates[id] = statusProcessing
		_ = json.NewEncoder(writer).Encode(map[string]any{"ok": true, "order_id": id, "currency": "USD"})
	case "api_status":
		state, ok := fake.orderStates[body["order_id"].(string)]
		if !ok {
			writer.WriteHeader(http.StatusNotFound)
			_, _ = writer.Write([]byte(`{"ok":false,"error":"not found"}`))
			return
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{"ok": true, "order": body["order_id"], "status": state})
	case "api_me":
		_, _ = writer.Write([]byte(`{"ok":true,"login":"op","balance":42.5,"discount":2.00}`))
	default:
		writer.WriteHeader(http.StatusNotFound)
	}
}

func request(dollars int) goods.DeliveryRequest {
	return goods.DeliveryRequest{
		Request: goods.Request{Kind: goods.KindSteamTopUp, StarQuantity: dollars, Recipient: "gaben_01", Currency: "RUB"},
		OrderID: "11111111-2222-3333-4444-555555555555",
	}
}

func TestQuoteConvertsDollars(t *testing.T) {
	t.Parallel()
	_, provider := newFake(t)
	quote, err := provider.Quote(context.Background(), goods.Request{Kind: goods.KindSteamTopUp, StarQuantity: 10, Currency: "RUB"})
	if err != nil || quote.CostMinor != 80_000 || quote.Currency != "RUB" {
		t.Fatalf("quote = %+v, %v", quote, err)
	}
	if _, err := provider.Quote(context.Background(), goods.Request{Kind: goods.KindSteamTopUp, StarQuantity: 10, Currency: "EUR"}); !errors.Is(err, goods.ErrRateUnavailable) {
		t.Fatalf("EUR: %v", err)
	}
}

func TestDeliverMarksThenOrdersAndPollResolves(t *testing.T) {
	t.Parallel()
	fake, provider := newFake(t)
	var marker goods.SettlementMarker
	delivery := request(10)
	delivery.BeforeSettle = func(_ context.Context, settlement goods.SettlementMarker) error {
		if len(fake.orders) != 0 {
			t.Fatal("ordered before the marker")
		}
		marker = settlement
		return nil
	}
	got, err := provider.Deliver(context.Background(), delivery)
	if err != nil || got.Status != "submitted" || got.Reference == "" {
		t.Fatalf("delivery = %+v, %v", got, err)
	}
	placed := fake.orders[0]
	if placed["currency"] != "USD" || placed["amount"].(float64) != 10 || placed["steam_login"] != "gaben_01" ||
		placed["partner_id"] != marker.ProviderRequestID {
		t.Fatalf("order = %v, marker = %+v", placed, marker)
	}
	if strings.ContainsAny(marker.ProviderRequestID, "-_") || marker.CostNano != 1_000 {
		t.Fatalf("marker = %+v", marker)
	}

	polled, _ := provider.Poll(context.Background(), got.Reference)
	if polled.Status != "submitted" {
		t.Fatalf("processing polled as %+v", polled)
	}
	fake.orderStates[got.Reference] = statusDelivered
	if polled, _ = provider.Poll(context.Background(), got.Reference); polled.Status != "delivered" {
		t.Fatalf("delivered polled as %+v", polled)
	}
	fake.orderStates[got.Reference] = statusRejected
	if polled, _ = provider.Poll(context.Background(), got.Reference); polled.Status != "failed" || polled.FailureClass != goods.FailurePermanent {
		t.Fatalf("rejected polled as %+v", polled)
	}
	if polled, _ = provider.Poll(context.Background(), "unknown"); polled.Status != "submitted" {
		t.Fatalf("an unreadable order is not given up on: %+v", polled)
	}
	if len(fake.orders) != 1 {
		t.Fatalf("polling placed %d orders", len(fake.orders))
	}
}

func TestAnUnknownLoginIsRefusedBeforeTheMarker(t *testing.T) {
	t.Parallel()
	fake, provider := newFake(t)
	fake.validLogin = false
	delivery := request(5)
	delivery.BeforeSettle = func(context.Context, goods.SettlementMarker) error {
		t.Fatal("marked an order for an unknown login")
		return nil
	}
	got, _ := provider.Deliver(context.Background(), delivery)
	if got.FailureClass != goods.FailureRecipientInvalid || len(fake.orders) != 0 {
		t.Fatalf("delivery = %+v, orders = %d", got, len(fake.orders))
	}
	if err := provider.ValidateRecipient(context.Background(), "gaben_01"); !errors.Is(err, goods.ErrRecipientUnknown) {
		t.Fatalf("validate = %v", err)
	}
}

func TestAnUnreachableLoginCheckRetriesWithoutOrdering(t *testing.T) {
	t.Parallel()
	fake, provider := newFake(t)
	fake.checkDrop = true
	got, _ := provider.Deliver(context.Background(), request(5))
	if got.FailureClass != goods.FailureProviderUnavailable || len(fake.orders) != 0 {
		t.Fatalf("delivery = %+v, orders = %d", got, len(fake.orders))
	}
}

func TestOrderRefusalsAreClassified(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		status     int
		body       string
		drop       bool
		class      string
		notCharged bool
	}{
		{"lost answer", 0, "", true, goods.FailureAmbiguous, false},
		{"server error", 500, `{"ok":false,"error":"oops"}`, false, goods.FailureAmbiguous, false},
		{"supplier rejected", 502, `{"ok":false,"error":"rejected"}`, false, goods.FailureAmbiguous, false},
		{"unreadable success", 200, `not json`, false, goods.FailureAmbiguous, false},
		{"insufficient", 400, `{"ok":false,"error":"Недостаточно средств","balance":1,"required":6.29}`, false, goods.FailureProviderBalance, true},
		{"bad login", 400, `{"ok":false,"error":"Invalid steam_login"}`, false, goods.FailureRecipientInvalid, true},
		{"ip not allowed", 401, `{"ok":false,"error":"ip"}`, false, goods.FailureProviderUnavailable, true},
		{"rate limited", 429, `{"ok":false,"error":"slow down"}`, false, goods.FailureRetryable, true},
		{"other refusal", 400, `{"ok":false,"error":"amount out of range"}`, false, goods.FailurePermanent, true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			fake, provider := newFake(t)
			fake.orderStatus, fake.orderBody, fake.orderDrop = testCase.status, testCase.body, testCase.drop
			got, err := provider.Deliver(context.Background(), request(5))
			if err != nil || got.Status != "failed" || got.FailureClass != testCase.class || got.NotCharged != testCase.notCharged {
				t.Fatalf("delivery = %+v, %v", got, err)
			}
		})
	}
}

func TestBalanceIsInCents(t *testing.T) {
	t.Parallel()
	_, provider := newFake(t)
	cents, currency, err := provider.Balance(context.Background())
	if err != nil || cents != 4_250 || currency != "USD" {
		t.Fatalf("balance = %d %s, %v", cents, currency, err)
	}
}
