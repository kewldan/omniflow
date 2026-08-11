package goods

import (
	"errors"
	"testing"
	"time"
)

func TestNormalizeRecipientAcceptsTheFormsPeopleActuallyPaste(t *testing.T) {
	for _, input := range []string{
		"omniflow_user",
		"@omniflow_user",
		" @omniflow_user ",
		"t.me/omniflow_user",
		"https://t.me/omniflow_user",
		"https://t.me/omniflow_user?start=1",
		"telegram.me/omniflow_user/",
	} {
		got, err := NormalizeRecipient(input)
		if err != nil {
			t.Fatalf("NormalizeRecipient(%q): %v", input, err)
		}
		if got != "omniflow_user" {
			t.Fatalf("NormalizeRecipient(%q) = %q", input, got)
		}
	}
}

func TestNormalizeRecipientRefusesWhatTelegramWouldNotAccept(t *testing.T) {
	for _, input := range []string{
		"",
		"abc",                                  // shorter than five characters
		"1abcde",                               // must begin with a letter
		"has-a-dash",                           // dashes are not allowed
		"way_too_long_username_for_telegram_x", // longer than thirty-two
		"@",
	} {
		if _, err := NormalizeRecipient(input); !errors.Is(err, ErrInvalidRecipient) {
			t.Fatalf("NormalizeRecipient(%q) should have been refused", input)
		}
	}
}

func TestPriceAppliesMarkupThenRounding(t *testing.T) {
	// A two-decimal currency: 10.00 cost, 15% markup, rounded up to a whole unit.
	rule := PricingRule{Currency: "RUB", MarkupBPS: 1500, Rounding: RoundUnit}
	if got := Price(1000, rule, 2); got != 1200 {
		t.Fatalf("expected 1150 rounded up to 12.00, got %d", got)
	}

	// The markup itself rounds up rather than truncating, so a shop earns the
	// margin its operator configured.
	fine := PricingRule{Currency: "RUB", MarkupBPS: 1, Rounding: RoundNone}
	if got := Price(1000, fine, 2); got != 1001 {
		t.Fatalf("a 0.01%% markup on 1000 must round up to 1001, got %d", got)
	}

	// A zero-exponent currency: unit rounding is a no-op because a minor unit
	// already is a unit.
	stars := PricingRule{Currency: "XTR", MarkupBPS: 1000, Rounding: RoundUnit}
	if got := Price(100, stars, 0); got != 110 {
		t.Fatalf("expected 110 Stars, got %d", got)
	}
}

func TestPriceRoundingSteps(t *testing.T) {
	cases := []struct {
		rounding string
		want     int64
	}{
		{RoundNone, 10_001},
		{RoundMinor, 10_001},
		{RoundUnit, 10_100},
		{RoundTenUnits, 11_000},
		{RoundHundredUnits, 20_000},
	}
	for _, testCase := range cases {
		rule := PricingRule{Currency: "RUB", Rounding: testCase.rounding}
		if got := Price(10_001, rule, 2); got != testCase.want {
			t.Fatalf("rounding %q: got %d, want %d", testCase.rounding, got, testCase.want)
		}
	}

	// An amount already on the step is left alone rather than pushed to the next.
	exact := PricingRule{Currency: "RUB", Rounding: RoundUnit}
	if got := Price(10_000, exact, 2); got != 10_000 {
		t.Fatalf("an exact multiple must not be rounded up, got %d", got)
	}

	// An unrecognised mode behaves as `none` rather than inventing a price.
	future := PricingRule{Currency: "RUB", Rounding: "up_to_the_nearest_moon"}
	if got := Price(10_001, future, 2); got != 10_001 {
		t.Fatalf("an unknown rounding mode must not change the amount, got %d", got)
	}
}

func TestPriceHonoursAFixedOverride(t *testing.T) {
	fixed := int64(49_900)
	rule := PricingRule{Currency: "RUB", MarkupBPS: 5000, Rounding: RoundHundredUnits, FixedAmountMinor: &fixed}
	if got := Price(1_000_000, rule, 2); got != fixed {
		t.Fatalf("a fixed price must ignore both cost and markup, got %d", got)
	}
}

func TestQuoteFreshness(t *testing.T) {
	now := time.Now().UTC()
	if !(Quote{}).Fresh(now) {
		t.Fatal("an adapter that publishes no expiry must not be treated as stale")
	}
	if (Quote{ExpiresAt: now.Add(-time.Second)}).Fresh(now) {
		t.Fatal("an expired quote must not be fresh")
	}
	if !(Quote{ExpiresAt: now.Add(time.Minute)}).Fresh(now) {
		t.Fatal("a live quote must be fresh")
	}
}

func TestEveryFailureClassHasExactlyOneResolution(t *testing.T) {
	// Each class resolves in exactly one way: retried, refunded, or parked for
	// a person. A class that matched two of these would let one code path
	// refund an order another path is still retrying.
	classes := []string{
		FailureRetryable, FailurePermanent, FailureRecipientInvalid,
		FailureProviderBalance, FailureProviderUnavailable, FailureAmbiguous,
	}
	for _, class := range classes {
		resolutions := 0
		for _, resolved := range []bool{Retryable(class), Refundable(class), NeedsReview(class)} {
			if resolved {
				resolutions++
			}
		}
		if resolutions != 1 {
			t.Fatalf("class %q has %d resolutions, want exactly 1", class, resolutions)
		}
	}
}

// An ambiguous outcome is the one an automated rule must not resolve: both
// answers can be wrong in a way that costs somebody money.
func TestAmbiguousIsNeitherRetriedNorRefunded(t *testing.T) {
	if Retryable(FailureAmbiguous) {
		t.Fatal("retrying an ambiguous purchase could deliver and charge twice")
	}
	if Refundable(FailureAmbiguous) {
		t.Fatal("refunding an ambiguous purchase could give money back for delivered goods")
	}
	if !NeedsReview(FailureAmbiguous) {
		t.Fatal("an ambiguous purchase must be parked for an operator")
	}
}

func TestBackoffGrowsAndIsCapped(t *testing.T) {
	if got := Backoff(0); got != time.Minute {
		t.Fatalf("a nonsensical attempt number must not produce a zero delay, got %v", got)
	}
	if got := Backoff(1); got != time.Minute {
		t.Fatalf("the first retry waits a minute, got %v", got)
	}
	if got := Backoff(3); got != 4*time.Minute {
		t.Fatalf("expected doubling, got %v", got)
	}
	if got := Backoff(MaxAttempts + 10); got != time.Hour {
		t.Fatalf("the schedule must be capped at an hour, got %v", got)
	}
}
