//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/accountpg"
	"github.com/omniflow/omniflow/internal/accountshop"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/goods"
	"github.com/omniflow/omniflow/internal/goodsdelivery"
	"github.com/riverqueue/river"
)

// The customer web shop against a real database.
//
// Four properties can only be checked here, because each of them is a claim
// about rows rather than about Go. A quote that has expired must not be
// chargeable even though the panel asked politely. A paid order must reach the
// provider exactly once, which is a claim about the delivery row's primary key
// and the worker's claim transaction. An ambiguous outcome must park rather
// than retry or refund, which is a claim about three tables agreeing. And an
// order identifier out of a URL must not address somebody else's purchase,
// which is a claim about the query's predicate.

// stubProvider is a gateway that answers however a test needs it to, and counts
// what it was asked to do.
//
// The count is the point: "delivers exactly once" is a statement about how many
// times a provider was told to buy something, and no amount of reading the
// database afterwards can substitute for it.
type stubProvider struct {
	mutex     sync.Mutex
	costMinor int64
	outcome   goods.Delivery
	quoteErr  error

	delivered int
	polled    int
}

func (provider *stubProvider) Name() string { return "fragment" }

func (provider *stubProvider) Supports(kind string) bool { return true }

func (provider *stubProvider) Quote(context.Context, goods.Request) (goods.Quote, error) {
	if provider.quoteErr != nil {
		return goods.Quote{}, provider.quoteErr
	}
	return goods.Quote{CostMinor: provider.costMinor, Currency: "RUB"}, nil
}

func (provider *stubProvider) Deliver(
	context.Context, goods.DeliveryRequest,
) (goods.Delivery, error) {
	provider.mutex.Lock()
	defer provider.mutex.Unlock()
	provider.delivered++
	return provider.outcome, nil
}

func (provider *stubProvider) Poll(context.Context, string) (goods.Delivery, error) {
	provider.mutex.Lock()
	defer provider.mutex.Unlock()
	provider.polled++
	return provider.outcome, nil
}

func (provider *stubProvider) Balance(context.Context) (int64, string, error) {
	return 1_000_000, "RUB", nil
}

func (provider *stubProvider) submissions() int {
	provider.mutex.Lock()
	defer provider.mutex.Unlock()
	return provider.delivered
}

// stubRegistry resolves every slug to the one stub, standing in for the
// credential-unsealing registry the API wires in production.
type stubRegistry struct{ provider goods.Provider }

func (registry stubRegistry) Provider(context.Context, string) (goods.Provider, error) {
	return registry.provider, nil
}

func testShop(harness *harness, provider goods.Provider) *accountshop.Service {
	service, err := accountshop.New(
		harness.pool, commercepg.New(harness.pool, nil, testOptions()), nil,
		stubRegistry{provider: provider},
		accountshop.Options{
			Settings: accountshop.Settings{Currency: "RUB"},
			Logger:   slog.New(slog.DiscardHandler),
		},
	)
	if err != nil {
		panic(err)
	}
	return service
}

// quotedProduct inserts a product priced off the provider's rate rather than at
// a number the operator published, which is the case where the quote's expiry
// actually means something.
func (harness *harness) quotedProduct(ctx context.Context, t *testing.T) string {
	t.Helper()
	if _, err := harness.pool.Exec(ctx,
		`INSERT INTO goods_providers (slug, enabled) VALUES ('fragment', true)
		 ON CONFLICT (slug) DO UPDATE SET enabled = true`); err != nil {
		t.Fatalf("create goods provider: %v", err)
	}
	var productID string
	if err := harness.pool.QueryRow(ctx,
		`INSERT INTO goods_products (code, provider_slug, kind, star_quantity, visible)
		 VALUES ('stars-250', 'fragment', 'telegram_stars', 250, true) RETURNING id::text`).
		Scan(&productID); err != nil {
		t.Fatalf("create goods product: %v", err)
	}
	if _, err := harness.pool.Exec(ctx,
		`INSERT INTO goods_pricing (product_id, currency, markup_bps, rounding, quote_ttl_seconds)
		 VALUES ($1::uuid, 'RUB', 1500, 'up_unit', 60)`, productID); err != nil {
		t.Fatalf("price goods product: %v", err)
	}
	return productID
}

