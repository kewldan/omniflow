//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/botapp"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/goods"
	"github.com/omniflow/omniflow/internal/goodsdelivery"
	"github.com/omniflow/omniflow/internal/panelpg"
	"github.com/omniflow/omniflow/internal/tonrate"
	"github.com/riverqueue/river"
)

// Selling Stars the way the operator's Bedolaga fork does, against a real
// database: a custom amount at a fixed price per Star, delivered by an adapter
// that pays the provider itself, with the settlement marker that keeps a crash
// from paying twice.

// payingProvider stands in for fragment_direct. It calls BeforeSettle exactly
// where the real adapter does — after everything that cannot spend money and
// immediately before the payment — and then answers however the test asks.
type payingProvider struct {
	mutex sync.Mutex
	// before runs inside Deliver before the marker, to simulate what can
	// happen between the claim and the payment.
	before func()
	// outcome is returned once the marker committed.
	outcome goods.Delivery
	// markerErr records what BeforeSettle answered on the last delivery.
	markerErr error
	paid      int
	asked     int
}

func (provider *payingProvider) Name() string              { return "fragment_direct" }
func (provider *payingProvider) Supports(kind string) bool { return kind == goods.KindTelegramStars }
func (provider *payingProvider) Quote(context.Context, goods.Request) (goods.Quote, error) {
	return goods.Quote{}, goods.ErrCostUnavailable
}

func (provider *payingProvider) Deliver(ctx context.Context, request goods.DeliveryRequest) (goods.Delivery, error) {
	provider.mutex.Lock()
	provider.asked++
	before := provider.before
	provider.mutex.Unlock()
	if before != nil {
		before()
	}
	err := request.BeforeSettle(ctx, goods.SettlementMarker{
		ProviderRequestID: "req-" + request.OrderID, Asset: "TON", CostNano: 1_000_000_000,
	})
	provider.mutex.Lock()
	defer provider.mutex.Unlock()
	provider.markerErr = err
	if err != nil {
		return goods.Delivery{Status: "failed", FailureClass: goods.FailureRetryable, ErrorCode: "settlement_marker"}, nil
	}
	provider.paid++
	return provider.outcome, nil
}

func (provider *payingProvider) Poll(context.Context, string) (goods.Delivery, error) {
	return goods.Delivery{}, goods.ErrUnsupported
}

func (provider *payingProvider) Balance(context.Context) (int64, string, error) {
	return 0, "TON", nil
}

type fixedRate struct{ rate tonrate.Rate }

func (source fixedRate) Current(context.Context) (tonrate.Rate, error) { return source.rate, nil }

// starsProduct inserts a custom-amount Stars product sold through the direct
// provider at a fixed price per Star.
func (harness *harness) starsProduct(ctx context.Context, t *testing.T, unitMinor int64) string {
	t.Helper()
	if _, err := harness.pool.Exec(ctx,
		`UPDATE goods_providers SET enabled = true WHERE slug = 'fragment_direct'`); err != nil {
		t.Fatalf("enable provider: %v", err)
	}
	var productID string
	if err := harness.pool.QueryRow(ctx,
		`INSERT INTO goods_products (code, provider_slug, kind, min_star_quantity, max_star_quantity, star_presets, visible)
		 VALUES ('stars', 'fragment_direct', 'telegram_stars', 50, 10000, '{50,100,250}', true) RETURNING id::text`).
		Scan(&productID); err != nil {
		t.Fatalf("create product: %v", err)
	}
	if _, err := harness.pool.Exec(ctx,
		`INSERT INTO goods_pricing (product_id, currency, rounding, unit_price_minor)
		 VALUES ($1::uuid, 'RUB', 'none', $2)`, productID, unitMinor); err != nil {
		t.Fatalf("price product: %v", err)
	}
	return productID
}

