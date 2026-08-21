package accountcheckout

import (
	"errors"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/commerce"
)

// An order can take a payment only while it is pending, owes something, and
// has not passed its expiry — and expiry is judged by the clock, not by
// whether the sweep has already flipped the state.
func TestOrderPayableJudgesStateAmountAndExpiry(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		order OrderSummary
		want  error
	}{
		{"pending and owing", OrderSummary{State: commerce.OrderPending, ExternalMinor: 100, ExpiresAt: now.Add(time.Hour)}, nil},
		{"pending with no expiry", OrderSummary{State: commerce.OrderPending, ExternalMinor: 100}, nil},
		{"nothing owed", OrderSummary{State: commerce.OrderPending, ExternalMinor: 0, ExpiresAt: now.Add(time.Hour)}, ErrPaymentNotRequired},
		{"already paid", OrderSummary{State: commerce.OrderPaid, ExternalMinor: 100}, ErrOrderNotPayable},
		{"cancelled", OrderSummary{State: commerce.OrderCancelled, ExternalMinor: 100}, ErrOrderNotPayable},
		{"marked expired", OrderSummary{State: commerce.OrderExpired, ExternalMinor: 100}, ErrOrderNotPayable},
		{"past expiry but not yet swept", OrderSummary{State: commerce.OrderPending, ExternalMinor: 100, ExpiresAt: now.Add(-time.Second)}, ErrOrderNotPayable},
		{"expiring this instant", OrderSummary{State: commerce.OrderPending, ExternalMinor: 100, ExpiresAt: now}, ErrOrderNotPayable},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := OrderPayable(testCase.order, now); !errors.Is(got, testCase.want) && got != testCase.want {
				t.Fatalf("OrderPayable = %v, want %v", got, testCase.want)
			}
		})
	}
}
