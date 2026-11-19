//go:build integration

package integrationtest

import (
	"context"
	"testing"

	"github.com/omniflow/omniflow/internal/accountcheckout"
)

// A gift bought on the web is the bot's gift: the wallet pays it, the code is
// shown once, a second request with the same key is the same gift, and the
// recipient redeems it from their wallet page.
func TestWebGiftIsBoughtOnceAndRedeemedByTheRecipient(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	service := newAccountCheckout(t, harness, store)
	planVersionID := harness.catalog(ctx, t, "gift-plan", 22900)
	sender := harness.customer(ctx, t)
	harness.seedWallet(ctx, t, sender, "gift-wallet", 50000)

	gift, err := service.BuyGift(ctx, sender, "web-gift-0001", accountcheckout.GiftInput{PlanVersionID: planVersionID, Currency: "RUB", Message: "С днём рождения!"})
	if err != nil || gift.Code == "" || gift.ExternalDue != 0 {
		t.Fatalf("buy: %+v %v", gift, err)
	}
	again, err := service.BuyGift(ctx, sender, "web-gift-0001", accountcheckout.GiftInput{PlanVersionID: planVersionID, Currency: "RUB"})
	if err != nil || again.ID != gift.ID || again.Code != "" {
		t.Fatalf("retry: %+v %v; want the same gift without the code", again, err)
	}
	if balance := harness.walletBalance(ctx, t, sender); balance != 50000-22900 {
		t.Fatalf("sender balance = %d", balance)
	}

	recipient := harness.customer(ctx, t)
	redeemed, err := service.RedeemCode(ctx, recipient, gift.Code)
	if err != nil || redeemed.Kind != "subscription" {
		t.Fatalf("redeem: %+v %v", redeemed, err)
	}
	sent, err := service.GiftsSent(ctx, sender)
	if err != nil || len(sent) != 1 || sent[0].Status != "claimed" {
		t.Fatalf("sent gifts = %+v (%v)", sent, err)
	}
	received, err := service.GiftsReceived(ctx, recipient)
	if err != nil || len(received) != 1 {
		t.Fatalf("received gifts = %+v (%v)", received, err)
	}
}