func starsOrder(customerID, productID, key string, stars int, unitMinor int64) commercepg.GoodsOrderInput {
	return commercepg.GoodsOrderInput{
		CustomerID: customerID, ProductID: productID, Quantity: 1, Recipient: "alice_stars",
		PriceMinor: unitMinor * int64(stars), Currency: "RUB", OperatorPriced: true,
		StarQuantity: stars, Source: "web", QuoteExpiresAt: time.Now().Add(time.Hour),
		IdempotencyKey: key,
	}
}

func runDelivery(
	ctx context.Context, t *testing.T, harness *harness, provider goods.Provider, orderID string,
	options goodsdelivery.Options,
) {
	t.Helper()
	worker := goodsdelivery.NewWorker(harness.pool, stubRegistry{provider: provider},
		slog.New(slog.DiscardHandler)).WithOptions(options)
	if err := worker.Work(ctx, &river.Job[goodsdelivery.JobArgs]{
		Args: goodsdelivery.JobArgs{OrderID: orderID},
	}); err != nil {
		t.Fatalf("delivery worker: %v", err)
	}
}

func (harness *harness) deliveryState(ctx context.Context, t *testing.T, orderID string) (string, string, string) {
	t.Helper()
	var delivery, order, code string
	if err := harness.pool.QueryRow(ctx, `SELECT d.status, g.status, COALESCE(d.last_error_code, '')
		FROM goods_deliveries d JOIN goods_orders g ON g.order_id = d.order_id
		WHERE d.order_id = $1::uuid`, orderID).Scan(&delivery, &order, &code); err != nil {
		t.Fatalf("read delivery: %v", err)
	}
	return delivery, order, code
}

func TestAPaidStarsOrderRecordsItsSettlementAndNotifiesTheOperator(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	customerID := harness.customer(ctx, t)
	productID := harness.starsProduct(ctx, t, 150)
	harness.creditWallet(ctx, t, customerID, "seed", 50_000)

	order, err := store.CreateGoodsOrder(ctx, starsOrder(customerID, productID, "stars-1", 250, 150))
	if err != nil {
		t.Fatalf("order: %v", err)
	}
	orderID := uuidText(order.ID)
	balanceAfter := int64(4_000_000_000)
	provider := &payingProvider{outcome: goods.Delivery{Status: "delivered", Reference: "req-1",
		Settlement: &goods.Settlement{ProviderRequestID: "req-1", Asset: "TON", CostNano: 1_000_000_000,
			TxHash: "abc123", ProviderConfirmed: true, BalanceAfterNano: &balanceAfter}}}
	runDelivery(ctx, t, harness, provider, orderID, goodsdelivery.Options{
		Rates:          fixedRate{tonrate.Rate{MinorPerTON: 30_000, Currency: "RUB", Source: tonrate.SourceTonapi}},
		LowWalletStars: 1000,
	})

	if delivery, status, _ := harness.deliveryState(ctx, t, orderID); delivery != "delivered" || status != "delivered" {
		t.Fatalf("delivery %s, order %s", delivery, status)
	}
	var stars int
	var cost, rate int64
	var hash, source string
	if err := harness.pool.QueryRow(ctx, `SELECT g.star_quantity, d.settled_cost_minor, d.settled_rate_minor,
		d.settlement_tx_hash, d.settled_rate_source FROM goods_orders g JOIN goods_deliveries d ON d.order_id = g.order_id
		WHERE g.order_id = $1::uuid`, orderID).Scan(&stars, &cost, &rate, &hash, &source); err != nil {
		t.Fatalf("read settlement: %v", err)
	}
	if stars != 250 || cost != 30_000 || rate != 30_000 || hash != "abc123" || source != "tonapi" {
		t.Fatalf("settlement = %d stars, cost %d, rate %d, hash %s, source %s", stars, cost, rate, hash, source)
	}
	var event, recipient string
	var margin, covered int64
	var low bool
	if err := harness.pool.QueryRow(ctx, `SELECT payload->>'event', payload->>'recipient',
		(payload->>'marginMinor')::bigint, (payload->>'walletStarsCovered')::bigint, (payload->>'walletLow')::boolean
		FROM operator_notifications WHERE kind = 'purchase' AND dedupe_key = 'goods-sale:' || $1`, orderID).
		Scan(&event, &recipient, &margin, &covered, &low); err != nil {
		t.Fatalf("read sale notice: %v", err)
	}
	// 250 Stars at 1.50 = 375.00; 1 TON at 300.00 = 300.00 cost; 75.00 margin.
	// 4 TON left at 1 TON per 250 Stars covers 1000 Stars: not below 1000.
	if event != "goods_sale" || recipient != "alice_stars" || margin != 7_500 || covered != 1000 || low {
		t.Fatalf("sale notice = %s %s margin %d covered %d low %v", event, recipient, margin, covered, low)
	}

	// The margin is fixed: a later rate does not move it.
	operations := newOperations(t, harness)
	sales, err := operations.GoodsSalesStats(ctx, 30, "RUB",
		&tonrate.Rate{MinorPerTON: 99_000, Currency: "RUB", Source: tonrate.SourceManual})
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if sales.StarsSold != 250 || sales.RevenueMinor != 37_500 || sales.MarginMinor == nil || *sales.MarginMinor != 7_500 {
		t.Fatalf("stats = %+v", sales)
	}
}

