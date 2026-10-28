package commerce

import (
	"testing"
	"time"
)

// A refund takes back the period its order added and nothing more: an
// extension falls back to the end the customer had before it, a purchase ends
// now, time already used is never clawed back, and an entitlement that has not
// started is withdrawn whole.
func TestRevokeRefundedPeriodTakesBackExactlyWhatTheOrderAdded(t *testing.T) {
	now := time.Date(2026, time.October, 28, 12, 0, 0, 0, time.UTC)
	month := 30 * 24 * time.Hour

	// An extension bought ten days ago on top of twenty remaining days.
	extensionStart := now.Add(-10 * 24 * time.Hour)
	previousEnd := now.Add(10 * 24 * time.Hour)
	if got := RevokeRefundedPeriod(now, extensionStart, previousEnd.Add(month), time.Time{}, month); got.Retire || !got.EndsAt.Equal(previousEnd) {
		t.Fatalf("an extension must fall back to the previous end %s, got %+v", previousEnd, got)
	}
	// A purchase bought five days ago: the rest of it ends now.
	purchaseStart := now.Add(-5 * 24 * time.Hour)
	if got := RevokeRefundedPeriod(now, purchaseStart, purchaseStart.Add(month), time.Time{}, month); got.Retire || !got.EndsAt.Equal(now) {
		t.Fatalf("a refunded purchase must end now, got %+v", got)
	}
	// Refunded the instant it started: still a positive length.
	if got := RevokeRefundedPeriod(now, now, now.Add(month), time.Time{}, month); !got.EndsAt.After(now) {
		t.Fatalf("the entitlement must keep a positive length, got %+v", got)
	}
	// A paused entitlement stopped its clock at the pause; its remaining
	// time ends there, not at a later now.
	pausedAt := now.Add(-3 * 24 * time.Hour)
	if got := RevokeRefundedPeriod(now, purchaseStart, purchaseStart.Add(month), pausedAt, month); !got.EndsAt.Equal(pausedAt) {
		t.Fatalf("a paused purchase must end at its pause instant, got %+v", got)
	}
	// An at_expiry downgrade that has not started is withdrawn whole.
	if got := RevokeRefundedPeriod(now, now.Add(24*time.Hour), now.Add(24*time.Hour+month), time.Time{}, month); !got.Retire {
		t.Fatalf("a not-yet-started entitlement must be retired, got %+v", got)
	}
	// Never lengthened, whatever the arithmetic says.
	short := purchaseStart.Add(time.Hour)
	if got := RevokeRefundedPeriod(purchaseStart, purchaseStart.Add(-time.Hour), short, time.Time{}, time.Minute); got.EndsAt.After(short) {
		t.Fatalf("a refund lengthened an entitlement: %+v", got)
	}
}
