package playwallet

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/goods"
)

// fakeMerchant is an in-memory PlayWallet merchant API.
type fakeMerchant struct {
	t *testing.T

	mu       sync.Mutex
	feeRatio string
	services string
	orders   map[string]*order
	creates  int
	pays     []string
	tokens   []string

	// Knobs for failure paths.
	createStatus   int
	createMessage  string
	payStatus      int
	payMessage     string
	payDrop        bool
	payDoesNothing bool
	getDrop        bool
	payResult      string
}

func newMerchant(t *testing.T) (*fakeMerchant, *httptest.Server) {
	t.Helper()
	merchant := &fakeMerchant{
		t: t, feeRatio: "0.02", orders: map[string]*order{}, payResult: statusQueued,
		services: `[{"id":"svc-steam","name":"Steam","minAmount":"1.00","maxAmount":"200.00"}]`,
	}
	server := httptest.NewServer(http.HandlerFunc(merchant.serve))
	t.Cleanup(server.Close)
	return merchant, server
}

func (merchant *fakeMerchant) reply(writer http.ResponseWriter, status int, message string, data any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	envelopeStatus := "success"
	if status != http.StatusOK {
		envelopeStatus = "error"
	}
	encoded, _ := json.Marshal(data)
	_ = json.NewEncoder(writer).Encode(map[string]any{
		"status": envelopeStatus, "message": message, "data": json.RawMessage(encoded),
	})
}

func (merchant *fakeMerchant) drop(writer http.ResponseWriter) {
	hijacker, ok := writer.(http.Hijacker)
	if !ok {
		merchant.t.Fatal("cannot drop the connection")
	}
	connection, _, err := hijacker.Hijack()
	if err != nil {
		merchant.t.Fatal(err)
	}
	_ = connection.Close()
}

func (merchant *fakeMerchant) serve(writer http.ResponseWriter, request *http.Request) {
	if request.Header.Get("pw-api-key") != "test-key" {
		merchant.reply(writer, http.StatusUnauthorized, "bad key", nil)
		return
	}
	merchant.mu.Lock()
	defer merchant.mu.Unlock()
	switch {
	case request.Method == http.MethodGet && request.URL.Path == "/api/merchant/get-balance":
		merchant.reply(writer, http.StatusOK, "", json.RawMessage(`{"balance":"500.00","frozenBalance":"0.00","feeRatio":"`+
			merchant.feeRatio+`","services":`+merchant.services+`}`))
	case request.Method == http.MethodPost && request.URL.Path == "/api/merchant/create-order/":
		merchant.creates++
		if merchant.createStatus != 0 {
			merchant.reply(writer, merchant.createStatus, merchant.createMessage, nil)
			return
		}
		var body map[string]string
		_ = json.NewDecoder(request.Body).Decode(&body)
		id := "order-" + string(rune('a'+merchant.creates-1))
		created := &order{
			ID: id, Status: statusActive, ExternalID: body["externalId"], ServiceID: body["serviceId"],
			Amount: body["amount"], CreatedDateTime: "2026-11-25T10:00:00.123456",
		}
		merchant.orders[id] = created
		merchant.reply(writer, http.StatusOK, "", created)
	case request.Method == http.MethodPost && request.URL.Path == "/api/merchant/pay-order/":
		var body map[string]string
		_ = json.NewDecoder(request.Body).Decode(&body)
		merchant.pays = append(merchant.pays, body["id"])
		merchant.tokens = append(merchant.tokens, body["token"])
		target := merchant.orders[body["id"]]
		if merchant.payDrop {
			if target != nil && !merchant.payDoesNothing {
				target.Status = merchant.payResult
			}
			merchant.drop(writer)
			return
		}
		if merchant.payStatus != 0 {
			merchant.reply(writer, merchant.payStatus, merchant.payMessage, nil)
			return
		}
		if target == nil || body["token"] != Token(target.ID, target.CreatedDateTime) {
			merchant.reply(writer, http.StatusBadRequest, "bad token", nil)
			return
		}
		target.Status = merchant.payResult
		merchant.reply(writer, http.StatusOK, "", target)
	case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/api/merchant/get-order/"):
		if merchant.getDrop {
			merchant.drop(writer)
			return
		}
		target := merchant.orders[strings.TrimPrefix(request.URL.Path, "/api/merchant/get-order/")]
		if target == nil {
			merchant.reply(writer, http.StatusNotFound, "not found", nil)
			return
		}
		merchant.reply(writer, http.StatusOK, "", target)
	default:
		merchant.reply(writer, http.StatusNotFound, "no route", nil)
	}
}

