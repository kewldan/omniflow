//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/commercepg"
)

// Promotions and saved carts for the shop, against a real database.
//
// Three properties can only be checked here. The catalogue split is enforced by
// a query, so a mock would agree with whatever the Go code believed. The cost
// floor is a table constraint as well as a domain rule, and the point of having
// both is that one of them holds when the other is bypassed. And the redemption
// count is shared between plans and goods, which is a claim about one table
// being written by two paths.

// goodsPromotion inserts a promotion for the shop and a code for it.
func (harness *harness) goodsPromotion(
	ctx context.Context, t *testing.T, code, kind string, value int64, appliesTo string,
) string {
	t.Helper()
	// The promotion's own code is the operator-facing slug and is lowercase;
	// the redeemable code a customer types is normalised to upper. They are
	// different columns with different shapes, and conflating them is how a
	// fixture ends up asserting nothing.
	var promotionID string
	if err := harness.pool.QueryRow(ctx,
		`INSERT INTO promotions (code, kind, value, currency, applies_to, per_customer_limit)
		 VALUES (lower($1), $2, $3, CASE WHEN $2 = 'fixed' THEN 'RUB' END, $4, 5)
		 RETURNING id::text`,
		code, kind, value, appliesTo).Scan(&promotionID); err != nil {
		t.Fatalf("create promotion: %v", err)
	}
	if _, err := harness.pool.Exec(ctx,
		`INSERT INTO promo_codes (promotion_id, normalized_code) VALUES ($1::uuid, $2)`,
		promotionID, code); err != nil {
		t.Fatalf("create promo code: %v", err)
	}
	return promotionID
}

func goodsOrder(customerID, productID, key string) commercepg.GoodsOrderInput {
	return commercepg.GoodsOrderInput{
		CustomerID: customerID, ProductID: productID, Quantity: 1,
		Recipient: "recipient_one", PriceMinor: 25_000, CostMinor: 20_000, CostKnown: true,
		Currency: "RUB", QuoteExpiresAt: time.Now().Add(time.Hour),
		IdempotencyKey: key, SkipWallet: true,
	}
}

// A promotion written for plans does not discount the shop, and the check that
// enforces it is the query rather than the Go code.
func TestAPlanPromotionCannotDiscountAShopOrder(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := commercepg.New(harness.pool, nil, testOptions())
	customerID := harness.customer(ctx, t)
	productID := harness.goodsProduct(ctx, t)

	// Unscoped, which for a plan promotion means every plan. Before the
	// catalogue split this would have matched the shop too.
	harness.goodsPromotion(ctx, t, "PLANSONLY", "percent", 1000, "plans")

	input := goodsOrder(customerID, productID, "goods-plan-promo")
	input.PromoCode = "PLANSONLY"
	if _, err := store.CreateGoodsOrder(ctx, input); !errors.Is(
		err, commercepg.ErrPromoIneligible,
	) {
		t.Fatalf("expected ErrPromoIneligible, got %v", err)
	}
}

// A shop promotion within the margin applies, and the money lands where it
// should: on the order, on the goods row, and in the shared redemption table.
func TestAShopPromotionDiscountsAndIsRecordedOnce(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := commercepg.New(harness.pool, nil, testOptions())
	customerID := harness.customer(ctx, t)
	productID := harness.goodsProduct(ctx, t)
	promotionID := harness.goodsPromotion(ctx, t, "SHOP10", "percent", 1000, "goods")

	input := goodsOrder(customerID, productID, "goods-shop-promo")
	input.PromoCode = "SHOP10"
	order, err := store.CreateGoodsOrder(ctx, input)
	if err != nil {
		t.Fatalf("create goods order: %v", err)
	}

	// Ten percent of 25,000 is 2,500, and the customer owes the rest.
	if order.DiscountMinor != 2_500 {
		t.Fatalf("discount was %d, expected 2500", order.DiscountMinor)
	}
	if order.SubtotalMinor != 25_000 || order.ExternalMinor != 22_500 {
		t.Fatalf("the order does not add up: subtotal %d external %d",
			order.SubtotalMinor, order.ExternalMinor)
	}

	// The goods row keeps the discount so the margin stays derivable. Without
	// it the finance view would report 5,000 of margin on a sale that earned
	// 2,500.
	var storedDiscount, storedPrice, storedCost int64
	if err := harness.pool.QueryRow(ctx,
		`SELECT discount_minor, quoted_price_minor, quoted_cost_minor
		 FROM goods_orders WHERE order_id = $1`, order.ID).
		Scan(&storedDiscount, &storedPrice, &storedCost); err != nil {
		t.Fatalf("read goods order: %v", err)
	}
	if storedDiscount != 2_500 {
		t.Fatalf("the goods row lost the discount: %d", storedDiscount)
	}
	if storedPrice-storedDiscount-storedCost != 2_500 {
		t.Fatalf("the realised margin is wrong: price %d discount %d cost %d",
			storedPrice, storedDiscount, storedCost)
	}

	// One redemption, in the same table a plan purchase writes to, so an
	// operator reading a promotion's usage gets one number.
	var redemptions int
	if err := harness.pool.QueryRow(ctx,
		`SELECT count(*) FROM promo_redemptions WHERE promotion_id = $1::uuid AND order_id = $2`,
		promotionID, order.ID).Scan(&redemptions); err != nil {
		t.Fatalf("count redemptions: %v", err)
	}
	if redemptions != 1 {
		t.Fatalf("expected one redemption, got %d", redemptions)
	}
}

