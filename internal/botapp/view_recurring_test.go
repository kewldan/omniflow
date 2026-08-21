package botapp

import (
	"strings"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/recurring"
)

const renewTestSubscription = "22222222-2222-2222-2222-222222222222"

// callbackData flattens every callback a view renders, so a test can assert on
// what a tap would send rather than on what the label says.
func callbackData(view View) []string {
	data := make([]string, 0, 8)
	if view.Keyboard == nil {
		return data
	}
	for _, buttonRow := range view.Keyboard.InlineKeyboard {
		for _, button := range buttonRow {
			if button.CallbackData != "" {
				data = append(data, button.CallbackData)
			}
		}
	}
	return data
}

func TestAutoRenewSwitchOnNamesTheSubscription(t *testing.T) {
	t.Parallel()
	view := autoRenewSettingsView(LocaleEnglish, autoRenewScreen{
		Settings:  RenewalSettings{AutoRenew: AutoRenew{SubscriptionID: renewTestSubscription}, Funding: recurring.FundingWallet},
		PlanName:  "Pro",
		Supported: true,
	})
	callbacks := strings.Join(callbackData(view), " ")
	if !strings.Contains(callbacks, actionPrefix+"autorenew:on:"+renewTestSubscription) {
		t.Fatalf("arming auto-renew must carry the subscription it targets: %s", callbacks)
	}
	if strings.Contains(buttonLabels(view), "Payment methods") {
		t.Fatalf("with nothing saved there is no method list to open: %s", buttonLabels(view))
	}
}

func TestAutoRenewWithoutAPlanOffersNothingToSwitchOn(t *testing.T) {
	t.Parallel()
	view := autoRenewSettingsView(LocaleEnglish, autoRenewScreen{
		Settings: RenewalSettings{Funding: recurring.FundingWallet}, Supported: true,
	})
	for _, data := range callbackData(view) {
		if strings.HasPrefix(data, actionPrefix+"autorenew:on") {
			t.Fatalf("a customer with no subscription cannot arm auto-renew: %s", data)
		}
	}
}

func TestAutoRenewEnabledScreenHidesTheSavedMethodWhenNoneIsSaved(t *testing.T) {
	t.Parallel()
	enabled := RenewalSettings{
		AutoRenew: AutoRenew{Enabled: true, Provider: "yookassa", SubscriptionID: renewTestSubscription},
		Funding:   recurring.FundingWallet, LeadTime: 3 * 24 * time.Hour, State: recurring.StateScheduled,
	}
	view := autoRenewSettingsView(LocaleEnglish, autoRenewScreen{Settings: enabled, PlanName: "Pro", Supported: true})
	callbacks := strings.Join(callbackData(view), " ")
	if strings.Contains(callbacks, "renew-funding:saved_method") {
		t.Fatalf("the saved-method source must not be offered with nothing saved: %s", callbacks)
	}
	if !strings.Contains(view.Text, "No card is saved") {
		t.Fatalf("the screen must say the wallet is the only source: %s", view.Text)
	}
	for _, expected := range []string{
		"renew-funding:wallet:" + renewTestSubscription,
		"renew-lead:1:" + renewTestSubscription,
		"renew-lead:3:" + renewTestSubscription,
		"renew-lead:7:" + renewTestSubscription,
		"autorenew:off:" + renewTestSubscription,
	} {
		if !strings.Contains(callbacks, actionPrefix+expected) {
			t.Fatalf("missing %q in %s", expected, callbacks)
		}
	}
	if !strings.Contains(buttonLabels(view), "• 3 d") {
		t.Fatalf("the current lead time must be marked: %s", buttonLabels(view))
	}
}

func TestAutoRenewEnabledScreenOffersTheSavedMethodWhenOneExists(t *testing.T) {
	t.Parallel()
	enabled := RenewalSettings{
		AutoRenew: AutoRenew{Enabled: true, Provider: "yookassa", SubscriptionID: renewTestSubscription},
		Funding:   recurring.FundingSavedMethod, LeadTime: 24 * time.Hour, MethodLabel: "Visa •• 4242",
	}
	methods := []SavedMethod{{ID: "m1", Provider: "yookassa", Label: "Visa •• 4242", Status: "active", IsDefault: true}}
	view := autoRenewSettingsView(LocaleEnglish, autoRenewScreen{Settings: enabled, Methods: methods, PlanName: "Pro", Supported: true})
	callbacks := strings.Join(callbackData(view), " ")
	if !strings.Contains(callbacks, actionPrefix+"renew-funding:saved_method:"+renewTestSubscription) {
		t.Fatalf("a saved method must be selectable: %s", callbacks)
	}
	if !strings.Contains(callbacks, callbackPrefix+routeMethods) {
		t.Fatalf("the method list must be reachable: %s", callbacks)
	}
	if !strings.Contains(view.Text, "Visa •• 4242") {
		t.Fatalf("the charged method must be named: %s", view.Text)
	}
	if strings.Contains(view.Text, "No card is saved") {
		t.Fatalf("the wallet-only notice is wrong with a card on file: %s", view.Text)
	}
}

func TestAutoRenewEveryRenderedActionIsRoutable(t *testing.T) {
	t.Parallel()
	enabled := RenewalSettings{
		AutoRenew: AutoRenew{Enabled: true, Provider: "yookassa", SubscriptionID: renewTestSubscription},
		Funding:   recurring.FundingWallet,
	}
	methods := []SavedMethod{{ID: "m1", Provider: "yookassa", Status: "active"}, {ID: "m2", Provider: "yookassa", Status: "active", IsDefault: true}}
	views := []View{
		autoRenewSettingsView(LocaleEnglish, autoRenewScreen{Settings: enabled, Methods: methods, PlanName: "Pro", Supported: true}),
		savedMethodsView(LocaleEnglish, methods),
		autoRenewPickerView(LocaleEnglish, []SubscriptionSummary{{ID: renewTestSubscription, Label: "Home"}}),
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

func TestAutoRenewPickerBacksOutToSettings(t *testing.T) {
	t.Parallel()
	view := autoRenewPickerView(LocaleRussian, []SubscriptionSummary{
		{ID: "a", Label: "Home"}, {ID: "b", Label: "Office"},
	})
	labels := buttonLabels(view)
	if !strings.Contains(labels, "Home") || !strings.Contains(labels, "Office") {
		t.Fatalf("every subscription must be offered: %s", labels)
	}
	if !strings.Contains(strings.Join(callbackData(view), " "), callbackPrefix+routeSettings) {
		t.Fatalf("the picker must lead back to settings: %v", callbackData(view))
	}
}

func TestAutoRenewUnsupportedExplainsItself(t *testing.T) {
	t.Parallel()
	view := autoRenewSettingsView(LocaleEnglish, autoRenewScreen{Supported: false})
	if !strings.Contains(view.Text, "No configured payment method supports recurring") {
		t.Fatalf("unsupported auto-renew must be explained: %s", view.Text)
	}
	for _, data := range callbackData(view) {
		if strings.HasPrefix(data, actionPrefix) {
			t.Fatalf("nothing may be switched on without a recurring provider: %s", data)
		}
	}
}