// creditWallet seeds a balance so a purchase settles without a payment
// provider, which is what makes a delivery exist to test.
func (harness *harness) creditWallet(
	ctx context.Context, t *testing.T, customerID, key string, amountMinor int64,
) {
	t.Helper()
	if _, err := harness.pool.Exec(ctx, `WITH t AS (
			INSERT INTO ledger_transactions (type, reference_type, reference_id, idempotency_key)
			VALUES ('credit', 'test', $2, $2) RETURNING id)
		INSERT INTO ledger_entries (transaction_id, account_type, user_id, currency, amount_minor)
		SELECT id, 'customer_wallet', $1::uuid, 'RUB', $3::bigint FROM t
		UNION ALL SELECT id, 'platform_clearing', NULL, 'RUB', 0 - $3::bigint FROM t`,
		customerID, key, amountMinor); err != nil {
		t.Fatalf("seed wallet: %v", err)
	}
}

// buy walks the flow the panel walks: quote the product, review the recipient,
// and purchase against the quote that was shown.
func buy(
	ctx context.Context, t *testing.T, shop *accountshop.Service,
	customerID, productID, recipient, key string, useWallet bool,
) accountshop.Order {
	t.Helper()
	detail, err := shop.Detail(ctx, customerID, productID, "en", 1, "")
	if err != nil {
		t.Fatalf("quote product: %v", err)
	}
	reviewed, err := shop.Review(ctx, productID, recipient)
	if err != nil {
		t.Fatalf("review recipient: %v", err)
	}
	order, err := shop.Purchase(ctx, accountshop.PurchaseRequest{
		CustomerID: customerID, ProductID: productID, Quantity: 1,
		Recipient: reviewed.Username, ShownPriceMinor: detail.Quote.PriceMinor,
		ShownCurrency: detail.Quote.Currency, QuoteExpiresAt: detail.Quote.ExpiresAt,
		UseWallet: useWallet, IdempotencyKey: key, Locale: "en",
	})
	if err != nil {
		t.Fatalf("purchase: %v", err)
	}
	return order
}

// deliver runs the delivery worker once for an order, exactly as River would.
func deliver(
	ctx context.Context, t *testing.T, harness *harness, provider goods.Provider, orderID string,
) {
	t.Helper()
	worker := goodsdelivery.NewWorker(harness.pool, stubRegistry{provider: provider},
		slog.New(slog.DiscardHandler))
	if err := worker.Work(ctx, &river.Job[goodsdelivery.JobArgs]{
		Args: goodsdelivery.JobArgs{OrderID: orderID},
	}); err != nil {
		t.Fatalf("delivery worker: %v", err)
	}
}

// An installation that sells nothing must read as "not offered here" rather
// than as a shop whose shelves happen to be empty, because the second is a
// state customers come back for.
func TestAnInstallationWithNothingToSellIsNotAShop(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	provider := &stubProvider{outcome: goods.Delivery{Status: "delivered"}}
	shop := testShop(harness, provider)

	if _, err := shop.Products(ctx, "en"); !errors.Is(err, accountshop.ErrUnavailable) {
		t.Fatalf("an empty catalogue answered %v, expected the shop to be unavailable", err)
	}

	productID := harness.goodsProduct(ctx, t)
	products, err := shop.Products(ctx, "en")
	if err != nil {
		t.Fatalf("read catalogue: %v", err)
	}
	if len(products) != 1 || products[0].ID != productID {
		t.Fatalf("catalogue returned %d products", len(products))
	}
	if !products[0].Available {
		t.Fatal("a product with a working gateway read as unavailable")
	}
	// The operator's published price is known without asking anybody, which is
	// what lets the catalogue show a number before anything is quoted.
	if !products[0].PriceKnown || products[0].PriceMinor != 25_000 {
		t.Fatalf("catalogue price was %d (known=%v)", products[0].PriceMinor, products[0].PriceKnown)
	}

	// A gateway an operator switched off takes its products out of the
	// catalogue, at the switch rather than at the next deployment.
	if _, err = harness.pool.Exec(ctx,
		`UPDATE goods_providers SET enabled = false WHERE slug = 'fragment'`); err != nil {
		t.Fatalf("disable provider: %v", err)
	}
	if _, err = shop.Products(ctx, "en"); !errors.Is(err, accountshop.ErrUnavailable) {
		t.Fatalf("a disabled gateway left the shop answering %v", err)
	}
}

