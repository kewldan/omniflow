//go:build integration

package integrationtest

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

	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/goods"
	"github.com/omniflow/omniflow/internal/goods/nervixy"
	"github.com/omniflow/omniflow/internal/goods/playwallet"
	"github.com/omniflow/omniflow/internal/goodsdelivery"
)

// Steam top-ups through PlayWallet against a real database: the real adapter
// talks to an in-memory merchant API, and the delivery worker's marker and
// polling are what keep a retry from topping an account up twice.

type merchantOrder struct {
	ID              string `json:"id"`
	Status          string `json:"status"`
	ExternalID      string `json:"externalId"`
	Amount          string `json:"amount"`
	CreatedDateTime string `json:"createdDateTime"`
}

type steamMerchant struct {
	mu            sync.Mutex
	orders        map[string]*merchantOrder
	creates, pays int
	payResult     string
	createStatus  int
	createMessage string
}

func newSteamMerchant(t *testing.T) (*steamMerchant, *playwallet.Provider) {
	t.Helper()
	merchant := &steamMerchant{orders: map[string]*merchantOrder{}, payResult: "queued"}
	server := httptest.NewServer(http.HandlerFunc(merchant.serve))
	t.Cleanup(server.Close)
	provider, err := playwallet.New(playwallet.Config{
		BaseURL: server.URL, APIKey: "key", HTTP: server.Client(),
	}, nil)
	if err != nil {
		t.Fatalf("adapter: %v", err)
	}
	return merchant, provider
}

func (merchant *steamMerchant) reply(writer http.ResponseWriter, status int, message string, data any) {
	writer.WriteHeader(status)
	state := "success"
	if status != http.StatusOK {
		state = "error"
	}
	_ = json.NewEncoder(writer).Encode(map[string]any{"status": state, "message": message, "data": data})
}

func (merchant *steamMerchant) serve(writer http.ResponseWriter, request *http.Request) {
	merchant.mu.Lock()
	defer merchant.mu.Unlock()
	switch {
	case request.URL.Path == "/get-balance":
		merchant.reply(writer, http.StatusOK, "", map[string]any{
			"balance": "100.00", "feeRatio": "0.02",
			"services": []map[string]string{{"id": "svc", "name": "Steam", "minAmount": "1.00", "maxAmount": "200.00"}},
		})
	case request.URL.Path == "/create-order/":
		merchant.creates++
		if merchant.createStatus != 0 {
			merchant.reply(writer, merchant.createStatus, merchant.createMessage, nil)
			return
		}
		var body map[string]string
		_ = json.NewDecoder(request.Body).Decode(&body)
		created := &merchantOrder{
			ID:     "pw-" + body["externalId"][:8] + "-" + string(rune('0'+merchant.creates)),
			Status: "active", ExternalID: body["externalId"], Amount: body["amount"],
			CreatedDateTime: "2026-11-25T10:00:00.000001",
		}
		merchant.orders[created.ID] = created
		merchant.reply(writer, http.StatusOK, "", created)
	case request.URL.Path == "/pay-order/":
		var body map[string]string
		_ = json.NewDecoder(request.Body).Decode(&body)
		merchant.pays++
		target := merchant.orders[body["id"]]
		if target == nil || body["token"] != playwallet.Token(target.ID, target.CreatedDateTime) {
			merchant.reply(writer, http.StatusBadRequest, "bad token", nil)
			return
		}
		if target.Status == "active" {
			target.Status = merchant.payResult
		}
		merchant.reply(writer, http.StatusOK, "", target)
	case strings.HasPrefix(request.URL.Path, "/get-order/"):
		target := merchant.orders[strings.TrimPrefix(request.URL.Path, "/get-order/")]
		if target == nil {
			merchant.reply(writer, http.StatusNotFound, "not found", nil)
			return
		}
		merchant.reply(writer, http.StatusOK, "", target)
	default:
		merchant.reply(writer, http.StatusNotFound, "", nil)
	}
}

func (merchant *steamMerchant) set(id, status string) {
	merchant.mu.Lock()
	defer merchant.mu.Unlock()
	merchant.orders[id].Status = status
}

func (merchant *steamMerchant) counts() (int, int) {
	merchant.mu.Lock()
	defer merchant.mu.Unlock()
	return merchant.creates, merchant.pays
}