// The provider still charges its cost whatever the customer pays, so a discount
// past the markup is refused rather than quietly capped.
func TestAShopDiscountPastTheMarginIsRefused(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := commercepg.New(harness.pool, nil, testOptions())
	customerID := harness.customer(ctx, t)
	productID := harness.goodsProduct(ctx, t)

	// The line has 5,000 of margin on 25,000. Half off would leave 12,500
	// against a cost of 20,000.
	harness.goodsPromotion(ctx, t, "HALFOFF", "percent", 5000, "goods")

	input := goodsOrder(customerID, productID, "goods-below-cost")
	input.PromoCode = "HALFOFF"
	if _, err := store.CreateGoodsOrder(ctx, input); !errors.Is(
		err, commercepg.ErrPromoBelowCost,
	) {
		t.Fatalf("expected ErrPromoBelowCost, got %v", err)
	}

	// Nothing was written: no order, and no redemption to leak a usage count.
	var orders int
	if err := harness.pool.QueryRow(ctx,
		`SELECT count(*) FROM orders WHERE user_id = $1::uuid AND operation = 'goods'`,
		customerID).Scan(&orders); err != nil {
		t.Fatalf("count orders: %v", err)
	}
	if orders != 0 {
		t.Fatalf("a refused promotion still created %d orders", orders)
	}
}

// The floor lives in the table as well as in Go, so a script cannot write what
// the application refuses.
func TestTheDatabaseRefusesADiscountBelowCost(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := commercepg.New(harness.pool, nil, testOptions())
	customerID := harness.customer(ctx, t)
	productID := harness.goodsProduct(ctx, t)

	order, err := store.CreateGoodsOrder(ctx, goodsOrder(customerID, productID, "goods-floor"))
	if err != nil {
		t.Fatalf("create goods order: %v", err)
	}
	// 25,000 priced, 20,000 cost: a 6,000 discount is 1,000 under.
	if _, err := harness.pool.Exec(ctx,
		`UPDATE goods_orders SET discount_minor = 6000 WHERE order_id = $1`, order.ID,
	); err == nil {
		t.Fatal("the table accepted a discount below cost")
	}
}

// A product whose provider will not say what it charges has no floor to check
// against, so it cannot be discounted.
func TestAnUnknownCostCannotBeDiscounted(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := commercepg.New(harness.pool, nil, testOptions())
	customerID := harness.customer(ctx, t)
	productID := harness.goodsProduct(ctx, t)
	harness.goodsPromotion(ctx, t, "ANYTHING", "percent", 1000, "goods")

	input := goodsOrder(customerID, productID, "goods-unknown-cost")
	input.PromoCode = "ANYTHING"
	input.CostKnown = false
	input.CostMinor = 0
	if _, err := store.CreateGoodsOrder(ctx, input); !errors.Is(
		err, commercepg.ErrPromoBelowCost,
	) {
		t.Fatalf("expected a refusal, got %v", err)
	}

	// Unless the operator set the price themselves, in which case the number
	// being discounted is one they chose.
	input.OperatorPriced = true
	order, err := store.CreateGoodsOrder(ctx, input)
	if err != nil {
		t.Fatalf("an operator-priced product refused a discount: %v", err)
	}
	if order.DiscountMinor != 2_500 {
		t.Fatalf("discount was %d, expected 2500", order.DiscountMinor)
	}
}