// The number on the screen is the number charged, and a screen showing a stale
// number is refused rather than quietly repriced.
func TestAnExpiredQuoteCannotBeCharged(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	provider := &stubProvider{costMinor: 20_000, outcome: goods.Delivery{Status: "delivered"}}
	shop := testShop(harness, provider)
	customerID := harness.customer(ctx, t)
	productID := harness.quotedProduct(ctx, t)

	detail, err := shop.Detail(ctx, customerID, productID, "en", 1, "")
	if err != nil {
		t.Fatalf("quote product: %v", err)
	}
	// 20,000 of cost plus fifteen per cent, rounded up to a whole rouble. The
	// markup comes from internal/goods, so this asserts the shop asked it rather
	// than that it re-derived the arithmetic.
	if detail.Quote.PriceMinor != 23_000 {
		t.Fatalf("price was %d, expected 23000", detail.Quote.PriceMinor)
	}
	if detail.Quote.ExpiresAt.IsZero() {
		t.Fatal("a derived price was quoted without an expiry")
	}

	request := accountshop.PurchaseRequest{
		CustomerID: customerID, ProductID: productID, Quantity: 1,
		Recipient: "recipient_one", ShownPriceMinor: detail.Quote.PriceMinor,
		ShownCurrency: detail.Quote.Currency,
		// The window this customer was shown closed while they were deciding.
		QuoteExpiresAt: time.Now().UTC().Add(-time.Second),
		IdempotencyKey: "web-shop-expired", Locale: "en",
	}
	if _, err = shop.Purchase(ctx, request); !errors.Is(err, accountshop.ErrQuoteExpired) {
		t.Fatalf("expected ErrQuoteExpired, got %v", err)
	}

	// A live window with a price that no longer holds is a different refusal,
	// because the customer has to see the new number before agreeing to it.
	request.QuoteExpiresAt = detail.Quote.ExpiresAt
	request.ShownPriceMinor = 19_000
	if _, err = shop.Purchase(ctx, request); !errors.Is(err, accountshop.ErrPriceChanged) {
		t.Fatalf("expected ErrPriceChanged, got %v", err)
	}

	// Neither refusal left an order behind, so a customer retrying does not
	// accumulate abandoned purchases.
	var orders int
	if err = harness.pool.QueryRow(ctx,
		`SELECT count(*) FROM orders WHERE user_id = $1::uuid AND operation = 'goods'`,
		customerID).Scan(&orders); err != nil {
		t.Fatalf("count orders: %v", err)
	}
	if orders != 0 {
		t.Fatalf("a refused purchase created %d orders", orders)
	}

	// And the price that is honoured is the one that was quoted.
	harness.creditWallet(ctx, t, customerID, "seed-expiry", 100_000)
	order := buy(ctx, t, shop, customerID, productID, "recipient_one", "web-shop-fresh", true)
	if order.PriceMinor != 23_000 {
		t.Fatalf("charged %d, quoted 23000", order.PriceMinor)
	}
}