// steamProduct inserts a Steam top-up sold through PlayWallet at a fixed
// price per dollar.
func (harness *harness) steamProduct(ctx context.Context, t *testing.T, perDollarMinor int64) string {
	t.Helper()
	if _, err := harness.pool.Exec(ctx,
		`INSERT INTO goods_providers (slug, enabled) VALUES ('playwallet', true)
		 ON CONFLICT (slug) DO UPDATE SET enabled = true`); err != nil {
		t.Fatalf("enable provider: %v", err)
	}
	var productID string
	if err := harness.pool.QueryRow(ctx,
		`INSERT INTO goods_products (code, provider_slug, kind, min_star_quantity, max_star_quantity, star_presets, visible)
		 VALUES ('steam', 'playwallet', 'steam_topup', 1, 200, '{5,10}', true) RETURNING id::text`).
		Scan(&productID); err != nil {
		t.Fatalf("create product: %v", err)
	}
	if _, err := harness.pool.Exec(ctx,
		`INSERT INTO goods_pricing (product_id, currency, rounding, unit_price_minor)
		 VALUES ($1::uuid, 'RUB', 'none', $2)`, productID, perDollarMinor); err != nil {
		t.Fatalf("price product: %v", err)
	}
	return productID
}

func steamOrder(customerID, productID, key, login string, dollars int, perDollarMinor int64) commercepg.GoodsOrderInput {
	return commercepg.GoodsOrderInput{
		CustomerID: customerID, ProductID: productID, Quantity: 1, Recipient: login,
		PriceMinor: perDollarMinor * int64(dollars), Currency: "RUB", OperatorPriced: true,
		StarQuantity: dollars, Source: "bot", QuoteExpiresAt: time.Now().Add(time.Hour),
		IdempotencyKey: key,
	}
}

func (harness *harness) steamDelivery(ctx context.Context, t *testing.T, orderID string) (reference, requestID string, marked bool) {
	t.Helper()
	if err := harness.pool.QueryRow(ctx, `SELECT COALESCE(provider_reference, ''), COALESCE(provider_request_id, ''),
		broadcasting_at IS NOT NULL FROM goods_deliveries WHERE order_id = $1::uuid`, orderID).
		Scan(&reference, &requestID, &marked); err != nil {
		t.Fatalf("read delivery: %v", err)
	}
	return reference, requestID, marked
}

func TestASteamTopUpIsPaidOnceAndPolledToDelivery(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	customerID := harness.customer(ctx, t)
	productID := harness.steamProduct(ctx, t, 10_000)
	harness.creditWallet(ctx, t, customerID, "seed", 200_000)
	merchant, provider := newSteamMerchant(t)

	order, err := store.CreateGoodsOrder(ctx, steamOrder(customerID, productID, "steam-1", "gaben_01", 10, 10_000))
	if err != nil {
		t.Fatalf("order: %v", err)
	}
	orderID := uuidText(order.ID)

	runDelivery(ctx, t, harness, provider, orderID, goodsdelivery.Options{})
	reference, requestID, marked := harness.steamDelivery(ctx, t, orderID)
	if reference == "" || reference != requestID || !marked {
		t.Fatalf("after paying: reference %q, request %q, marked %v", reference, requestID, marked)
	}
	if delivery, status, _ := harness.deliveryState(ctx, t, orderID); delivery != "submitted" || status != "delivering" {
		t.Fatalf("delivery %s, order %s", delivery, status)
	}

	// Still queued: the next attempt asks, and neither opens nor pays again.
	runDelivery(ctx, t, harness, provider, orderID, goodsdelivery.Options{})
	if creates, pays := merchant.counts(); creates != 1 || pays != 1 {
		t.Fatalf("while queued: %d orders opened, %d paid", creates, pays)
	}

	merchant.set(reference, "completed")
	runDelivery(ctx, t, harness, provider, orderID, goodsdelivery.Options{})
	if delivery, status, _ := harness.deliveryState(ctx, t, orderID); delivery != "delivered" || status != "delivered" {
		t.Fatalf("delivery %s, order %s", delivery, status)
	}
	if creates, pays := merchant.counts(); creates != 1 || pays != 1 {
		t.Fatalf("%d orders opened, %d paid; want one of each", creates, pays)
	}
	var kind, recipient string
	var stars int
	if err := harness.pool.QueryRow(ctx, `SELECT payload->>'kind', payload->>'recipient', (payload->>'stars')::int
		FROM operator_notifications WHERE kind = 'purchase' AND dedupe_key = 'goods-sale:' || $1`, orderID).
		Scan(&kind, &recipient, &stars); err != nil {
		t.Fatalf("sale notice: %v", err)
	}
	if kind != goods.KindSteamTopUp || recipient != "gaben_01" || stars != 10 {
		t.Fatalf("sale notice = %s %s %d", kind, recipient, stars)
	}
}