func newProvider(t *testing.T, server *httptest.Server, rates RateFunc) *Provider {
	t.Helper()
	provider, err := New(Config{
		BaseURL: server.URL + "/api/merchant/", APIKey: "test-key", HTTP: server.Client(),
		Clock: func() time.Time { return time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC) },
	}, rates)
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func steamRequest(dollars int) goods.DeliveryRequest {
	return goods.DeliveryRequest{
		Request: goods.Request{Kind: goods.KindSteamTopUp, StarQuantity: dollars, Quantity: 1,
			Recipient: "gaben_01", Currency: "RUB"},
		OrderID: "11111111-2222-3333-4444-555555555555", IdempotencyKey: "goods:11111111-2222-3333-4444-555555555555",
	}
}

func TestTokenMatchesThePublishedExample(t *testing.T) {
	t.Parallel()
	got := Token("72cf5503-ae6a-45af-97b8-aae3a0731687", "2024-04-18T17:36:55.453116")
	want := "db8e5992e47c48b46f59ecdd1789cc525f4061a52fbf8200dbdab4bc0929de9cf2f61bd8725314440c498a694176b06a2faec22776be4dc8123fffee14b027a5"
	if got != want {
		t.Fatalf("token = %s", got)
	}
}

func TestQuoteAddsTheFeeAndConvertsRoundingUp(t *testing.T) {
	t.Parallel()
	_, server := newMerchant(t)
	provider := newProvider(t, server, func(_ context.Context, currency string) (int64, error) {
		if currency != "RUB" {
			t.Fatalf("asked for %s", currency)
		}
		return 9_001, nil // 90.01 ₽ per dollar
	})
	quote, err := provider.Quote(context.Background(), goods.Request{
		Kind: goods.KindSteamTopUp, StarQuantity: 10, Currency: "RUB",
	})
	if err != nil {
		t.Fatal(err)
	}
	// $10 + 2% = 1020 cents; 1020 × 9001 / 100 = 91810.2 → 91811 kopecks.
	if quote.CostMinor != 91_811 || quote.Currency != "RUB" {
		t.Fatalf("quote = %+v", quote)
	}
	dollars, err := provider.Quote(context.Background(), goods.Request{
		Kind: goods.KindSteamTopUp, StarQuantity: 10, Currency: "USD",
	})
	if err != nil || dollars.CostMinor != 1_020 {
		t.Fatalf("dollar quote = %+v, %v", dollars, err)
	}
}

func TestQuoteRefusesWhatItCannotPrice(t *testing.T) {
	t.Parallel()
	_, server := newMerchant(t)
	provider := newProvider(t, server, nil)
	ctx := context.Background()
	if _, err := provider.Quote(ctx, goods.Request{Kind: goods.KindSteamTopUp, StarQuantity: 500, Currency: "USD"}); !errors.Is(err, ErrAmountOutOfRange) {
		t.Fatalf("over the maximum: %v", err)
	}
	if _, err := provider.Quote(ctx, goods.Request{Kind: goods.KindSteamTopUp, StarQuantity: 5, Currency: "RUB"}); !errors.Is(err, ErrRateUnavailable) {
		t.Fatalf("no rate: %v", err)
	}
	if _, err := provider.Quote(ctx, goods.Request{Kind: goods.KindTelegramStars, StarQuantity: 50}); !errors.Is(err, goods.ErrUnsupported) {
		t.Fatalf("Stars: %v", err)
	}
}

func TestServiceNeedsToBeIdentifiable(t *testing.T) {
	t.Parallel()
	merchant, server := newMerchant(t)
	merchant.services = `[{"id":"a","name":"Gift cards"},{"id":"b","name":"Mobile"}]`
	provider := newProvider(t, server, nil)
	if _, err := provider.Quote(context.Background(), goods.Request{Kind: goods.KindSteamTopUp, StarQuantity: 5, Currency: "USD"}); !errors.Is(err, ErrServiceUnknown) {
		t.Fatalf("ambiguous services: %v", err)
	}
	merchant.services = `[{"id":"a","name":"Gift cards"},{"id":"b","name":"STEAM wallet"}]`
	provider = newProvider(t, server, nil)
	if _, err := provider.Quote(context.Background(), goods.Request{Kind: goods.KindSteamTopUp, StarQuantity: 5, Currency: "USD"}); err != nil {
		t.Fatalf("the Steam service is named: %v", err)
	}
}

