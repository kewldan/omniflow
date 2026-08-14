package botapp

import (
	"strings"
	"testing"

	"github.com/omniflow/omniflow/internal/notice"
)

// The bot side of operator wording.
//
// The point worth asserting is the fallback direction. An installation that has
// overridden nothing must behave exactly as it did before this existed, and a
// notifier whose override lookup failed must still send its notices — a
// customer whose subscription expires tomorrow does not care whose words warned
// them.

func TestNoOverrideMeansTheShippedWording(t *testing.T) {
	var none notices // the zero value, which is what a failed refresh leaves

	for _, locale := range []Locale{LocaleEnglish, LocaleRussian} {
		rendered := none.text(locale, notice.CodeExpiry, map[string]string{"days": "3"})
		if rendered == "" {
			t.Fatalf("the %s expiry notice rendered empty with no overrides", locale)
		}
		if !strings.Contains(rendered, "3") {
			t.Fatalf("the value was not substituted: %q", rendered)
		}
		if strings.ContainsAny(rendered, "{}") {
			t.Fatalf("a placeholder survived: %q", rendered)
		}
	}
}

func TestAnOverrideReplacesOnlyItsOwnLocale(t *testing.T) {
	set := notices{overrides: map[string]string{
		"expiry/en": "Access ends in {days} days. Renew today.",
	}}

	english := set.text(LocaleEnglish, notice.CodeExpiry, map[string]string{"days": "1"})
	if english != "Access ends in 1 days. Renew today." {
		t.Fatalf("the English override rendered as %q", english)
	}

	definition, _ := notice.Lookup("expiry")
	russian := set.text(LocaleRussian, notice.CodeExpiry, map[string]string{"days": "1"})
	if russian != notice.Render(definition.Default["ru"], map[string]string{"days": "1"}) {
		t.Fatalf("overriding English changed the Russian notice: %q", russian)
	}
}

// Every alert view has to produce a message. A view that renders empty is a
// message Telegram refuses, which turns a wording change into a delivery
// failure for every customer at once.
func TestEveryAlertViewStillProducesAMessage(t *testing.T) {
	var none notices
	entitlement := Entitlement{PlanName: "Pro", Found: true}

	for name, view := range map[string]View{
		"expiry":      expiryAlertView(none, LocaleEnglish, 3, ""),
		"traffic":     trafficAlertView(none, LocaleEnglish, 85, ""),
		"renewal":     renewalReminderView(none, LocaleEnglish, entitlement, 5),
		"grace":       gracePeriodView(none, LocaleEnglish, entitlement),
		"recovery":    recoveryView(none, LocaleEnglish, entitlement),
		"fulfilled":   fulfillmentAlertView(none, LocaleEnglish, true, ""),
		"unfulfilled": fulfillmentAlertView(none, LocaleEnglish, false, ""),
		"dunning":     dunningAlertView(none, LocaleEnglish, false),
		"abandoned":   dunningAlertView(none, LocaleEnglish, true),
	} {
		if strings.TrimSpace(view.Text) == "" {
			t.Fatalf("the %s alert rendered empty", name)
		}
		if strings.ContainsAny(view.Text, "{}") {
			t.Fatalf("the %s alert leaked a placeholder: %q", name, view.Text)
		}
	}
}

// A plan name is operator-entered and the body is parsed as HTML, so it is
// escaped at substitution. The wording around it was validated when it was
// saved; the value put into it never can be.
func TestAValueIsEscapedEvenThoughTheWordingWasValidated(t *testing.T) {
	var none notices
	view := renewalReminderView(none, LocaleEnglish, Entitlement{
		PlanName: `Pro <script>alert(1)</script>`, Found: true,
	}, 5)
	if strings.Contains(view.Text, "<script>") {
		t.Fatalf("a plan name reached the message unescaped: %q", view.Text)
	}
	if !strings.Contains(view.Text, "&lt;script&gt;") {
		t.Fatalf("the plan name is missing entirely: %q", view.Text)
	}
}
