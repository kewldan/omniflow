package botapp

import (
	"strings"
	"testing"
	"time"
)

// Every action button a commerce screen renders has to be in commerceActions,
// or the tap never reaches its handler and the customer reads "Action was not
// completed". The gift, shop, and offer surfaces shipped with handlers and
// buttons and no entry in that set; this pins every surface down.
func TestEveryRenderedCommerceActionIsRoutable(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	product := ShopProduct{ID: "p1", Kind: "premium", Name: "Premium", Currency: "RUB", PriceMinor: 1000, PriceKnown: true}
	quote := ShopQuote{PriceMinor: 1000, Currency: "RUB", ExpiresAt: now.Add(time.Hour)}
	offer := CustomerOffer{ID: "o1", Title: "Offer", PromoCode: "CODE", ExpiresAt: now.Add(48 * time.Hour)}
	views := []View{
		giftsView(LocaleEnglish, []SentGift{{CodeHint: "ABCD", Kind: "subscription", Status: "deliverable"}}),
		giftPlansView(LocaleEnglish, []Plan{{PlanVersionID: "pv", Name: "Pro", Currency: "RUB", AmountMinor: 100}}, "RUB"),
		giftCreditView(LocaleEnglish, []int64{50000}, "RUB"),
		giftCodeView(LocaleEnglish, GiftPurchase{OrderID: "o", Code: "X"}, OrderSummary{ID: "o", ExternalMinor: 1}),
		giftClaimedView(LocaleEnglish, ClaimedGift{Kind: "wallet_credit", CreditMinor: 1, Currency: "RUB"}),
		shopCatalogView(LocaleEnglish, []ShopProduct{product}),
		shopItemView(LocaleEnglish, product, quote, false),
		shopConfirmView(LocaleEnglish, product, quote, "someone", false, ShopPromoState{Code: "CODE", DiscountMinor: 10}),
		shopOrdersView(LocaleEnglish, []ShopOrder{{OrderID: "o", ProductName: "Premium", Status: "paid"}}),
		shopSavedView(LocaleEnglish, product),
		offersView(LocaleEnglish, []CustomerOffer{offer}, now),
		offerDetailView(LocaleEnglish, offer, now),
	}
	for _, view := range views {
		for _, data := range callbackData(view) {
			if !strings.HasPrefix(data, actionPrefix) {
				continue
			}
			action := strings.SplitN(strings.TrimPrefix(data, actionPrefix), ":", 2)[0]
			if !commerceActions[action] {
				t.Fatalf("button %q is rendered but %q is not a routable commerce action", data, action)
			}
		}
	}
}