// A purchase must reach the gateway once. The gateway honours no idempotency
// key, so a second submission is a second purchase of somebody's money.
func TestAPaidOrderIsDeliveredExactlyOnce(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	provider := &stubProvider{outcome: goods.Delivery{Status: "delivered", Reference: "gw-1"}}
	shop := testShop(harness, provider)
	customerID := harness.customer(ctx, t)
	productID := harness.goodsProduct(ctx, t)
	harness.creditWallet(ctx, t, customerID, "seed-once", 100_000)

	order := buy(ctx, t, shop, customerID, productID, "recipient_one", "web-shop-once", true)
	if order.Delivery.State != accountshop.StateQueued {
		t.Fatalf("a wallet-covered purchase read as %q, expected queued", order.Delivery.State)
	}

	// Three runs: the job River scheduled, a duplicate enqueue, and a retry
	// after a worker died somewhere it should not matter.
	for range 3 {
		deliver(ctx, t, harness, provider, order.ID)
	}
	if submissions := provider.submissions(); submissions != 1 {
		t.Fatalf("the gateway was asked to buy %d times", submissions)
	}

	delivered, err := shop.Order(ctx, customerID, order.ID, "en")
	if err != nil {
		t.Fatalf("read order: %v", err)
	}
	if delivered.Delivery.State != accountshop.StateDelivered {
		t.Fatalf("state = %q, expected delivered", delivered.Delivery.State)
	}
	if delivered.Delivery.SupportHandoff {
		t.Fatal("a delivered order offered a support handoff")
	}

	// The wallet paid for exactly one purchase and nothing was refunded.
	if balance := harness.walletBalance(ctx, t, customerID); balance != 75_000 {
		t.Fatalf("balance is %d, expected 75000", balance)
	}
}

// An ambiguous outcome is resolved by neither retry nor refund. It parks, and
// the customer surface says so rather than offering a button that could buy the
// goods a second time.
func TestAnAmbiguousDeliveryParksInsteadOfRetrying(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	provider := &stubProvider{outcome: goods.Delivery{
		Status: "failed", FailureClass: goods.FailureAmbiguous, ErrorCode: "timeout",
	}}
	shop := testShop(harness, provider)
	customerID := harness.customer(ctx, t)
	productID := harness.goodsProduct(ctx, t)
	harness.creditWallet(ctx, t, customerID, "seed-parked", 100_000)

	order := buy(ctx, t, shop, customerID, productID, "recipient_one", "web-shop-parked", true)
	for range 3 {
		deliver(ctx, t, harness, provider, order.ID)
	}
	if submissions := provider.submissions(); submissions != 1 {
		t.Fatalf("an ambiguous outcome was retried: %d submissions", submissions)
	}

	parked, err := shop.Order(ctx, customerID, order.ID, "en")
	if err != nil {
		t.Fatalf("read order: %v", err)
	}
	if parked.Delivery.State != accountshop.StateNeedsReview {
		t.Fatalf("state = %q, expected needs_review", parked.Delivery.State)
	}
	if !parked.Delivery.SupportHandoff {
		t.Fatal("a parked delivery offered no route to a person")
	}
	if parked.Delivery.Refund.Refunded {
		t.Fatal("an ambiguous delivery was refunded, which pays for goods that may have arrived")
	}

	// Nothing was given back, because nobody knows yet whether anything was
	// received. The wallet still shows the purchase.
	if balance := harness.walletBalance(ctx, t, customerID); balance != 75_000 {
		t.Fatalf("balance is %d, expected 75000", balance)
	}
	var refunds int
	if err = harness.pool.QueryRow(ctx,
		`SELECT count(*) FROM ledger_transactions WHERE idempotency_key = 'goods-refund:' || $1`,
		order.ID).Scan(&refunds); err != nil {
		t.Fatalf("count refunds: %v", err)
	}
	if refunds != 0 {
		t.Fatalf("an ambiguous delivery wrote %d refunds", refunds)
	}

	// And it left the due queue, so nothing sweeps it back into the gateway.
	var status string
	if err = harness.pool.QueryRow(ctx,
		`SELECT status FROM goods_deliveries WHERE order_id = $1::uuid`, order.ID).
		Scan(&status); err != nil {
		t.Fatalf("read delivery: %v", err)
	}
	if status != "needs_review" {
		t.Fatalf("delivery status is %q, expected needs_review", status)
	}
}