// A preview must not consume a redemption, or looking at a price would spend
// the code.
func TestPreviewingAShopDiscountDoesNotRedeemIt(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := commercepg.New(harness.pool, nil, testOptions())
	customerID := harness.customer(ctx, t)
	productID := harness.goodsProduct(ctx, t)
	promotionID := harness.goodsPromotion(ctx, t, "PREVIEW", "percent", 1000, "goods")

	input := goodsOrder(customerID, productID, "goods-preview")
	input.PromoCode = "PREVIEW"
	for range 3 {
		discount, err := store.PreviewGoodsDiscount(ctx, input)
		if err != nil {
			t.Fatalf("preview: %v", err)
		}
		if discount != 2_500 {
			t.Fatalf("preview discount was %d, expected 2500", discount)
		}
	}

	var redemptions int
	if err := harness.pool.QueryRow(ctx,
		`SELECT count(*) FROM promo_redemptions WHERE promotion_id = $1::uuid`,
		promotionID).Scan(&redemptions); err != nil {
		t.Fatalf("count redemptions: %v", err)
	}
	if redemptions != 0 {
		t.Fatalf("previewing redeemed the code %d times", redemptions)
	}
}

// A promotion scoped to specific products discounts those and no others.
func TestProductScopingIsEnforcedByTheQuery(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := commercepg.New(harness.pool, nil, testOptions())
	customerID := harness.customer(ctx, t)
	productID := harness.goodsProduct(ctx, t)
	promotionID := harness.goodsPromotion(ctx, t, "SCOPED", "percent", 1000, "goods")

	// Scoped to a different product than the one being bought.
	var otherID string
	if err := harness.pool.QueryRow(ctx,
		`INSERT INTO goods_products (code, provider_slug, kind, star_quantity, visible)
		 VALUES ('stars-500', 'fragment', 'telegram_stars', 500, true) RETURNING id::text`).
		Scan(&otherID); err != nil {
		t.Fatalf("create second product: %v", err)
	}
	if _, err := harness.pool.Exec(ctx,
		`INSERT INTO promotion_goods (promotion_id, product_id) VALUES ($1::uuid, $2::uuid)`,
		promotionID, otherID); err != nil {
		t.Fatalf("scope promotion: %v", err)
	}

	input := goodsOrder(customerID, productID, "goods-scoped")
	input.PromoCode = "SCOPED"
	if _, err := store.CreateGoodsOrder(ctx, input); !errors.Is(
		err, commercepg.ErrPromoIneligible,
	) {
		t.Fatalf("expected ErrPromoIneligible, got %v", err)
	}
}

// A goods promotion cannot be scoped to products it does not apply to, and the
// trigger is what says so.
func TestScopingAPlanPromotionToProductsIsRefused(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	productID := harness.goodsProduct(ctx, t)
	promotionID := harness.goodsPromotion(ctx, t, "WRONGSCOPE", "percent", 1000, "plans")

	if _, err := harness.pool.Exec(ctx,
		`INSERT INTO promotion_goods (promotion_id, product_id) VALUES ($1::uuid, $2::uuid)`,
		promotionID, productID); err == nil {
		t.Fatal("a plan promotion accepted goods scoping, which reads as configured and does nothing")
	}
}

// A saved shop purchase survives navigating away, carries its promo code, and
// never buys itself.
func TestAShopPurchaseCanBeSavedAndResumed(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := commercepg.New(harness.pool, nil, testOptions())
	customerID := harness.customer(ctx, t)
	productID := harness.goodsProduct(ctx, t)

	saved, err := store.SaveGoodsCart(ctx, commercepg.GoodsCartInput{
		CustomerID: customerID, ProductID: productID, Quantity: 1,
		Recipient: "recipient_one", SavedPriceMinor: 25_000, Currency: "RUB",
	})
	if err != nil {
		t.Fatalf("save cart: %v", err)
	}
	// A shop price is a provider quote that expires, so it is never charged
	// unattended.
	if saved.AutoPurchase {
		t.Fatal("a saved shop purchase would buy itself")
	}

	read, found, err := store.OpenGoodsCart(ctx, customerID)
	if err != nil || !found {
		t.Fatalf("read cart: %v found=%v", err, found)
	}
	if read.Line.RecipientUsername != "recipient_one" || read.Line.SavedPriceMinor != 25_000 {
		t.Fatalf("the saved line is wrong: %+v", read.Line)
	}

	if _, err := store.SetGoodsCartPromo(ctx, customerID, "shop10"); err != nil {
		t.Fatalf("set promo: %v", err)
	}
	withPromo, _, err := store.OpenGoodsCart(ctx, customerID)
	if err != nil {
		t.Fatalf("re-read cart: %v", err)
	}
	// Normalised on the way in, so the code stored is the code looked up.
	if withPromo.PromoCode.String != "SHOP10" {
		t.Fatalf("the promo code was not normalised: %q", withPromo.PromoCode.String)
	}
}

