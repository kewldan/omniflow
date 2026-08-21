package commerce

import (
	"errors"
	"testing"
	"time"
)

// "Buy now" is the customer's own tap. The automatic-purchase switch governs
// the unattended sweep and must not refuse a customer who is present.
func TestManualCartPurchaseIgnoresTheAutomaticSwitch(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	request := AutoPurchaseRequest{Enabled: false, Now: now, ExpiresAt: now.Add(time.Hour), Quote: coveredQuote()}
	if reason, err := EvaluateCartPurchase(request); err != nil || reason != "ready" {
		t.Fatalf("a covered cart must be buyable by hand with automatic purchase off, got %q %v", reason, err)
	}
	if reason, err := EvaluateAutoPurchase(request); !errors.Is(err, ErrCartRejected) || reason != CartAutoPurchaseOff {
		t.Fatalf("the sweep must still respect the switch, got %q %v", reason, err)
	}
}

// Every other refusal applies to a manual purchase exactly as it does to the
// sweep: a stale price, an expired cart, maintenance, and a short balance are
// facts about the cart, not about who is asking.
func TestManualCartPurchaseKeepsEveryOtherRefusal(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	short := coveredQuote()
	short.WalletBalanceMinor = 1
	cases := []struct {
		name    string
		request AutoPurchaseRequest
		reason  string
	}{
		{"expired", AutoPurchaseRequest{Now: now, ExpiresAt: now.Add(-time.Second), Quote: coveredQuote()}, CartExpired},
		{"maintenance", AutoPurchaseRequest{Now: now, Quote: coveredQuote(), Maintenance: true}, CartMaintenance},
		{"short balance", AutoPurchaseRequest{Now: now, Quote: short}, CartInsufficientBalance},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			reason, err := EvaluateCartPurchase(testCase.request)
			if !errors.Is(err, ErrCartRejected) {
				t.Fatalf("expected a rejection, got %v", err)
			}
			if reason != testCase.reason {
				t.Fatalf("expected %q, got %q", testCase.reason, reason)
			}
		})
	}
}
