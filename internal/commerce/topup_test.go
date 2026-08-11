package commerce

import (
	"errors"
	"testing"
	"time"
)

func limits() TopUpLimits {
	return TopUpLimits{
		Enabled: true, Presets: []int64{10000, 50000, 500000},
		MinimumMinor: 10000, MaximumMinor: 100000,
		WindowLimitMinor: 150000, Window: 24 * time.Hour,
	}
}

func TestTopUpLimitsRejectEveryOutOfBoundsAmount(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		limits   TopUpLimits
		amount   int64
		credited int64
		reason   string
	}{
		{"disabled", TopUpLimits{}, 10000, 0, TopUpDisabled},
		{"zero", limits(), 0, 0, TopUpInvalidAmount},
		{"negative", limits(), -1, 0, TopUpInvalidAmount},
		{"below minimum", limits(), 9999, 0, TopUpBelowMinimum},
		{"above maximum", limits(), 100001, 0, TopUpAboveMaximum},
		{"window exhausted", limits(), 100000, 100000, TopUpWindowExceeded},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			reason, err := testCase.limits.Validate(testCase.amount, testCase.credited)
			if !errors.Is(err, ErrTopUpRejected) {
				t.Fatalf("expected a rejection, got %v", err)
			}
			if reason != testCase.reason {
				t.Fatalf("expected reason %q, got %q", testCase.reason, reason)
			}
		})
	}
}

func TestTopUpLimitsAcceptAnAmountInsideEveryBound(t *testing.T) {
	t.Parallel()
	reason, err := limits().Validate(50000, 50000)
	if err != nil || reason != "accepted" {
		t.Fatalf("expected acceptance, got %q %v", reason, err)
	}
}

// A window that is exactly full must refuse the next minor unit, not round in
// the customer's favour.
func TestTopUpWindowBoundaryIsExclusive(t *testing.T) {
	t.Parallel()
	if _, err := limits().Validate(50000, 100000); err != nil {
		t.Fatalf("an amount that exactly fills the window must be accepted: %v", err)
	}
	if _, err := limits().Validate(50001, 100000); !errors.Is(err, ErrTopUpRejected) {
		t.Fatal("one minor unit past the window must be refused")
	}
}

func TestOfferedPresetsHideAmountsThatWouldBeRefused(t *testing.T) {
	t.Parallel()
	offered := limits().OfferedPresets(0)
	if len(offered) != 2 || offered[0] != 10000 || offered[1] != 50000 {
		t.Fatalf("a preset above the maximum must not be offered: %v", offered)
	}
	if len(limits().OfferedPresets(150000)) != 0 {
		t.Fatal("an exhausted window must offer no preset at all")
	}
}

func TestMinimumIsNeverBelowOneMinorUnit(t *testing.T) {
	t.Parallel()
	if got := (TopUpLimits{Enabled: true}).Minimum(); got != 1 {
		t.Fatalf("expected a floor of 1 minor unit, got %d", got)
	}
}

// An overpayment and an underpayment both credit exactly what arrived, because
// a wallet owes nothing beyond the money itself.
func TestSettleTopUpCreditsWhatWasActuallyReceived(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name                    string
		requested, received     int64
		credited                int64
		expectedClassifications string
	}{
		{"exact", 50000, 50000, 50000, "paid"},
		{"over", 50000, 60000, 60000, "overpayment"},
		{"under", 50000, 40000, 40000, "underpayment"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			settlement, err := SettleTopUp(testCase.requested, testCase.received)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if settlement.CreditedMinor != testCase.credited {
				t.Fatalf("expected %d credited, got %d", testCase.credited, settlement.CreditedMinor)
			}
			if settlement.Classification != testCase.expectedClassifications {
				t.Fatalf("expected classification %q, got %q", testCase.expectedClassifications, settlement.Classification)
			}
		})
	}
}

func TestSettleTopUpRejectsANonPositiveRequest(t *testing.T) {
	t.Parallel()
	if _, err := SettleTopUp(0, 100); !errors.Is(err, ErrInvalidAmount) {
		t.Fatalf("expected an invalid-amount error, got %v", err)
	}
}