// The price is re-quoted before any charge, and a rise is refused rather than
// applied.
func TestASavedShopPurchaseRefusesAPriceRise(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := commercepg.New(harness.pool, nil, testOptions())
	customerID := harness.customer(ctx, t)
	productID := harness.goodsProduct(ctx, t)

	if _, err := store.SaveGoodsCart(ctx, commercepg.GoodsCartInput{
		CustomerID: customerID, ProductID: productID, Quantity: 1,
		Recipient: "recipient_one", SavedPriceMinor: 25_000, Currency: "RUB",
	}); err != nil {
		t.Fatalf("save cart: %v", err)
	}
	cart, _, err := store.OpenGoodsCart(ctx, customerID)
	if err != nil {
		t.Fatalf("read cart: %v", err)
	}
	now := time.Now().UTC()

	risen := commercepg.EvaluateGoodsCart(cart, 27_000, now, false)
	if risen.Ready() || risen.Reason != commerce.CartPriceChanged {
		t.Fatalf("a price rise was accepted: %+v", risen)
	}
	if !risen.Increased() {
		t.Fatal("the rise was not reported as one")
	}

	// A fall needs no confirmation: nobody objects to paying less.
	fallen := commercepg.EvaluateGoodsCart(cart, 22_000, now, false)
	if !fallen.Ready() {
		t.Fatalf("a price fall was refused: %+v", fallen)
	}

	same := commercepg.EvaluateGoodsCart(cart, 25_000, now, false)
	if !same.Ready() {
		t.Fatalf("an unchanged price was refused: %+v", same)
	}

	// A withdrawn product cannot be bought, and the cart survives so the
	// customer can see what it was.
	if _, err := harness.pool.Exec(ctx,
		`UPDATE goods_products SET visible = false WHERE id = $1::uuid`, productID); err != nil {
		t.Fatalf("hide product: %v", err)
	}
	hidden, _, err := store.OpenGoodsCart(ctx, customerID)
	if err != nil {
		t.Fatalf("re-read cart: %v", err)
	}
	if decision := commercepg.EvaluateGoodsCart(
		hidden, 25_000, now, false,
	); decision.Reason != commerce.CartPlanUnavailable {
		t.Fatalf("a withdrawn product was still purchasable: %+v", decision)
	}
}

// One open cart per customer means saving a shop item replaces a saved plan
// rather than sitting beside it — otherwise the auto-purchase sweep would charge
// for something the customer believed they had replaced.
func TestSavingAShopItemReplacesASavedPlan(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := commercepg.New(harness.pool, nil, testOptions())
	customerID := harness.customer(ctx, t)
	productID := harness.goodsProduct(ctx, t)
	planVersionID := harness.catalog(ctx, t, "cart-swap", 10_000)

	if _, err := store.SaveCart(ctx, commercepg.CartInput{
		CustomerID: customerID, PlanVersionID: planVersionID,
		Operation: "purchase", Currency: "RUB", AutoPurchase: true,
	}); err != nil {
		t.Fatalf("save plan cart: %v", err)
	}
	if _, err := store.SaveGoodsCart(ctx, commercepg.GoodsCartInput{
		CustomerID: customerID, ProductID: productID, Quantity: 1,
		Recipient: "recipient_one", SavedPriceMinor: 25_000, Currency: "RUB",
	}); err != nil {
		t.Fatalf("save goods cart: %v", err)
	}

	var open int
	if err := harness.pool.QueryRow(ctx,
		`SELECT count(*) FROM carts WHERE user_id = $1::uuid AND status = 'open'`,
		customerID).Scan(&open); err != nil {
		t.Fatalf("count carts: %v", err)
	}
	if open != 1 {
		t.Fatalf("expected one open cart, got %d", open)
	}

	cart, found, err := store.OpenGoodsCart(ctx, customerID)
	if err != nil || !found {
		t.Fatalf("the goods cart did not replace the plan: %v found=%v", err, found)
	}
	if cart.PlanVersionID.Valid {
		t.Fatal("the goods cart kept the plan it replaced")
	}
	if cart.AutoPurchase {
		t.Fatal("the goods cart inherited auto-purchase from the plan it replaced")
	}
}