func TestDeliverMarksThenPaysAndPollCompletes(t *testing.T) {
	t.Parallel()
	merchant, server := newMerchant(t)
	provider := newProvider(t, server, nil)
	var marker goods.SettlementMarker
	request := steamRequest(10)
	request.BeforeSettle = func(_ context.Context, settlement goods.SettlementMarker) error {
		if len(merchant.pays) != 0 {
			t.Fatal("paid before the marker was committed")
		}
		marker = settlement
		return nil
	}
	delivery, err := provider.Deliver(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if delivery.Status != "submitted" || delivery.Reference != "order-a" || delivery.FailureClass != "" {
		t.Fatalf("delivery = %+v", delivery)
	}
	if marker.ProviderRequestID != "order-a" || marker.Asset != "USD" || marker.CostNano != 1_020 {
		t.Fatalf("marker = %+v", marker)
	}
	created := merchant.orders["order-a"]
	if created.Amount != "10.00" || created.ServiceID != "svc-steam" ||
		!strings.HasPrefix(created.ExternalID, request.OrderID+"-") {
		t.Fatalf("created = %+v", created)
	}
	if merchant.tokens[0] != Token("order-a", created.CreatedDateTime) {
		t.Fatal("pay-order was not signed with the order's token")
	}

	created.Status = statusCompleted
	polled, err := provider.Poll(context.Background(), "order-a")
	if err != nil || polled.Status != "delivered" {
		t.Fatalf("poll = %+v, %v", polled, err)
	}
	if merchant.creates != 1 || len(merchant.pays) != 1 {
		t.Fatalf("creates = %d, pays = %d", merchant.creates, len(merchant.pays))
	}
}

func TestDeliverRefusalsBeforePaymentNeverPay(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		status  int
		message string
		class   string
	}{
		{"unknown login", http.StatusBadRequest, "Steam login not found", goods.FailureRecipientInvalid},
		{"empty balance", http.StatusBadRequest, "Insufficient balance", goods.FailureProviderBalance},
		{"outage", http.StatusBadGateway, "", goods.FailureProviderUnavailable},
		{"allow-list", http.StatusForbidden, "ip", goods.FailureProviderUnavailable},
		{"other refusal", http.StatusBadRequest, "something else", goods.FailurePermanent},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			merchant, server := newMerchant(t)
			merchant.createStatus, merchant.createMessage = testCase.status, testCase.message
			provider := newProvider(t, server, nil)
			request := steamRequest(5)
			request.BeforeSettle = func(context.Context, goods.SettlementMarker) error {
				t.Fatal("marked an order that was never opened")
				return nil
			}
			delivery, err := provider.Deliver(context.Background(), request)
			if err != nil || delivery.Status != "failed" || delivery.FailureClass != testCase.class {
				t.Fatalf("delivery = %+v, %v", delivery, err)
			}
			if strings.Contains(delivery.ErrorCode, "gaben") || strings.Contains(delivery.ErrorCode, "login") {
				t.Fatalf("error code carries provider text: %q", delivery.ErrorCode)
			}
			if len(merchant.pays) != 0 {
				t.Fatal("paid after a refused create-order")
			}
		})
	}
}

func TestDeliverRefusesABadLoginWithoutCallingPlayWallet(t *testing.T) {
	t.Parallel()
	merchant, server := newMerchant(t)
	provider := newProvider(t, server, nil)
	request := steamRequest(5)
	request.Recipient = "not a login"
	delivery, _ := provider.Deliver(context.Background(), request)
	if delivery.FailureClass != goods.FailureRecipientInvalid || merchant.creates != 0 {
		t.Fatalf("delivery = %+v, creates = %d", delivery, merchant.creates)
	}
}

func TestMarkerFailureLeavesTheOrderUnpaid(t *testing.T) {
	t.Parallel()
	merchant, server := newMerchant(t)
	provider := newProvider(t, server, nil)
	request := steamRequest(5)
	request.BeforeSettle = func(context.Context, goods.SettlementMarker) error {
		return errors.New("refunded by an operator")
	}
	delivery, _ := provider.Deliver(context.Background(), request)
	if delivery.Status != "failed" || delivery.FailureClass != goods.FailureRetryable || len(merchant.pays) != 0 {
		t.Fatalf("delivery = %+v, pays = %v", delivery, merchant.pays)
	}
}

func TestLostPayAnswerIsReadBack(t *testing.T) {
	t.Parallel()
	merchant, server := newMerchant(t)
	merchant.payDrop = true // PlayWallet took the payment, the answer was lost.
	provider := newProvider(t, server, nil)
	delivery, _ := provider.Deliver(context.Background(), steamRequest(5))
	if delivery.Status != "submitted" || delivery.Reference != "order-a" {
		t.Fatalf("delivery = %+v", delivery)
	}
}