// A worker that died after the marker and before recording anything leaves
// a marked row with no reference. A Steam delivery resumes from the marked
// PlayWallet order — paying that order, never opening another — instead of
// parking for review.
func TestACrashAfterTheMarkerResumesTheSameSteamOrder(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	customerID := harness.customer(ctx, t)
	productID := harness.steamProduct(ctx, t, 10_000)
	harness.creditWallet(ctx, t, customerID, "seed", 200_000)
	merchant, provider := newSteamMerchant(t)
	order, err := store.CreateGoodsOrder(ctx, steamOrder(customerID, productID, "steam-crash", "gaben_01", 5, 10_000))
	if err != nil {
		t.Fatalf("order: %v", err)
	}
	orderID := uuidText(order.ID)

	merchant.orders["pw-opened"] = &merchantOrder{ID: "pw-opened", Status: "active",
		ExternalID: orderID + "-x", Amount: "5.00", CreatedDateTime: "2026-11-25T10:00:00.5"}
	if _, err := harness.pool.Exec(ctx, `UPDATE goods_deliveries
		SET status = 'submitted', attempt_count = 1, broadcasting_at = now(), next_attempt_at = now(),
		    provider_request_id = 'pw-opened', settlement_asset = 'USD', settlement_cost_nano = 510
		WHERE order_id = $1::uuid`, orderID); err != nil {
		t.Fatalf("simulate crash: %v", err)
	}

	runDelivery(ctx, t, harness, provider, orderID, goodsdelivery.Options{})
	if creates, pays := merchant.counts(); creates != 0 || pays != 1 {
		t.Fatalf("resuming opened %d orders and paid %d times; want 0 and 1", creates, pays)
	}
	if reference, _, _ := harness.steamDelivery(ctx, t, orderID); reference != "pw-opened" {
		t.Fatalf("reference = %q", reference)
	}
	merchant.set("pw-opened", "completed")
	runDelivery(ctx, t, harness, provider, orderID, goodsdelivery.Options{})
	if delivery, _, _ := harness.deliveryState(ctx, t, orderID); delivery != "delivered" {
		t.Fatalf("delivery %s", delivery)
	}
	if creates, pays := merchant.counts(); creates != 0 || pays != 1 {
		t.Fatalf("after delivery: %d opened, %d paid", creates, pays)
	}
}

// A top-up PlayWallet accepted and never finished is parked once the
// attempts run out: refunding could pay the customer back for money that
// still reaches Steam.
func TestASteamTopUpStillRunningAfterEveryAttemptIsParkedNotRefunded(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	customerID := harness.customer(ctx, t)
	productID := harness.steamProduct(ctx, t, 10_000)
	harness.creditWallet(ctx, t, customerID, "seed", 200_000)
	_, provider := newSteamMerchant(t)
	order, err := store.CreateGoodsOrder(ctx, steamOrder(customerID, productID, "steam-slow", "gaben_01", 5, 10_000))
	if err != nil {
		t.Fatalf("order: %v", err)
	}
	orderID := uuidText(order.ID)
	for attempt := 1; attempt <= goods.MaxAttempts; attempt++ {
		runDelivery(ctx, t, harness, provider, orderID, goodsdelivery.Options{})
	}
	delivery, status, code := harness.deliveryState(ctx, t, orderID)
	if delivery != "needs_review" || status != "needs_review" || code != "provider_still_processing" {
		t.Fatalf("delivery %s, order %s, code %s", delivery, status, code)
	}
	if balance := harness.walletBalance(ctx, t, customerID); balance != 200_000-50_000 {
		t.Fatalf("a parked top-up must not be refunded: balance %d", balance)
	}
}

