package botapp

import (
	"strings"
	"testing"
)

// Each shop outcome reads as what happened, in the buyer's language, and the
// price-rise notice sends them back to confirm rather than to their orders.
func TestGoodsNoticesSayWhatHappened(t *testing.T) {
	base := goodsNotice{Kind: "telegram_stars", Recipient: "alice_stars", Stars: 250, PaidMinor: 36000, Currency: "RUB"}
	cases := map[string]struct {
		notice goodsNotice
		locale Locale
		want   []string
		route  string
	}{
		"delivered": {
			notice: with(base, func(n *goodsNotice) { n.Status = "delivered" }),
			locale: LocaleRussian, want: []string{"250 звёзд", "@alice_stars"},
		},
		"refunded, recipient": {
			notice: with(base, func(n *goodsNotice) { n.Status = "refunded"; n.Failure = "recipient_invalid" }),
			locale: LocaleEnglish, want: []string{"could not find @alice_stars", "360"}, route: routeShopOrders,
		},
		"refunded": {
			notice: with(base, func(n *goodsNotice) { n.Status = "refunded"; n.Failure = "retryable" }),
			locale: LocaleEnglish, want: []string{"could not be delivered", "back in your wallet"}, route: routeShopOrders,
		},
		"bought after a top-up": {
			notice: with(base, func(n *goodsNotice) { n.Auto = true }),
			locale: LocaleRussian, want: []string{"Баланс пополнен", "250 ⭐"}, route: routeShopOrders,
		},
		"price rose": {
			notice: with(base, func(n *goodsNotice) { n.PriceRose = true }),
			locale: LocaleEnglish, want: []string{"price of Stars changed", "not paid automatically"}, route: routeStars,
		},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			view := goodsNoticeView(test.locale, test.notice)
			for _, want := range test.want {
				if !strings.Contains(view.Text, want) {
					t.Fatalf("%q lacks %q", view.Text, want)
				}
			}
			if test.route != "" {
				if view.Keyboard == nil || view.Keyboard.InlineKeyboard[0][0].CallbackData != callbackPrefix+test.route {
					t.Fatalf("keyboard = %+v, want a button to %s", view.Keyboard, test.route)
				}
			}
		})
	}
}

func with(notice goodsNotice, change func(*goodsNotice)) goodsNotice {
	change(&notice)
	return notice
}

// Every Stars string exists in both languages, like the rest of the catalogue.
func TestStarsCopyIsBilingual(t *testing.T) {
	for key, message := range starsCatalog {
		if message.ru == "" || message.en == "" {
			t.Fatalf("%s is missing a translation", key)
		}
		if _, merged := catalog[key]; !merged {
			t.Fatalf("%s is not reachable through text()", key)
		}
	}
}