func TestACrashAfterTheSettlementMarkerParksForReviewAndNeverPaysAgain(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	customerID := harness.customer(ctx, t)
	productID := harness.starsProduct(ctx, t, 150)
	harness.creditWallet(ctx, t, customerID, "seed", 50_000)
	order, err := store.CreateGoodsOrder(ctx, starsOrder(customerID, productID, "stars-crash", 100, 150))
	if err != nil {
		t.Fatalf("order: %v", err)
	}
	orderID := uuidText(order.ID)

	// A worker claimed the attempt, committed the marker, paid, and died
	// before recording anything. This is the row it leaves behind.
	if _, err := harness.pool.Exec(ctx, `UPDATE goods_deliveries
		SET status = 'submitted', attempt_count = 1, broadcasting_at = now(), next_attempt_at = now(),
		    provider_request_id = 'req-x', settlement_asset = 'TON', settlement_cost_nano = 1000000000
		WHERE order_id = $1::uuid`, orderID); err != nil {
		t.Fatalf("simulate crash: %v", err)
	}
	provider := &payingProvider{outcome: goods.Delivery{Status: "delivered"}}
	runDelivery(ctx, t, harness, provider, orderID, goodsdelivery.Options{})
	runDelivery(ctx, t, harness, provider, orderID, goodsdelivery.Options{})

	if provider.asked != 0 {
		t.Fatalf("the provider was asked %d times after a crash past the marker; it must not be asked at all", provider.asked)
	}
	delivery, status, code := harness.deliveryState(ctx, t, orderID)
	if delivery != "needs_review" || status != "needs_review" || code != "interrupted_after_marker" {
		t.Fatalf("delivery %s, order %s, code %s", delivery, status, code)
	}
	if balance := harness.walletBalance(ctx, t, customerID); balance != 50_000-15_000 {
		t.Fatalf("a parked delivery must not be refunded automatically: balance %d", balance)
	}
	var notices int
	_ = harness.pool.QueryRow(ctx, `SELECT count(*) FROM operator_notifications
		WHERE kind = 'fulfillment_failure' AND dedupe_key = 'goods-review:' || $1`, orderID).Scan(&notices)
	if notices != 1 {
		t.Fatalf("expected one review notice, got %d", notices)
	}
}

