package botapp

import (
	"strings"
	"testing"

	"github.com/omniflow/omniflow/internal/accountcheckout"
	"github.com/omniflow/omniflow/internal/commercepg"
)

func TestCapacityPreviewStatesChargeCreditAndWallet(t *testing.T) {
	t.Parallel()
	subscription := SubscriptionSummary{ID: "sub", Label: "Home", Slot: 1, Found: true}
	quote := commercepg.CapacityChangeQuote{
		Currency: "RUB", ChargeMinor: 5000, CreditMinor: 8000, CreditAppliedMinor: 5000, CreditToWalletMinor: 3000,
	}
	view := capacityConfirmView(LocaleEnglish, subscription, "Switch", quote, "tpk-buy:1:-")
	for _, fragment := range []string{"50 RUB", "−80 RUB", "Nothing is left to pay", "Back to your wallet: <b>30 RUB</b>"} {
		if !strings.Contains(view.Text, fragment) {
			t.Fatalf("preview is missing %q: %s", fragment, view.Text)
		}
	}
	if got := view.Keyboard.InlineKeyboard[0][0].CallbackData; !strings.HasSuffix(got, "tpk-buy:1:-") {
		t.Fatalf("confirm button = %q", got)
	}
}

func TestTrafficPackagesListEveryOptionWithItsDirection(t *testing.T) {
	t.Parallel()
	subscription := SubscriptionSummary{ID: "sub", Label: "Home", Slot: 2, Found: true}
	packages := accountcheckout.TrafficPackages{Available: true, Currency: "RUB", CurrentTrafficBytes: 50 << 30, Options: []accountcheckout.TrafficPackageOption{
		{Direction: "down"},
		{AddonVersionID: "00000000-0000-0000-0000-000000000001", Name: "Big", TrafficBytes: 100 << 30, Direction: "up"},
	}}
	view := trafficPackagesView(LocaleEnglish, subscription, packages, "2")
	labels := buttonLabels(view)
	for _, fragment := range []string{"⬇️ No package", "⬆️ Big"} {
		if !strings.Contains(labels, fragment) {
			t.Fatalf("options are missing %q: %s", fragment, labels)
		}
	}
	for _, button := range view.Keyboard.InlineKeyboard {
		if len(button[0].CallbackData) > 64 {
			t.Fatalf("callback data is longer than Telegram allows: %q", button[0].CallbackData)
		}
	}
}
