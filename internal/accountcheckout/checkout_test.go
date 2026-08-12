package accountcheckout

import (
	"errors"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/accountpg"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/commercepg"
)

func TestToggleSquadAddsAndRemovesWithoutDisturbingTheRest(t *testing.T) {
	t.Parallel()
	selected := ToggleSquad([]string{"a", "b"}, "c")
	if len(selected) != 3 || selected[2] != "c" {
		t.Fatalf("adding a squad produced %v", selected)
	}
	selected = ToggleSquad(selected, "b")
	if len(selected) != 2 || selected[0] != "a" || selected[1] != "c" {
		t.Fatalf("removing a squad produced %v", selected)
	}
	// Toggling the same squad twice must return to where it started, or a double
	// tap on a slow connection would leave a selection the customer never chose.
	if again := ToggleSquad(ToggleSquad(selected, "b"), "b"); len(again) != 2 {
		t.Fatalf("a double toggle produced %v", again)
	}
}

func TestPreferredCurrencyFavoursTheInstallationDefault(t *testing.T) {
	t.Parallel()
	option := commerce.PaymentOption{Provider: "cryptobot", Enabled: true, Currencies: []string{"USD", "RUB"}}
	if got := PreferredCurrency(option, []string{"RUB", "USD"}, "USD"); got != "USD" {
		t.Fatalf("PreferredCurrency = %q, want USD", got)
	}
	if got := PreferredCurrency(option, []string{"RUB"}, "USD"); got != "RUB" {
		t.Fatalf("PreferredCurrency fallback = %q, want RUB", got)
	}
	stars := commerce.PaymentOption{Provider: "telegram_stars", Enabled: true, Currencies: []string{"XTR"}}
	if got := PreferredCurrency(stars, []string{"RUB"}, "RUB"); got != "" {
		t.Fatalf("an incompatible adapter must be skipped, got %q", got)
	}
	if got := PreferredCurrency(commerce.PaymentOption{Provider: "manual"}, []string{"RUB"}, "RUB"); got != "" {
		t.Fatalf("a disabled adapter must be skipped, got %q", got)
	}
}

func TestHandoffDistinguishesHostedTelegramAndManual(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		provider string
		url      string
		want     string
	}{
		{"yookassa", "https://example.test/pay", HandoffHosted},
		{"telegram_stars", "https://t.me/invoice", HandoffTelegram},
		{"manual", "", HandoffManual},
		{"manual", "https://example.test/pay", HandoffManual},
		{"cryptobot", "", HandoffNone},
	} {
		if got := HandoffFor(testCase.provider, testCase.url); got != testCase.want {
			t.Fatalf("HandoffFor(%q, %q) = %q, want %q",
				testCase.provider, testCase.url, got, testCase.want)
		}
	}
}

func TestPromoRejectionOnlyClaimsPromotionFailures(t *testing.T) {
	t.Parallel()
	for reason, err := range map[string]error{
		PromoUnknown:    commercepg.ErrPromoUnknown,
		PromoIneligible: commercepg.ErrPromoIneligible,
		PromoExhausted:  commercepg.ErrPromoExhausted,
		PromoInvalid:    commercepg.ErrPromoInvalid,
	} {
		if got := PromoRejection(err); got != reason {
			t.Fatalf("PromoRejection(%v) = %q, want %q", err, got, reason)
		}
	}
	// A plan that vanished is not a promo rejection. Reporting it as one would
	// tell the customer to try a different code for a problem no code can fix.
	if got := PromoRejection(commercepg.ErrPlanUnavailable); got != "" {
		t.Fatalf("PromoRejection of an unrelated failure = %q", got)
	}
	if got := PromoRejection(nil); got != "" {
		t.Fatalf("PromoRejection(nil) = %q", got)
	}
}

func TestEligibilityOffersEveryLifecycleOperationTheCatalogueAllows(t *testing.T) {
	t.Parallel()
	record := planRecord{
		offer:         PlanOffer{Kind: "one_time"},
		upgradePolicy: "extend", downgradePolicy: "at_expiry",
	}
	// With nothing yet bought there is only one thing to do.
	first := applyEligibility(record, eligibility{policy: commerce.SubscriptionPolicy{}})
	if len(first.Operations) != 1 || first.Operations[0] != "purchase" {
		t.Fatalf("a first purchase offered %v", first.Operations)
	}

	// With a subscription in hand and concurrency off, buying again extends what
	// exists rather than opening a second subscription.
	existing := applyEligibility(record, eligibility{
		activeSubscriptions: 1, policy: commerce.SubscriptionPolicy{},
	})
	if contains(existing.Operations, "purchase") {
		t.Fatalf("a single-subscription installation offered a second purchase: %v", existing.Operations)
	}
	for _, operation := range []string{"extension", "upgrade", "downgrade"} {
		if !contains(existing.Operations, operation) {
			t.Fatalf("%s was not offered: %v", operation, existing.Operations)
		}
	}

	// A forbidding policy removes exactly the operation it forbids.
	record.upgradePolicy = "forbid"
	restricted := applyEligibility(record, eligibility{
		activeSubscriptions: 1, policy: commerce.SubscriptionPolicy{},
	})
	if contains(restricted.Operations, "upgrade") {
		t.Fatalf("a forbidden upgrade was offered: %v", restricted.Operations)
	}
	if !contains(restricted.Operations, "downgrade") {
		t.Fatalf("forbidding an upgrade removed the downgrade too: %v", restricted.Operations)
	}
}

