package renewal

import (
	"context"
	"errors"
	"testing"

	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/payments"
)

// Every attempt on a cycle converges on one order, so what that order's state
// means decides whether an attempt charges, succeeds, or fails without touching
// a provider.
func TestCycleOrderDispositionDecidesTheAttempt(t *testing.T) {
	for state, want := range map[string]orderDisposition{
		"pending":            orderOpen,
		"draft":              orderOpen,
		"paid":               orderSettled,
		"fulfilled":          orderSettled,
		"partially_refunded": orderSettled,
		"refunded":           orderSettled,
		"expired":            orderClosed,
		"cancelled":          orderClosed,
	} {
		if got := cycleOrderDisposition(state); got != want {
			t.Errorf("order state %q classified as %d, want %d", state, got, want)
		}
	}
}

// A failure code is a stable label the customer notice and the panel key on,
// so each order-creation failure has to land on the label that describes it.
func TestOrderFailureCodesAreStable(t *testing.T) {
	for name, testCase := range map[string]struct {
		err  error
		want string
	}{
		"retired plan":       {errPlanUnavailable, "plan_unavailable"},
		"trial already used": {commercepg.ErrTrialAlreadyClaimed, "plan_unavailable"},
		"short wallet":       {errInsufficientWallet, "insufficient_wallet_balance"},
		"anything else":      {errors.New("connection reset"), "order_failed"},
	} {
		if got := orderFailureCode(testCase.err); got != testCase.want {
			t.Errorf("%s: orderFailureCode = %q, want %q", name, got, testCase.want)
		}
	}
}

func TestChargeFailureCodesNeverCarryProviderText(t *testing.T) {
	for name, testCase := range map[string]struct {
		err  error
		want string
	}{
		"adapter cannot bind":  {payments.ErrUnsupported, "recurring_unsupported"},
		"provider declined":    {payments.ErrProviderResponse, "declined"},
		"provider timed out":   {context.DeadlineExceeded, "provider_timeout"},
		"something with token": {errors.New("token=secret rejected"), "charge_failed"},
	} {
		if got := chargeFailureCode(testCase.err); got != testCase.want {
			t.Errorf("%s: chargeFailureCode = %q, want %q", name, got, testCase.want)
		}
	}
}