func TestLostPayAndLostReadStaySubmitted(t *testing.T) {
	t.Parallel()
	merchant, server := newMerchant(t)
	merchant.payDrop, merchant.getDrop = true, true
	provider := newProvider(t, server, nil)
	delivery, _ := provider.Deliver(context.Background(), steamRequest(5))
	if delivery.Status != "submitted" || delivery.Reference != "order-a" || delivery.FailureClass != "" {
		t.Fatalf("an unknown outcome must stay attached to its order: %+v", delivery)
	}
}

func TestUnpaidOrderIsPaidAgainOnPollNeverReopened(t *testing.T) {
	t.Parallel()
	merchant, server := newMerchant(t)
	merchant.payDrop, merchant.payDoesNothing = true, true // the payment never landed
	provider := newProvider(t, server, nil)
	delivery, _ := provider.Deliver(context.Background(), steamRequest(5))
	if delivery.Status != "submitted" || delivery.FailureClass != goods.FailureProviderUnavailable {
		t.Fatalf("delivery = %+v", delivery)
	}
	merchant.payDrop, merchant.payDoesNothing = false, false
	polled, err := provider.Poll(context.Background(), delivery.Reference)
	if err != nil || polled.Status != "submitted" || polled.FailureClass != "" {
		t.Fatalf("poll = %+v, %v", polled, err)
	}
	if merchant.creates != 1 {
		t.Fatalf("a poll opened a second order: %d", merchant.creates)
	}
	if len(merchant.pays) != 2 || merchant.pays[0] != "order-a" || merchant.pays[1] != "order-a" {
		t.Fatalf("pays = %v", merchant.pays)
	}
	if merchant.orders["order-a"].Status != statusQueued {
		t.Fatal("the original order was not paid")
	}
}

func TestPayRefusals(t *testing.T) {
	t.Parallel()
	merchant, server := newMerchant(t)
	merchant.payStatus, merchant.payMessage = http.StatusBadRequest, "insufficient balance"
	provider := newProvider(t, server, nil)
	short, _ := provider.Deliver(context.Background(), steamRequest(5))
	if short.Status != "submitted" || short.FailureClass != goods.FailureProviderBalance || short.Reference == "" {
		t.Fatalf("an empty balance keeps the order to pay later: %+v", short)
	}

	merchant.payMessage = "order cannot be paid"
	refused, _ := provider.Deliver(context.Background(), steamRequest(5))
	if refused.Status != "failed" || refused.FailureClass != goods.FailurePermanent {
		t.Fatalf("a refused unpaid order is refunded: %+v", refused)
	}
}

func TestPollMapsProviderStates(t *testing.T) {
	t.Parallel()
	merchant, server := newMerchant(t)
	provider := newProvider(t, server, nil)
	merchant.orders["queued"] = &order{ID: "queued", Status: statusQueued}
	merchant.orders["failed"] = &order{ID: "failed", Status: statusError}
	merchant.orders["odd"] = &order{ID: "odd", Status: "refunding"}
	for reference, want := range map[string]goods.Delivery{
		"queued": {Reference: "queued", Status: "submitted"},
		"failed": {Reference: "failed", Status: "failed", FailureClass: goods.FailurePermanent, ErrorCode: "provider_order_error"},
		"odd":    {Reference: "odd", Status: "submitted"},
	} {
		got, err := provider.Poll(context.Background(), reference)
		if err != nil || got.Status != want.Status || got.FailureClass != want.FailureClass || got.Reference != want.Reference {
			t.Fatalf("%s: %+v, %v", reference, got, err)
		}
	}
	missing, err := provider.Poll(context.Background(), "nope")
	if err != nil || missing.Status != "submitted" {
		t.Fatalf("an unreadable order is not given up on: %+v, %v", missing, err)
	}
	if merchant.creates != 0 || len(merchant.pays) != 0 {
		t.Fatal("polling a settled order spent money")
	}
}

func TestBalanceIsInCents(t *testing.T) {
	t.Parallel()
	_, server := newMerchant(t)
	provider := newProvider(t, server, nil)
	cents, currency, err := provider.Balance(context.Background())
	if err != nil || cents != 50_000 || currency != "USD" {
		t.Fatalf("balance = %d %s, %v", cents, currency, err)
	}
}

func TestWithFeeRoundsUp(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		cents int64
		fee   string
		want  int64
	}{{100, "0.02", 102}, {100, "0", 100}, {333, "0.005", 335}, {1000, "0.0600", 1060}} {
		parsed, _ := parseRat(testCase.fee)
		if got := withFee(testCase.cents, parsed); got != testCase.want {
			t.Fatalf("withFee(%d, %s) = %d, want %d", testCase.cents, testCase.fee, got, testCase.want)
		}
	}
}

func parseRat(value string) (*big.Rat, bool) { return new(big.Rat).SetString(value) }