func TestEligibilityOffersAnAdditionalSubscriptionOnlyWithinThePolicy(t *testing.T) {
	t.Parallel()
	record := planRecord{offer: PlanOffer{Kind: "one_time"}, upgradePolicy: "extend", downgradePolicy: "at_expiry"}
	policy := commerce.SubscriptionPolicy{MultiEnabled: true, MaxPerCustomer: 2}
	if offer := applyEligibility(record, eligibility{activeSubscriptions: 1, policy: policy}); !contains(offer.Operations, "purchase") {
		t.Fatalf("an allowed additional subscription was not offered: %v", offer.Operations)
	}
	// At the limit the purchase disappears, but changing what is already held
	// must not: a customer at their cap can still renew or upgrade.
	atLimit := applyEligibility(record, eligibility{activeSubscriptions: 2, policy: policy})
	if contains(atLimit.Operations, "purchase") {
		t.Fatalf("a purchase was offered past the limit: %v", atLimit.Operations)
	}
	if !contains(atLimit.Operations, "extension") {
		t.Fatalf("a customer at their limit lost the ability to renew: %v", atLimit.Operations)
	}
}

func TestEligibilityExplainsWhyATrialIsRefused(t *testing.T) {
	t.Parallel()
	record := planRecord{offer: PlanOffer{Kind: "trial"}, trialRule: commerce.TrialNewCustomer}
	offered := applyEligibility(record, eligibility{trial: commerce.TrialRequest{}})
	if !offered.Eligible || len(offered.Operations) != 1 {
		t.Fatalf("an eligible trial was not offered: %+v", offered)
	}
	// A refused trial is still shown, carrying the reason. Hiding it would leave
	// the customer wondering where the free period went.
	refused := applyEligibility(record, eligibility{
		trial: commerce.TrialRequest{AlreadyClaimed: true},
	})
	if refused.Eligible || refused.IneligibleReason != "trial_already_used" {
		t.Fatalf("a claimed trial reported %+v", refused)
	}
}

func TestQuoteCarriesTheWholeBreakdownIncludingTheRejection(t *testing.T) {
	t.Parallel()
	preview := commercepg.OrderQuote{
		DiscountMinor: 500, WalletBalance: 3000, WalletMinor: 2000, ExternalMinor: 7500, AddonMinor: 1000,
	}
	quote := quoteFrom(preview, Session{PromoCode: "SPRING", PromoRejection: PromoExhausted})
	if quote.DiscountMinor != 500 || quote.WalletBalanceMinor != 3000 ||
		quote.WalletAppliedMinor != 2000 || quote.ExternalMinor != 7500 || quote.AddonMinor != 1000 {
		t.Fatalf("the breakdown was not carried through: %+v", quote)
	}
	if quote.PromoCode != "SPRING" || quote.PromoRejection != PromoExhausted {
		t.Fatalf("the promotion state was not carried through: %+v", quote)
	}
}

func TestPaginationHelpersStayInsideWhatAPanelCanRender(t *testing.T) {
	t.Parallel()
	if got := boundLimit(0); got != 20 {
		t.Fatalf("an absent limit = %d, want the default 20", got)
	}
	if got := boundLimit(5_000); got != 100 {
		t.Fatalf("an oversized limit = %d, want it capped at 100", got)
	}
	if got := boundLimit(7); got != 7 {
		t.Fatalf("a reasonable limit was changed to %d", got)
	}
	if optionalTime(time.Time{}) != nil {
		t.Fatal("an absent cursor must be SQL NULL")
	}
	if optionalTime(time.Now()) == nil {
		t.Fatal("a present cursor must be sent")
	}
}

func TestLocaleFallsBackToEnglishRatherThanToAnEmptyCatalogue(t *testing.T) {
	t.Parallel()
	for input, want := range map[string]string{
		"ru": "ru", " RU ": "ru", "en": "en", "": "en", "de": "en",
	} {
		if got := normalizeLocale(input); got != want {
			t.Fatalf("normalizeLocale(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestNotFoundStatesAreIndistinguishableFromTheOutside(t *testing.T) {
	t.Parallel()
	// "You have no checkout", "that order is not yours", and "that plan is gone"
	// must all reach the transport as the same kind of answer, so an identifier
	// cannot be probed for somebody else's records.
	for _, err := range []error{ErrNoCheckout, ErrOrderNotFound, ErrPlanUnavailable} {
		if !errors.Is(err, accountpg.ErrNotFound) {
			t.Fatalf("%v does not read as a not-found state", err)
		}
	}
}

func TestTopUpRejectionRecoversTheMachineReason(t *testing.T) {
	t.Parallel()
	limits := commerce.TopUpLimits{Enabled: true, MinimumMinor: 1000, MaximumMinor: 100000}
	_, err := limits.Validate(10, 0)
	wrapped := errors.New("wallet top-up rejected: " + commerce.TopUpBelowMinimum)
	if err == nil {
		t.Fatal("a below-minimum top-up must be refused")
	}
	if got := TopUpRejection(wrapped); got != commerce.TopUpBelowMinimum {
		t.Fatalf("TopUpRejection = %q, want %q", got, commerce.TopUpBelowMinimum)
	}
	if got := TopUpRejection(nil); got != "" {
		t.Fatalf("TopUpRejection(nil) = %q", got)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