func TestAnythingButDeliveredAfterTheMarkerIsAmbiguous(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	customerID := harness.customer(ctx, t)
	productID := harness.starsProduct(ctx, t, 150)
	harness.creditWallet(ctx, t, customerID, "seed", 50_000)
	order, _ := store.CreateGoodsOrder(ctx, starsOrder(customerID, productID, "stars-lie", 100, 150))
	orderID := uuidText(order.ID)

	// An adapter that claims a retryable failure after money may have left
	// is overruled: the worker parks it.
	provider := &payingProvider{outcome: goods.Delivery{Status: "failed", FailureClass: goods.FailureRetryable}}
	runDelivery(ctx, t, harness, provider, orderID, goodsdelivery.Options{})
	if delivery, _, code := harness.deliveryState(ctx, t, orderID); delivery != "needs_review" || code != "after_settlement_marker" {
		t.Fatalf("delivery %s, code %s", delivery, code)
	}
}

func TestARefundBeforeTheMarkerStopsThePayment(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	customerID := harness.customer(ctx, t)
	productID := harness.starsProduct(ctx, t, 150)
	harness.creditWallet(ctx, t, customerID, "seed", 50_000)
	order, _ := store.CreateGoodsOrder(ctx, starsOrder(customerID, productID, "stars-race", 100, 150))
	orderID := uuidText(order.ID)
	operations := newOperations(t, harness)

	provider := &payingProvider{outcome: goods.Delivery{Status: "delivered"}}
	provider.before = func() {
		// The operator refunds while the worker is talking to Fragment.
		if err := operations.RefundGoodsOrder(ctx, orderID, panelpg.Actor{Reason: "customer asked"}); err != nil {
			t.Errorf("operator refund: %v", err)
		}
	}
	runDelivery(ctx, t, harness, provider, orderID, goodsdelivery.Options{})

	if provider.markerErr == nil || provider.paid != 0 {
		t.Fatalf("marker err %v, paid %d: a refunded delivery must not be paid for", provider.markerErr, provider.paid)
	}
	if delivery, status, _ := harness.deliveryState(ctx, t, orderID); delivery != "failed" || status != "refunded" {
		t.Fatalf("the worker overwrote the operator: delivery %s, order %s", delivery, status)
	}
	if balance := harness.walletBalance(ctx, t, customerID); balance != 50_000 {
		t.Fatalf("expected a single full refund, balance %d", balance)
	}
}

func TestRetryableFailuresRefundAfterTheLastAttempt(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	customerID := harness.customer(ctx, t)
	productID := harness.starsProduct(ctx, t, 150)
	harness.creditWallet(ctx, t, customerID, "seed", 50_000)
	order, _ := store.CreateGoodsOrder(ctx, starsOrder(customerID, productID, "stars-retry", 100, 150))
	orderID := uuidText(order.ID)

	// Fails before the payment every time: Fragment is down.
	provider := &stubProvider{outcome: goods.Delivery{
		Status: "failed", FailureClass: goods.FailureRetryable, ErrorCode: "fragment_unavailable",
	}}
	for attempt := 1; attempt <= goods.MaxAttempts+2; attempt++ {
		if _, err := harness.pool.Exec(ctx,
			`UPDATE goods_deliveries SET next_attempt_at = now() WHERE order_id = $1::uuid`, orderID); err != nil {
			t.Fatal(err)
		}
		runDelivery(ctx, t, harness, provider, orderID, goodsdelivery.Options{})
	}
	if provider.submissions() != goods.MaxAttempts {
		t.Fatalf("submitted %d times, want exactly %d", provider.submissions(), goods.MaxAttempts)
	}
	if delivery, status, _ := harness.deliveryState(ctx, t, orderID); delivery != "failed" || status != "refunded" {
		t.Fatalf("delivery %s, order %s", delivery, status)
	}
	if balance := harness.walletBalance(ctx, t, customerID); balance != 50_000 {
		t.Fatalf("expected the purchase refunded once, balance %d", balance)
	}
}

func TestTheSameKeyBuysOnce(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	customerID := harness.customer(ctx, t)
	productID := harness.starsProduct(ctx, t, 150)
	harness.creditWallet(ctx, t, customerID, "seed", 50_000)

	first, err := store.CreateGoodsOrder(ctx, starsOrder(customerID, productID, "same-key", 100, 150))
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateGoodsOrder(ctx, starsOrder(customerID, productID, "same-key", 100, 150))
	if err != nil {
		t.Fatal(err)
	}
	if uuidText(first.ID) != uuidText(second.ID) {
		t.Fatal("a repeated key opened a second order")
	}
	if balance := harness.walletBalance(ctx, t, customerID); balance != 50_000-15_000 {
		t.Fatalf("charged more than once: balance %d", balance)
	}
}