// A login PlayWallet refuses before anything is paid is refunded to the
// wallet, and the refund happens once.
func TestAnUnknownSteamLoginIsRefunded(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	customerID := harness.customer(ctx, t)
	productID := harness.steamProduct(ctx, t, 10_000)
	harness.creditWallet(ctx, t, customerID, "seed", 200_000)
	merchant, provider := newSteamMerchant(t)
	merchant.createStatus, merchant.createMessage = http.StatusBadRequest, "Steam login not found"
	order, err := store.CreateGoodsOrder(ctx, steamOrder(customerID, productID, "steam-bad", "nobody_here", 5, 10_000))
	if err != nil {
		t.Fatalf("order: %v", err)
	}
	orderID := uuidText(order.ID)
	runDelivery(ctx, t, harness, provider, orderID, goodsdelivery.Options{})
	runDelivery(ctx, t, harness, provider, orderID, goodsdelivery.Options{})
	if delivery, status, _ := harness.deliveryState(ctx, t, orderID); delivery != "failed" || status != "refunded" {
		t.Fatalf("delivery %s, order %s", delivery, status)
	}
	if balance := harness.walletBalance(ctx, t, customerID); balance != 200_000 {
		t.Fatalf("balance after refund = %d", balance)
	}
	if creates, pays := merchant.counts(); creates != 1 || pays != 0 {
		t.Fatalf("%d opened, %d paid", creates, pays)
	}
}

// The recipient rule follows the product: a Steam login may be three
// characters, a Telegram username may not, and neither accepts spaces.
func TestTheRecipientRuleFollowsTheProduct(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	customerID := harness.customer(ctx, t)
	steamID := harness.steamProduct(ctx, t, 10_000)
	starsID := harness.starsProduct(ctx, t, 150)
	harness.creditWallet(ctx, t, customerID, "seed", 200_000)

	if _, err := store.CreateGoodsOrder(ctx, steamOrder(customerID, steamID, "steam-abc", "abc", 1, 10_000)); err != nil {
		t.Fatalf("a three-character Steam login: %v", err)
	}
	if _, err := store.CreateGoodsOrder(ctx, steamOrder(customerID, steamID, "steam-space", "two words", 1, 10_000)); !errors.Is(err, goods.ErrInvalidSteamLogin) {
		t.Fatalf("a login with a space: %v", err)
	}
	stars := starsOrder(customerID, starsID, "stars-abc", 50, 150)
	stars.Recipient = "abc"
	if _, err := store.CreateGoodsOrder(ctx, stars); !errors.Is(err, goods.ErrInvalidRecipient) {
		t.Fatalf("a three-character Telegram username: %v", err)
	}
}

// nervixyFake is an in-memory Nervixy API.
type nervixyFake struct {
	mu          sync.Mutex
	orders      int
	orderStatus int
	orderBody   string
	drop        bool
	states      map[string]string
}

func newNervixy(t *testing.T) (*nervixyFake, goods.Provider) {
	t.Helper()
	fake := &nervixyFake{states: map[string]string{}}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		var body map[string]any
		_ = json.NewDecoder(request.Body).Decode(&body)
		switch request.URL.Query().Get("action") {
		case "api_check_login":
			_ = json.NewEncoder(writer).Encode(map[string]any{"ok": true, "valid": true})
		case "api_order":
			fake.orders++
			if fake.drop {
				connection, _, _ := writer.(http.Hijacker).Hijack()
				_ = connection.Close()
				return
			}
			if fake.orderStatus != 0 {
				writer.WriteHeader(fake.orderStatus)
				_, _ = writer.Write([]byte(fake.orderBody))
				return
			}
			id := "nx-" + body["partner_id"].(string)
			fake.states[id] = "processing"
			_ = json.NewEncoder(writer).Encode(map[string]any{"ok": true, "order_id": id})
		case "api_status":
			_ = json.NewEncoder(writer).Encode(map[string]any{"ok": true, "status": fake.states[body["order_id"].(string)]})
		}
	}))
	t.Cleanup(server.Close)
	provider, err := nervixy.New(nervixy.Config{BaseURL: server.URL + "/api.php", APIKey: "key", HTTP: server.Client()}, nil)
	if err != nil {
		t.Fatalf("adapter: %v", err)
	}
	return fake, provider
}

