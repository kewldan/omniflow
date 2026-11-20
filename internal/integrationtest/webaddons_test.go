//go:build integration

package integrationtest

import (
	"context"
	"testing"

	"github.com/omniflow/omniflow/internal/commercepg"
)

// The web panel offers and buys a mid-period add-on through the same order
// path the bot uses: the wallet settles it, a repeated request with the same
// key is the same order, and someone else's subscription offers nothing.
func TestTheWebBuysAMidPeriodAddon(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	checkout := newAccountCheckout(t, harness, store)
	customerID := harness.customer(ctx, t)
	stranger := harness.customer(ctx, t)
	planVersionID := harness.catalog(ctx, t, "web-addon-plan", 0)
	pack := harness.addon(ctx, t, planVersionID, "web-pack", 15000, 10<<30, "full_price")
	plan, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: planVersionID, Currency: "RUB",
		Operation: "purchase", IdempotencyKey: "web-addon-plan-order",
	})
	if err != nil {
		t.Fatalf("buy the plan: %v", err)
	}
	subscriptionID := uuidText(plan.SubscriptionID)
	harness.seedWallet(ctx, t, customerID, "web-addon-wallet", 20000)

	offer, err := checkout.SubscriptionAddons(ctx, customerID, subscriptionID, "en")
	if err != nil {
		t.Fatalf("list add-ons: %v", err)
	}
	if !offer.Available || len(offer.Addons) != 1 || offer.Addons[0].ChargedMinor != 15000 || offer.WalletMinor != 20000 {
		t.Fatalf("offer = %+v", offer)
	}
	if foreign, foreignErr := checkout.SubscriptionAddons(ctx, stranger, subscriptionID, "en"); foreignErr != nil || foreign.Available {
		t.Fatalf("another customer's subscription was offered: %+v, %v", foreign, foreignErr)
	}

	first, err := checkout.BuySubscriptionAddon(ctx, customerID, subscriptionID, pack, 1, "click-1", "en")
	if err != nil {
		t.Fatalf("buy: %v", err)
	}
	again, err := checkout.BuySubscriptionAddon(ctx, customerID, subscriptionID, pack, 1, "click-1", "en")
	if err != nil || again.ID != first.ID {
		t.Fatalf("a repeated click opened another order: %v / %v", first.ID, again.ID)
	}
	if first.ExternalMinor != 0 {
		t.Fatalf("the wallet should have covered it: %+v", first)
	}
	if balance := harness.walletBalance(ctx, t, customerID); balance != 5000 {
		t.Fatalf("balance = %d, want one charge", balance)
	}
}