func TestAPriceThatMovedIsRefusedUnderTheOrderLock(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	customerID := harness.customer(ctx, t)
	productID := harness.starsProduct(ctx, t, 150)
	harness.creditWallet(ctx, t, customerID, "seed", 50_000)
	if _, err := harness.pool.Exec(ctx,
		`UPDATE goods_pricing SET unit_price_minor = 160 WHERE product_id = $1::uuid`, productID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateGoodsOrder(ctx, starsOrder(customerID, productID, "moved", 100, 150)); !errors.Is(err, commercepg.ErrPriceChanged) {
		t.Fatalf("err = %v, want a price-changed refusal", err)
	}
	if balance := harness.walletBalance(ctx, t, customerID); balance != 50_000 {
		t.Fatalf("a refused order charged: balance %d", balance)
	}
}

func TestAShortWalletSavesTheOrderAndTheTopUpBuysIt(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	customerID := harness.customer(ctx, t)
	productID := harness.starsProduct(ctx, t, 150)
	harness.creditWallet(ctx, t, customerID, "seed", 5_000)

	// 100 Stars at 1.50 = 150.00; the wallet holds 50.00.
	if _, err := store.SaveGoodsCart(ctx, commercepg.GoodsCartInput{
		CustomerID: customerID, ProductID: productID, Quantity: 1, Recipient: "alice_stars",
		SavedPriceMinor: 15_000, Currency: "RUB", StarQuantity: 100, AutoPurchase: true,
	}); err != nil {
		t.Fatalf("save cart: %v", err)
	}
	purchase, err := store.TryPurchaseCart(ctx, customerID)
	if err != nil || purchase.Purchased || purchase.Reason != commerce.CartInsufficientBalance {
		t.Fatalf("short wallet: %+v, %v", purchase, err)
	}
	var orders int
	_ = harness.pool.QueryRow(ctx, `SELECT count(*) FROM goods_orders WHERE user_id = $1::uuid`, customerID).Scan(&orders)
	if orders != 0 {
		t.Fatal("a short wallet must not open an order with an external part")
	}

	// The customer tops up exactly the shortfall; settlement buys the cart.
	topUp, err := store.CreateTopUpOrder(ctx, commercepg.TopUpInput{
		CustomerID: customerID, Currency: "RUB", AmountMinor: 10_000, IdempotencyKey: "shortfall",
	})
	if err != nil {
		t.Fatalf("top-up: %v", err)
	}
	harness.settle(ctx, t, store, uuidText(topUp.ID), 10_000, "shortfall-paid")

	var source, recipient string
	var stars int
	if err := harness.pool.QueryRow(ctx, `SELECT source, recipient_username, star_quantity FROM goods_orders
		WHERE user_id = $1::uuid`, customerID).Scan(&source, &recipient, &stars); err != nil {
		t.Fatalf("the top-up did not buy the saved order: %v", err)
	}
	if source != "auto_cart" || recipient != "alice_stars" || stars != 100 {
		t.Fatalf("order = %s %s %d", source, recipient, stars)
	}
	if balance := harness.walletBalance(ctx, t, customerID); balance != 0 {
		t.Fatalf("balance after the automatic purchase = %d", balance)
	}
	var cartStatus string
	_ = harness.pool.QueryRow(ctx, `SELECT status FROM carts WHERE user_id = $1::uuid`, customerID).Scan(&cartStatus)
	if cartStatus != "purchased" {
		t.Fatalf("cart status = %s", cartStatus)
	}

	// The sweep running again, and a replayed credit, buy nothing more.
	store.SweepCarts(ctx, slog.New(slog.DiscardHandler))
	harness.settle(ctx, t, store, uuidText(topUp.ID), 10_000, "shortfall-paid-again")
	_ = harness.pool.QueryRow(ctx, `SELECT count(*) FROM goods_orders WHERE user_id = $1::uuid`, customerID).Scan(&orders)
	if orders != 1 {
		t.Fatalf("expected one order, got %d", orders)
	}
}

func TestAPriceRiseStopsTheAutomaticPurchase(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	customerID := harness.customer(ctx, t)
	productID := harness.starsProduct(ctx, t, 150)
	if _, err := store.SaveGoodsCart(ctx, commercepg.GoodsCartInput{
		CustomerID: customerID, ProductID: productID, Quantity: 1, Recipient: "alice_stars",
		SavedPriceMinor: 15_000, Currency: "RUB", StarQuantity: 100, AutoPurchase: true,
	}); err != nil {
		t.Fatalf("save cart: %v", err)
	}
	if _, err := harness.pool.Exec(ctx,
		`UPDATE goods_pricing SET unit_price_minor = 160 WHERE product_id = $1::uuid`, productID); err != nil {
		t.Fatal(err)
	}
	harness.creditWallet(ctx, t, customerID, "topped-up", 50_000)
	store.SweepCarts(ctx, slog.New(slog.DiscardHandler))

	var orders int
	_ = harness.pool.QueryRow(ctx, `SELECT count(*) FROM goods_orders WHERE user_id = $1::uuid`, customerID).Scan(&orders)
	if orders != 0 {
		t.Fatal("a price rise was charged unattended")
	}
	var failure string
	var auto bool
	if err := harness.pool.QueryRow(ctx, `SELECT last_failure, auto_purchase FROM carts
		WHERE user_id = $1::uuid AND status = 'open'`, customerID).Scan(&failure, &auto); err != nil {
		t.Fatal(err)
	}
	if failure != commerce.CartPriceChanged || auto {
		t.Fatalf("cart failure %s, auto %v: a refused rise must stop the cart buying itself", failure, auto)
	}
	// Dropping the price back does not charge behind the customer's back.
	if _, err := harness.pool.Exec(ctx,
		`UPDATE goods_pricing SET unit_price_minor = 150 WHERE product_id = $1::uuid`, productID); err != nil {
		t.Fatal(err)
	}
	store.SweepCarts(ctx, slog.New(slog.DiscardHandler))
	_ = harness.pool.QueryRow(ctx, `SELECT count(*) FROM goods_orders WHERE user_id = $1::uuid`, customerID).Scan(&orders)
	if orders != 0 {
		t.Fatal("the cart bought itself after the customer was told it would not")
	}
}

func TestTheBuyerIsToldOnceWhatBecameOfTheirStars(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	customerID := harness.customer(ctx, t)
	productID := harness.starsProduct(ctx, t, 150)
	harness.creditWallet(ctx, t, customerID, "seed", 50_000)
	order, err := store.CreateGoodsOrder(ctx, starsOrder(customerID, productID, "told", 100, 150))
	if err != nil {
		t.Fatal(err)
	}
	runDelivery(ctx, t, harness, &payingProvider{outcome: goods.Delivery{Status: "delivered"}},
		uuidText(order.ID), goodsdelivery.Options{})

	bot, err := botapp.NewPostgresStoreWithPool(harness.pool)
	if err != nil {
		t.Fatal(err)
	}
	notices, err := bot.PendingGoodsNotices(ctx, 50)
	if err != nil {
		t.Fatalf("notice query: %v", err)
	}
	if len(notices) != 1 || notices[0].Status != "delivered" || notices[0].Stars != 100 ||
		notices[0].Recipient != "alice_stars" {
		t.Fatalf("notices = %+v", notices)
	}
	if _, err := harness.pool.Exec(ctx, `INSERT INTO notification_deliveries (user_id, kind, dedupe_key, status)
		VALUES ($1::uuid, 'fulfillment', $2, 'sent')`, customerID, notices[0].Dedupe); err != nil {
		t.Fatal(err)
	}
	if notices, err = bot.PendingGoodsNotices(ctx, 50); err != nil || len(notices) != 0 {
		t.Fatalf("a sent notice came back: %+v, %v", notices, err)
	}
}

func TestOperatorActionsOnAParkedDelivery(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	operations := newOperations(t, harness)
	customerID := harness.customer(ctx, t)
	productID := harness.starsProduct(ctx, t, 150)
	harness.creditWallet(ctx, t, customerID, "seed", 100_000)

	park := func(key string) string {
		order, err := store.CreateGoodsOrder(ctx, starsOrder(customerID, productID, key, 100, 150))
		if err != nil {
			t.Fatal(err)
		}
		orderID := uuidText(order.ID)
		provider := &payingProvider{outcome: goods.Delivery{
			Status: "failed", FailureClass: goods.FailureAmbiguous, ErrorCode: "broadcast_uncertain",
		}}
		runDelivery(ctx, t, harness, provider, orderID, goodsdelivery.Options{})
		return orderID
	}
	actor := panelpg.Actor{Reason: "checked the wallet"}

	// Retry: the payment did not leave.
	retried := park("park-retry")
	if err := operations.RetryGoodsDelivery(ctx, retried, panelpg.Actor{}); !errors.Is(err, panelpg.ErrValidaton) {
		t.Fatalf("a retry without a reason must be refused: %v", err)
	}
	if err := operations.RetryGoodsDelivery(ctx, retried, actor); err != nil {
		t.Fatalf("retry: %v", err)
	}
	var status string
	var attempts int
	var marked bool
	_ = harness.pool.QueryRow(ctx, `SELECT status, attempt_count, broadcasting_at IS NOT NULL FROM goods_deliveries
		WHERE order_id = $1::uuid`, retried).Scan(&status, &attempts, &marked)
	if status != "pending" || attempts != 0 || marked {
		t.Fatalf("after retry: %s, %d attempts, marked %v", status, attempts, marked)
	}
	delivered := &payingProvider{outcome: goods.Delivery{Status: "delivered"}}
	runDelivery(ctx, t, harness, delivered, retried, goodsdelivery.Options{})
	if delivery, _, _ := harness.deliveryState(ctx, t, retried); delivery != "delivered" {
		t.Fatalf("a retried delivery did not deliver: %s", delivery)
	}

	// Refund: credited once, however often it is asked.
	refunded := park("park-refund")
	before := harness.walletBalance(ctx, t, customerID)
	if err := operations.RefundGoodsOrder(ctx, refunded, actor); err != nil {
		t.Fatalf("refund: %v", err)
	}
	if err := operations.RefundGoodsOrder(ctx, refunded, actor); !errors.Is(err, panelpg.ErrRejected) {
		t.Fatalf("a second refund must be refused: %v", err)
	}
	if gained := harness.walletBalance(ctx, t, customerID) - before; gained != 15_000 {
		t.Fatalf("refund credited %d", gained)
	}

	// Complete: the Stars did arrive.
	completed := park("park-complete")
	if err := operations.CompleteGoodsDelivery(ctx, completed, "f00d", actor); err != nil {
		t.Fatalf("complete: %v", err)
	}
	var hash string
	_ = harness.pool.QueryRow(ctx, `SELECT d.status, d.settlement_tx_hash FROM goods_deliveries d
		WHERE d.order_id = $1::uuid`, completed).Scan(&status, &hash)
	if status != "delivered" || hash != "f00d" {
		t.Fatalf("after complete: %s %s", status, hash)
	}

	var audits int
	_ = harness.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action IN
		('panel.goods.delivery_retried', 'panel.goods.order_refunded', 'panel.goods.delivery_completed')`).Scan(&audits)
	if audits != 3 {
		t.Fatalf("expected an audit record per action, got %d", audits)
	}
}
