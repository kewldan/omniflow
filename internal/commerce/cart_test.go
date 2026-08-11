package commerce

import (
	"errors"
	"testing"
	"time"
)

func coveredQuote() CartQuote {
	return CartQuote{Currency: "RUB", SubtotalMinor: 50000, AddonMinor: 10000, TotalMinor: 60000, WalletBalanceMinor: 60000}
}

func TestCartQuoteReportsWhatIsStillMissing(t *testing.T) {
	t.Parallel()
	quote := coveredQuote()
	if !quote.Covered() || quote.MissingMinor() != 0 {
		t.Fatalf("an exactly covered cart must report nothing missing: %+v", quote)
	}
	quote.WalletBalanceMinor = 45000
	if quote.Covered() {
		t.Fatal("a short balance must not report the cart as covered")
	}
	if quote.MissingMinor() != 15000 {
		t.Fatalf("expected 15000 missing, got %d", quote.MissingMinor())
	}
}

func TestAutoPurchaseRefusesEveryReasonItShould(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	short := coveredQuote()
	short.WalletBalanceMinor = 1
	cases := []struct {
		name    string
		request AutoPurchaseRequest
		reason  string
	}{
		{"disabled", AutoPurchaseRequest{Enabled: false, Now: now, Quote: coveredQuote()}, CartAutoPurchaseOff},
		{"expired", AutoPurchaseRequest{Enabled: true, Now: now, ExpiresAt: now.Add(-time.Second), Quote: coveredQuote()}, CartExpired},
		{"maintenance", AutoPurchaseRequest{Enabled: true, Now: now, Quote: coveredQuote(), Maintenance: true}, CartMaintenance},
		{"short balance", AutoPurchaseRequest{Enabled: true, Now: now, Quote: short}, CartInsufficientBalance},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			reason, err := EvaluateAutoPurchase(testCase.request)
			if !errors.Is(err, ErrCartRejected) {
				t.Fatalf("expected a rejection, got %v", err)
			}
			if reason != testCase.reason {
				t.Fatalf("expected %q, got %q", testCase.reason, reason)
			}
		})
	}
}

func TestAutoPurchaseAcceptsACoveredCart(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	reason, err := EvaluateAutoPurchase(AutoPurchaseRequest{
		Enabled: true, Now: now, ExpiresAt: now.Add(time.Hour), Quote: coveredQuote(),
	})
	if err != nil || reason != "ready" {
		t.Fatalf("expected the cart to be ready, got %q %v", reason, err)
	}
}

// A cart with no expiry set never expires on its own, which is what keeps a
// deferred purchase alive while a customer arranges funds.
func TestCartWithoutAnExpiryDoesNotExpire(t *testing.T) {
	t.Parallel()
	reason, err := EvaluateAutoPurchase(AutoPurchaseRequest{Enabled: true, Now: time.Now(), Quote: coveredQuote()})
	if err != nil || reason != "ready" {
		t.Fatalf("expected the cart to be ready, got %q %v", reason, err)
	}
}
