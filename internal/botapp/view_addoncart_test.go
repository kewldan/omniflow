package botapp

import (
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

func TestAShortWalletOffersToBuyTheAddonAfterATopUp(t *testing.T) {
	t.Parallel()
	subscription, addon := addonFixture()
	charge := commerce.AddonCharge{ChargedMinor: 12000, Quantity: 1, Proration: addon.Proration}
	short := addonConfirmView(LocaleEnglish, subscription, addon, charge, 5000, "2")
	want := actionPrefix + "addon-cart:" + addon.AddonVersionID + ":2"
	if !strings.Contains(strings.Join(callbackData(short), " "), want) {
		t.Fatalf("a short wallet must offer the cart: %v", callbackData(short))
	}
	covered := addonConfirmView(LocaleEnglish, subscription, addon, charge, 50000, "2")
	if strings.Contains(strings.Join(callbackData(covered), " "), "addon-cart") {
		t.Fatal("a wallet that covers the add-on needs no cart")
	}
	if !commerceActions["addon-cart"] {
		t.Fatal("saving an add-on for later must be routable")
	}
}

func TestASavedAddonCartNamesTheAddon(t *testing.T) {
	t.Parallel()
	cart := commercepg.Cart{Cart: dbgen.Cart{
		Kind: "addon", AutoPurchase: true, Currency: "RUB",
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(24 * time.Hour), Valid: true},
	}}
	quote := commerce.CartQuote{Currency: "RUB", SubtotalMinor: 12000, TotalMinor: 12000, WalletBalanceMinor: 5000}
	view := cartView(LocaleEnglish, cart, quote, "+10 GB", time.Now())
	if !strings.Contains(view.Text, "Add-on: <b>+10 GB</b>") || strings.Contains(view.Text, "Plan:") {
		t.Fatalf("an add-on cart must name the add-on: %s", view.Text)
	}
}
