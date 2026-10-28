package renewal

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/database/dbgen"
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

// The first attempt is due at the lead-time boundary the database judged, not
// at the worker's own instant, so a worker clock running ahead of the
// database's cannot push the charge into the next pass.
func TestTheFirstAttemptIsDueWhenTheCycleEnteredItsLeadTime(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	ends := now.Add(24 * time.Hour)
	row := dbgen.ListAutoRenewDueRow{
		LeadTimeSeconds:   int64((72 * time.Hour).Seconds()),
		EntitlementEndsAt: pgtype.Timestamptz{Time: ends, Valid: true},
	}
	if got, want := firstAttemptAt(row, now), ends.Add(-72*time.Hour); !got.Equal(want) {
		t.Fatalf("first attempt at %v, want the lead-time boundary %v", got, want)
	}
	// A boundary still ahead of the worker's clock is clamped to now: the
	// attempt is never scheduled into the future.
	row.LeadTimeSeconds = 0
	if got := firstAttemptAt(row, now); !got.Equal(now) {
		t.Fatalf("first attempt at %v, want now", got)
	}
	if got := firstAttemptAt(dbgen.ListAutoRenewDueRow{}, now); !got.Equal(now) {
		t.Fatalf("without an end the first attempt is at %v, want now", got)
	}
}
