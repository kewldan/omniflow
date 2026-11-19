package botapp

import (
	"testing"

	"github.com/omniflow/omniflow/internal/payments"
)

// Every adapter Omniflow can register needs a button label in both languages;
// a missing one would render the raw message key on the payment screen.
func TestEveryPaymentProviderHasALabel(t *testing.T) {
	t.Parallel()
	for _, slug := range payments.Slugs() {
		for _, locale := range []Locale{"ru", "en"} {
			key := "pay." + slug
			if label := text(locale, key); label == key || label == "" {
				t.Errorf("no %s label for provider %q", locale, slug)
			}
		}
	}
}
