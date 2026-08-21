package botapp

import (
	"strings"
	"testing"

	"github.com/omniflow/omniflow/internal/commerce"
)

func addonFixture() (SubscriptionSummary, Addon) {
	subscription := SubscriptionSummary{ID: "22222222-2222-2222-2222-222222222222", Slot: 2, Label: "Office", Found: true, Status: "active"}
	addon := Addon{AddonVersionID: "33333333-3333-3333-3333-333333333333", Name: "Extra traffic", Description: "+100 GiB", Currency: "RUB", AmountMinor: 30000, MaxQuantity: 1, Proration: commerce.ProrationRemainingPeriod}
	return subscription, addon
}

func TestAddonListLeadsToAConfirmationWithinTheCallbackLimit(t *testing.T) {
	t.Parallel()
	subscription, addon := addonFixture()
	view := addonListView(LocaleEnglish, subscription, []Addon{addon})
	callbacks := callbackData(view)
	if callbacks[0] != actionPrefix+"addon-buy:"+addon.AddonVersionID+":2" {
		t.Fatalf("an add-on opens its confirmation for the subscription's slot: %v", callbacks)
	}
	for _, data := range callbacks {
		if len(data) > 64 {
			t.Fatalf("Telegram refuses callback data over 64 bytes: %q is %d", data, len(data))
		}
	}
}

func TestAddonConfirmationStatesTheChargeAndItsSource(t *testing.T) {
	t.Parallel()
	subscription, addon := addonFixture()
	charge := commerce.AddonCharge{ChargedMinor: 12000, Quantity: 1, Proration: addon.Proration}
	covered := addonConfirmView(LocaleEnglish, subscription, addon, charge, 50000, "2")
	if !strings.Contains(covered.Text, "120 RUB") || !strings.Contains(covered.Text, "Paid from your wallet") {
		t.Fatalf("the prorated charge and the wallet source must be stated: %s", covered.Text)
	}
	if !strings.Contains(covered.Text, "in proportion to the time left") {
		t.Fatalf("the proration rule must be stated: %s", covered.Text)
	}
	callbacks := callbackData(covered)
	if callbacks[0] != actionPrefix+"addon-confirm:"+addon.AddonVersionID+":2" {
		t.Fatalf("confirming must name the add-on and the slot: %v", callbacks)
	}
	if callbacks[1] != actionPrefix+"addons:"+subscription.ID {
		t.Fatalf("cancelling must return to the add-on list: %v", callbacks)
	}
	for _, data := range callbacks {
		if len(data) > 64 {
			t.Fatalf("Telegram refuses callback data over 64 bytes: %q is %d", data, len(data))
		}
	}
	partial := addonConfirmView(LocaleEnglish, subscription, addon, charge, 5000, "2")
	if !strings.Contains(partial.Text, "50 RUB comes from your wallet") || !strings.Contains(partial.Text, "70 RUB") {
		t.Fatalf("a partial wallet must say what remains: %s", partial.Text)
	}
	none := addonConfirmView(LocaleRussian, subscription, addon, charge, 0, "2")
	if !strings.Contains(none.Text, "на следующем шаге") {
		t.Fatalf("with no wallet the method is chosen next: %s", none.Text)
	}
	if !commerceActions["addon-confirm"] || !consequentialActions["addon-confirm"] {
		t.Fatal("confirming an add-on must be routable and claimed once")
	}
	if consequentialActions["addon-buy"] {
		t.Fatal("opening the confirmation moves no money and must not be claimed")
	}
}

func TestUnpaidGiftOffersToPickAPaymentMethod(t *testing.T) {
	t.Parallel()
	purchase := GiftPurchase{OrderID: "abcdef12-0000-0000-0000-000000000000", Code: "ABCD-EFGH", CodeHint: "EFGH"}
	view := giftCodeView(LocaleEnglish, purchase, OrderSummary{ID: purchase.OrderID, State: commerce.OrderPending, ExternalMinor: 100})
	callbacks := strings.Join(callbackData(view), " ")
	if !strings.Contains(callbacks, actionPrefix+"pay:"+purchase.OrderID+":pick") {
		t.Fatalf("an unpaid gift must let the sender pick how to pay: %s", callbacks)
	}
	paid := giftCodeView(LocaleEnglish, purchase, OrderSummary{ID: purchase.OrderID, State: commerce.OrderPaid})
	if strings.Contains(strings.Join(callbackData(paid), " "), actionPrefix+"pay:") {
		t.Fatal("a gift the wallet covered has nothing to pay")
	}
}
