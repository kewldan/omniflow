//go:build integration

package integrationtest

import (
	"context"
	"log/slog"
	"testing"

	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/commercepg"
)

// An add-on the wallet cannot cover is saved instead of opened as an unpaid
// order: nothing is charged while the balance is short, the top-up that covers
// it buys it once under the cart's key however often the purchase is retried,
// and a price above the one the customer saw is refused and stops the cart.
func TestASavedAddonIsBoughtOnceAfterTheTopUp(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	checkout := newAccountCheckout(t, harness, store)
	customerID := harness.customer(ctx, t)
	planVersionID := harness.catalog(ctx, t, "addon-cart-plan", 0)
	pack := harness.addon(ctx, t, planVersionID, "cart-pack", 15000, 10<<30, "full_price")
	plan, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: planVersionID, Currency: "RUB",
		Operation: "purchase", IdempotencyKey: "addon-cart-plan-order",
	})
	if err != nil {
		t.Fatalf("buy the plan: %v", err)
	}
	subscriptionID := uuidText(plan.SubscriptionID)
	harness.seedWallet(ctx, t, customerID, "addon-cart-wallet", 5000)

	listing, err := checkout.SaveSubscriptionAddonCart(ctx, customerID, subscriptionID, pack, 1, "en")
	if err != nil {
		t.Fatalf("save the add-on: %v", err)
	}
	if listing.Cart == nil || listing.Cart.SavedMinor != 15000 || listing.Cart.TotalMinor != 15000 || !listing.Cart.AutoPurchase {
		t.Fatalf("saved cart = %+v", listing.Cart)
	}
	purchase, err := store.TryPurchaseCart(ctx, customerID)
	if err != nil || purchase.Purchased || purchase.Reason != commerce.CartInsufficientBalance {
		t.Fatalf("a short wallet bought the add-on: %+v, %v", purchase, err)
	}
	var addonOrders int
	if err = harness.pool.QueryRow(ctx, `SELECT count(*) FROM orders WHERE user_id = $1::uuid AND operation = 'addon'`, customerID).Scan(&addonOrders); err != nil || addonOrders != 0 {
		t.Fatalf("a short wallet opened %d add-on orders (%v)", addonOrders, err)
	}

	topUp, err := store.CreateTopUpOrder(ctx, commercepg.TopUpInput{
		CustomerID: customerID, Currency: "RUB", AmountMinor: 10000, IdempotencyKey: "addon-cart-topup",
	})
	if err != nil {
		t.Fatalf("top up: %v", err)
	}
	harness.settle(ctx, t, store, uuidText(topUp.ID), 10000, "addon-cart-topup-event")
	if balance := harness.walletBalance(ctx, t, customerID); balance != 0 {
		t.Fatalf("balance after the top-up bought the add-on = %d, want 0", balance)
	}
	again, err := store.TryPurchaseCart(ctx, customerID)
	if err != nil || again.Purchased {
		t.Fatalf("the purchased cart bought again: %+v, %v", again, err)
	}
	store.SweepCarts(ctx, slog.New(slog.DiscardHandler))
	var orderState string
	if err = harness.pool.QueryRow(ctx, `SELECT count(*), min(state) FROM orders WHERE user_id = $1::uuid AND operation = 'addon'`,
		customerID).Scan(&addonOrders, &orderState); err != nil || addonOrders != 1 || (orderState != "paid" && orderState != "fulfilled") {
		t.Fatalf("add-on orders = %d (%s, %v), want one settled", addonOrders, orderState, err)
	}

	// A rise above the saved price is refused, and the cart stops buying itself.
	harness.seedWallet(ctx, t, customerID, "addon-cart-wallet-2", 50000)
	if _, err = checkout.SaveSubscriptionAddonCart(ctx, customerID, subscriptionID, pack, 1, "en"); err != nil {
		t.Fatalf("save again: %v", err)
	}
	if _, err = harness.pool.Exec(ctx, `UPDATE carts SET saved_price_minor = 100 WHERE user_id = $1::uuid AND status = 'open'`, customerID); err != nil {
		t.Fatalf("lower the saved price: %v", err)
	}
	refused, err := store.TryPurchaseCart(ctx, customerID)
	if err != nil || refused.Purchased || refused.Reason != commerce.CartPriceChanged {
		t.Fatalf("a dearer add-on was bought: %+v, %v", refused, err)
	}
	cart, found, err := store.OpenAddonCart(ctx, customerID)
	if err != nil || !found || cart.AutoPurchase {
		t.Fatalf("after a price rise the cart = %+v (found %v, %v), want it kept with automatic purchase off", cart.Cart, found, err)
	}
	if balance := harness.walletBalance(ctx, t, customerID); balance != 50000 {
		t.Fatalf("balance after the refusal = %d, want untouched", balance)
	}
}
