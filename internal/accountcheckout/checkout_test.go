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

// plans priced so that "dearer" and "cheaper" are unambiguous, which is what
// makes an upgrade an upgrade rather than a direction the catalogue guessed.
func rankedPlan(planID string, amountMinor int64) planRecord {
	return planRecord{
		offer:         PlanOffer{Kind: "one_time", PlanID: planID, AmountMinor: amountMinor},
		upgradePolicy: "extend", downgradePolicy: "at_expiry",
	}
}

func TestEligibilityRenewsTheHeldPlanAndRanksTheRest(t *testing.T) {
	t.Parallel()
	held, dearer, cheaper := rankedPlan("mid", 500), rankedPlan("high", 900), rankedPlan("low", 100)

	// With nothing yet bought there is only one thing to do.
	first := applyEligibility(held, eligibility{policy: commerce.SubscriptionPolicy{}})
	if len(first.Operations) != 1 || first.Operations[0] != "purchase" {
		t.Fatalf("a first purchase offered %v", first.Operations)
	}

	state := eligibility{
		operations: OperationContext{
			Subscriptions: 1,
			Held:          []HeldPlan{{AmountMinor: 500, PlanID: "mid"}},
		},
		policy: commerce.SubscriptionPolicy{},
	}

	// The plan already held renews. Offering to "upgrade" somebody to the plan
	// they are on is the catalogue talking about itself rather than about them.
	own := applyEligibility(held, state)
	if !own.Held {
		t.Fatalf("the held plan was not marked as held: %+v", own)
	}
	if contains(own.Operations, "purchase") {
		t.Fatalf("a single-subscription installation offered a second purchase: %v", own.Operations)
	}
	if !contains(own.Operations, "extension") {
		t.Fatalf("the held plan did not offer a renewal: %v", own.Operations)
	}
	for _, operation := range []string{"upgrade", "downgrade"} {
		if contains(own.Operations, operation) {
			t.Fatalf("the held plan offered %s to itself: %v", operation, own.Operations)
		}
	}

	// A dearer plan is an upgrade and only an upgrade; a cheaper one only a
	// downgrade. Neither renews something the customer does not have.
	up := applyEligibility(dearer, state)
	if !contains(up.Operations, "upgrade") || contains(up.Operations, "downgrade") ||
		contains(up.Operations, "extension") {
		t.Fatalf("a dearer plan offered %v", up.Operations)
	}
	down := applyEligibility(cheaper, state)
	if !contains(down.Operations, "downgrade") || contains(down.Operations, "upgrade") ||
		contains(down.Operations, "extension") {
		t.Fatalf("a cheaper plan offered %v", down.Operations)
	}

	// A forbidding policy removes exactly the operation it forbids, and nothing
	// invents a replacement for it: a dearer plan whose upgrade is forbidden is a
	// plan this customer cannot move to, and it says so rather than offering a
	// downgrade to something that costs more.
	dearer.upgradePolicy = "forbid"
	restricted := applyEligibility(dearer, state)
	if len(restricted.Operations) != 0 {
		t.Fatalf("a forbidden upgrade left something behind: %v", restricted.Operations)
	}
	if restricted.Eligible || restricted.IneligibleReason == "" {
		t.Fatalf("a plan with no available operation was not explained: %+v", restricted)
	}

	// The forbidden upgrade is not a blanket refusal: the cheaper plan still
	// offers its downgrade.
	cheaper.upgradePolicy = "forbid"
	stillDown := applyEligibility(cheaper, state)
	if !contains(stillDown.Operations, "downgrade") {
		t.Fatalf("forbidding an upgrade removed an unrelated downgrade: %v", stillDown.Operations)
	}
}

func TestEligibilityOffersAnAdditionalSubscriptionOnlyWithinThePolicy(t *testing.T) {
	t.Parallel()
	record := rankedPlan("mid", 500)
	held := []HeldPlan{{AmountMinor: 500, PlanID: "mid"}}
	policy := commerce.SubscriptionPolicy{MultiEnabled: true, MaxPerCustomer: 2}
	offer := applyEligibility(record, eligibility{
		operations: OperationContext{Subscriptions: 1, Held: held, AdditionalAllowed: true}, policy: policy,
	})
	if !contains(offer.Operations, "purchase") {
		t.Fatalf("an allowed additional subscription was not offered: %v", offer.Operations)
	}
	// At the limit the purchase disappears, but changing what is already held
	// must not: a customer at their cap can still renew.
	atLimit := applyEligibility(record, eligibility{
		operations: OperationContext{Subscriptions: 2, Held: held, AdditionalAllowed: false}, policy: policy,
	})
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