// A permanent failure is the one that refunds, through the wallet and the
// ordinary ledger. It is here to prove the parked case above is a deliberate
// exception rather than a refund path that simply does not work.
func TestAPermanentFailureRefundsToTheWallet(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	provider := &stubProvider{outcome: goods.Delivery{
		Status: "failed", FailureClass: goods.FailureRecipientInvalid, ErrorCode: "no_such_user",
	}}
	shop := testShop(harness, provider)
	customerID := harness.customer(ctx, t)
	productID := harness.goodsProduct(ctx, t)
	harness.creditWallet(ctx, t, customerID, "seed-refund", 100_000)

	order := buy(ctx, t, shop, customerID, productID, "recipient_one", "web-shop-refund", true)
	for range 2 {
		deliver(ctx, t, harness, provider, order.ID)
	}

	refunded, err := shop.Order(ctx, customerID, order.ID, "en")
	if err != nil {
		t.Fatalf("read order: %v", err)
	}
	if refunded.Delivery.State != accountshop.StateRefunded {
		t.Fatalf("state = %q, expected refunded", refunded.Delivery.State)
	}
	if refunded.Delivery.FailureReason != goods.FailureRecipientInvalid {
		t.Fatalf("failure reason = %q", refunded.Delivery.FailureReason)
	}
	// The amount is read from the ledger entry that landed, not recomputed, so
	// what the customer is told matches what their balance received.
	if refunded.Delivery.Refund.AmountMinor != 25_000 {
		t.Fatalf("refund reported %d", refunded.Delivery.Refund.AmountMinor)
	}
	if balance := harness.walletBalance(ctx, t, customerID); balance != 100_000 {
		t.Fatalf("balance is %d, expected the purchase to be made good", balance)
	}
}

// An order identifier out of a URL must not address anybody else's purchase,
// and "not yours" must be indistinguishable from "does not exist".
func TestShopHistoryIsScopedToItsOwner(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	provider := &stubProvider{outcome: goods.Delivery{Status: "delivered"}}
	shop := testShop(harness, provider)
	productID := harness.goodsProduct(ctx, t)

	owner := harness.customer(ctx, t)
	stranger := harness.customer(ctx, t)
	ownerOrder := buy(ctx, t, shop, owner, productID, "recipient_one", "web-shop-owner", false)
	strangerOrder := buy(ctx, t, shop, stranger, productID, "recipient_two", "web-shop-stranger", false)

	page, err := shop.Orders(ctx, owner, "en", accountshop.Cursor{}, 50)
	if err != nil {
		t.Fatalf("read history: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != ownerOrder.ID {
		t.Fatalf("history returned %d rows, expected only the owner's", len(page.Items))
	}

	if _, err = shop.Order(ctx, owner, strangerOrder.ID, "en"); !errors.Is(err, accountpg.ErrNotFound) {
		t.Fatalf("another customer's order answered %v, expected not found", err)
	}
	// The same answer for an identifier that never existed, so the route cannot
	// be used to discover which orders are real.
	if _, err = shop.Order(
		ctx, owner, "2f1c0c2e-0000-4000-8000-000000000000", "en",
	); !errors.Is(err, accountpg.ErrNotFound) {
		t.Fatalf("an unknown order answered %v, expected not found", err)
	}

	// An unpaid order is honest about having submitted nothing.
	if ownerOrder.Delivery.State != accountshop.StateAwaitingPayment {
		t.Fatalf("an unpaid purchase read as %q", ownerOrder.Delivery.State)
	}
	if provider.submissions() != 0 {
		t.Fatal("an unpaid order reached the gateway")
	}
}

// History pages through cursors rather than offsets, so a purchase made while
// the customer is reading cannot shift a row past them.
func TestShopHistoryPagesByCursor(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	provider := &stubProvider{outcome: goods.Delivery{Status: "delivered"}}
	shop := testShop(harness, provider)
	customerID := harness.customer(ctx, t)
	productID := harness.goodsProduct(ctx, t)

	for _, key := range []string{"web-page-1", "web-page-2", "web-page-3"} {
		buy(ctx, t, shop, customerID, productID, "recipient_one", key, false)
	}

	first, err := shop.Orders(ctx, customerID, "en", accountshop.Cursor{}, 2)
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(first.Items) != 2 || !first.HasMore {
		t.Fatalf("first page had %d rows, hasMore=%v", len(first.Items), first.HasMore)
	}
	second, err := shop.Orders(ctx, customerID, "en", first.Next, 2)
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if len(second.Items) != 1 || second.HasMore {
		t.Fatalf("second page had %d rows, hasMore=%v", len(second.Items), second.HasMore)
	}
	for _, seen := range first.Items {
		if seen.ID == second.Items[0].ID {
			t.Fatal("a cursor page repeated a row")
		}
	}
}
