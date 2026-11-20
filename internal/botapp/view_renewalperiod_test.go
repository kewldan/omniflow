package botapp

import (
	"strings"
	"testing"

	"github.com/omniflow/omniflow/internal/recurring"
)

func TestAutoRenewOffersTheRenewalPeriodsAndMarksTheChoice(t *testing.T) {
	t.Parallel()
	settings := RenewalSettings{
		AutoRenew: AutoRenew{Enabled: true, SubscriptionID: renewTestSubscription, Provider: "manual", Currency: "RUB"},
		Funding:   recurring.FundingWallet, Consented: true, PeriodDays: 90,
	}
	view := autoRenewSettingsView(LocaleEnglish, autoRenewScreen{
		Settings: settings, PlanName: "Pro", Supported: true,
		Periods: []RenewalPeriod{{Days: 30, AmountMinor: 25000, Own: true}, {Days: 90, AmountMinor: 60000}},
	})
	callbacks := strings.Join(callbackData(view), " ")
	for _, want := range []string{"renew-days:0:", "renew-days:30:", "renew-days:90:"} {
		if !strings.Contains(callbacks, actionPrefix+want+renewTestSubscription) {
			t.Fatalf("missing %s in %s", want, callbacks)
		}
	}
	for _, data := range callbackData(view) {
		if len(data) > 64 {
			t.Fatalf("callback %q is longer than Telegram accepts", data)
		}
	}
	if !strings.Contains(buttonLabels(view), "• 90 d") || !strings.Contains(view.Text, "Renewal period") {
		t.Fatalf("the chosen period is not marked: %s / %s", buttonLabels(view), view.Text)
	}

	single := autoRenewSettingsView(LocaleEnglish, autoRenewScreen{
		Settings: settings, PlanName: "Pro", Supported: true,
		Periods: []RenewalPeriod{{Days: 30, AmountMinor: 25000, Own: true}},
	})
	if strings.Contains(strings.Join(callbackData(single), " "), "renew-days") {
		t.Fatal("a plan with one renewal period offers no choice")
	}
}