// with runs a change to the fake under its lock: the fake's server answers on
// its own goroutines, so the test touching its fields directly is a data race.
func (fake *nervixyFake) with(change func()) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	change()
}

// placed is how many orders the fake has accepted so far.
func (fake *nervixyFake) placed() int {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return fake.orders
}

func (fake *nervixyFake) deliverAll() {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	for id := range fake.states {
		fake.states[id] = "delivered"
	}
}

// Nervixy charges when it accepts the order. An accepted order is polled to
// delivery even though its marker is set; nothing is ordered twice.
func TestANervixyTopUpIsOrderedOnceAndPolled(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	customerID := harness.customer(ctx, t)
	productID := harness.steamProduct(ctx, t, 10_000)
	harness.creditWallet(ctx, t, customerID, "seed", 200_000)
	fake, provider := newNervixy(t)
	order, err := store.CreateGoodsOrder(ctx, steamOrder(customerID, productID, "nx-1", "gaben_01", 10, 10_000))
	if err != nil {
		t.Fatalf("order: %v", err)
	}
	orderID := uuidText(order.ID)
	runDelivery(ctx, t, harness, provider, orderID, goodsdelivery.Options{})
	if delivery, _, _ := harness.deliveryState(ctx, t, orderID); delivery != "submitted" {
		t.Fatalf("an accepted order must be polled, not parked: %s", delivery)
	}
	runDelivery(ctx, t, harness, provider, orderID, goodsdelivery.Options{})
	fake.deliverAll()
	runDelivery(ctx, t, harness, provider, orderID, goodsdelivery.Options{})
	if delivery, status, _ := harness.deliveryState(ctx, t, orderID); delivery != "delivered" || status != "delivered" {
		t.Fatalf("delivery %s, order %s", delivery, status)
	}
	if placed := fake.placed(); placed != 1 {
		t.Fatalf("%d orders placed", placed)
	}
}

// A refusal Nervixy made before charging lifts the marker, so the delivery
// is retried; a lost answer to the charging call parks it for a person.
func TestNervixyRefusalsAndLostAnswers(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	customerID := harness.customer(ctx, t)
	productID := harness.steamProduct(ctx, t, 10_000)
	harness.creditWallet(ctx, t, customerID, "seed", 200_000)
	fake, provider := newNervixy(t)

	short, _ := store.CreateGoodsOrder(ctx, steamOrder(customerID, productID, "nx-short", "gaben_01", 5, 10_000))
	shortID := uuidText(short.ID)
	fake.with(func() {
		fake.orderStatus, fake.orderBody = http.StatusBadRequest, `{"ok":false,"error":"Недостаточно средств","required":6.29}`
	})
	runDelivery(ctx, t, harness, provider, shortID, goodsdelivery.Options{})
	if _, _, marked := harness.steamDelivery(ctx, t, shortID); marked {
		t.Fatal("a refusal before charging left the marker set")
	}
	if delivery, _, code := harness.deliveryState(ctx, t, shortID); delivery != "submitted" || code != "order_http_400" {
		t.Fatalf("delivery %s, code %s", delivery, code)
	}
	fake.with(func() { fake.orderStatus = 0 })
	runDelivery(ctx, t, harness, provider, shortID, goodsdelivery.Options{})
	fake.deliverAll()
	runDelivery(ctx, t, harness, provider, shortID, goodsdelivery.Options{})
	if delivery, _, _ := harness.deliveryState(ctx, t, shortID); delivery != "delivered" {
		t.Fatalf("after the balance was topped up: %s", delivery)
	}

	lost, _ := store.CreateGoodsOrder(ctx, steamOrder(customerID, productID, "nx-lost", "gaben_01", 5, 10_000))
	lostID := uuidText(lost.ID)
	fake.with(func() { fake.drop = true })
	runDelivery(ctx, t, harness, provider, lostID, goodsdelivery.Options{})
	runDelivery(ctx, t, harness, provider, lostID, goodsdelivery.Options{})
	if delivery, status, _ := harness.deliveryState(ctx, t, lostID); delivery != "needs_review" || status != "needs_review" {
		t.Fatalf("a lost charging answer: delivery %s, order %s", delivery, status)
	}
	if placed := fake.placed(); placed != 3 {
		t.Fatalf("orders placed = %d, want 3 (refused, retried, lost once)", placed)
	}
}
