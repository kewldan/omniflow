package botapp

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/goods"
)

func steamProductFixture() ShopProduct {
	unit := int64(10_500)
	return ShopProduct{
		ID: "11111111-1111-1111-1111-111111111111", Code: "steam", Kind: goods.KindSteamTopUp,
		Name: "Steam", Currency: "RUB", MinStars: 1, MaxStars: 200, Presets: []int{5, 10},
		UnitMinor: &unit, Rounding: goods.RoundMinor,
	}
}

// A Steam product is a chosen amount with its own flow: it is not a pack, not
// the Stars flow's product, and the pack path refuses to price it.
func TestSteamProductIsItsOwnFlow(t *testing.T) {
	product := steamProductFixture()
	if !product.SteamTopUp() || product.CustomAmount() || product.Pack() {
		t.Fatalf("SteamTopUp=%v CustomAmount=%v Pack=%v", product.SteamTopUp(), product.CustomAmount(), product.Pack())
	}
	commerce := &Commerce{}
	if _, err := commerce.QuoteGoods(context.Background(), product, 1); !errors.Is(err, ErrShopUnavailable) {
		t.Fatalf("the pack path priced a Steam top-up: %v", err)
	}
	stars := ShopProduct{Kind: goods.KindTelegramStars, MinStars: 50, MaxStars: 1000}
	if _, err := commerce.QuoteGoods(context.Background(), stars, 1); !errors.Is(err, ErrShopUnavailable) {
		t.Fatalf("the pack path priced a custom Stars amount: %v", err)
	}
}

// The operator's price per dollar is charged times the dollars, without the
// provider being asked.
func TestSteamOperatorPriceIsPerDollar(t *testing.T) {
	commerce := &Commerce{clock: time.Now}
	quote, err := commerce.QuoteStars(context.Background(), steamProductFixture(), 1, 10)
	if err != nil || quote.PriceMinor != 105_000 || quote.Currency != "RUB" {
		t.Fatalf("quote = %+v, %v", quote, err)
	}
}

// The menu shows the Steam entry only when a Steam product is on sale, and a
// Steam product alone does not open the shop catalogue.
func TestSteamMenuEntry(t *testing.T) {
	hasRoute := func(menu MenuState, route string) bool {
		for _, buttons := range commerceMenuRows(LocaleRussian, menu) {
			for _, button := range buttons {
				if button.CallbackData == callbackPrefix+route {
					return true
				}
			}
		}
		return false
	}
	if hasRoute(MenuState{CommerceEnabled: true}, routeSteam) {
		t.Fatal("the Steam entry shows with nothing on sale")
	}
	menu := MenuState{CommerceEnabled: true, SteamEnabled: true}
	if !hasRoute(menu, routeSteam) || hasRoute(menu, routeShop) {
		t.Fatal("a Steam product must add its own entry and nothing else")
	}
	if !knownRoute(routeSteam) || !commerceOnlyRoute(routeSteam) {
		t.Fatal("the Steam route is not routed")
	}
}

// Steam notices name the login as code, never as a Telegram handle.
func TestSteamNoticesSayWhatHappened(t *testing.T) {
	base := goodsNotice{Kind: goods.KindSteamTopUp, Recipient: "gaben_01", Stars: 10, PaidMinor: 105_000, Currency: "RUB"}
	cases := map[string]struct {
		notice goodsNotice
		locale Locale
		want   []string
		route  string
	}{
		"delivered": {
			notice: with(base, func(n *goodsNotice) { n.Status = "delivered" }),
			locale: LocaleRussian, want: []string{"$10", "<code>gaben_01</code>"},
		},
		"unknown login": {
			notice: with(base, func(n *goodsNotice) { n.Status = "refunded"; n.Failure = "recipient_invalid" }),
			locale: LocaleEnglish, want: []string{"no account <code>gaben_01</code>", "back in your wallet"}, route: routeShopOrders,
		},
		"refunded": {
			notice: with(base, func(n *goodsNotice) { n.Status = "refunded"; n.Failure = "permanent" }),
			locale: LocaleRussian, want: []string{"Не удалось пополнить", "вернули на баланс"}, route: routeShopOrders,
		},
		"bought after a top-up": {
			notice: with(base, func(n *goodsNotice) { n.Auto = true }),
			locale: LocaleEnglish, want: []string{"$10 for Steam"}, route: routeShopOrders,
		},
		"price rose": {
			notice: with(base, func(n *goodsNotice) { n.PriceRose = true }),
			locale: LocaleRussian, want: []string{"не оплачено автоматически"}, route: routeSteam,
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
			if strings.Contains(view.Text, "@") || strings.Contains(view.Text, "⭐") {
				t.Fatalf("a Steam notice reads like Stars: %q", view.Text)
			}
			if test.route != "" {
				if view.Keyboard == nil || view.Keyboard.InlineKeyboard[0][0].CallbackData != callbackPrefix+test.route {
					t.Fatalf("keyboard = %+v, want a button to %s", view.Keyboard, test.route)
				}
			}
		})
	}
}

// The order history names a Steam login, and a Telegram recipient as before.
func TestShopRecipientLine(t *testing.T) {
	steam := shopRecipientLine(LocaleEnglish, ShopOrder{Kind: goods.KindSteamTopUp, Recipient: "gaben_01"})
	if steam != "Steam login: gaben_01" {
		t.Fatalf("steam line = %q", steam)
	}
	stars := shopRecipientLine(LocaleEnglish, ShopOrder{Kind: goods.KindTelegramStars, Recipient: "alice"})
	if !strings.Contains(stars, "@alice") {
		t.Fatalf("stars line = %q", stars)
	}
}

// Every Steam string exists in both languages and is reachable through text().
func TestSteamCopyIsBilingual(t *testing.T) {
	for key, message := range steamCatalog {
		if message.ru == "" || message.en == "" {
			t.Fatalf("%s is missing a translation", key)
		}
		if _, merged := catalog[key]; !merged {
			t.Fatalf("%s is not reachable through text()", key)
		}
	}
}
