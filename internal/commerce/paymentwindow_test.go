package commerce

import (
	"testing"
	"time"
)

// A manual transfer is confirmed by a person, in days; everything else is a
// checkout a customer completes in minutes. The window follows the provider,
// and an unknown or absent provider gets the short default rather than the
// long one.
func TestPaymentWindowFollowsTheProvider(t *testing.T) {
	if got := PaymentWindow(ManualProvider); got != ManualPaymentWindow {
		t.Fatalf("manual window = %s", got)
	}
	for _, provider := range []string{"", "cryptobot", "yookassa", "telegram_stars", "something_new"} {
		if got := PaymentWindow(provider); got != DefaultPaymentWindow {
			t.Fatalf("window for %q = %s, want the default %s", provider, got, DefaultPaymentWindow)
		}
	}
	if ManualPaymentWindow <= DefaultPaymentWindow || ManualPaymentWindow < 48*time.Hour {
		t.Fatalf("the manual window %s must be days, not hours", ManualPaymentWindow)
	}
}
