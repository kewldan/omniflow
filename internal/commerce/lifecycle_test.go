package commerce

import (
	"errors"
	"testing"
	"time"
)

func TestEvaluatePhaseUsesGracePeriodBeforeExpiry(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	endsAt := now.Add(-2 * time.Hour)
	graced := EvaluatePhase(now, Subscription{Status: "active", EndsAt: endsAt, GracePeriod: 24 * time.Hour})
	if graced != PhaseGrace {
		t.Fatalf("phase = %q, want %q", graced, PhaseGrace)
	}
	expired := EvaluatePhase(now, Subscription{Status: "active", EndsAt: endsAt})
	if expired != PhaseExpired {
		t.Fatalf("phase = %q, want %q", expired, PhaseExpired)
	}
}

// Grace is the window Remnawave is asked to keep access alive past the paid
// end. Once Remnawave itself reports the user expired, the window is over
// whatever the clock says, and the screen must not promise access until a date
// while the tunnel is dead.
func TestEvaluatePhaseTrustsRemnawaveOverTheGraceWindow(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	endsAt := now.Add(-2 * time.Hour)
	grace := 24 * time.Hour
	if got := EvaluatePhase(now, Subscription{Status: "expired", EndsAt: endsAt, GracePeriod: grace}); got != PhaseExpired {
		t.Fatalf("remote expired inside the grace window: phase = %q, want %q", got, PhaseExpired)
	}
	if got := EvaluatePhase(now, Subscription{Status: "disabled", EndsAt: endsAt, GracePeriod: grace}); got != PhaseDisabled {
		t.Fatalf("remote disabled inside the grace window: phase = %q, want %q", got, PhaseDisabled)
	}
	// While Remnawave still reports the user active, the grace window holds.
	if got := EvaluatePhase(now, Subscription{Status: "active", EndsAt: endsAt, GracePeriod: grace}); got != PhaseGrace {
		t.Fatalf("remote active inside the grace window: phase = %q, want %q", got, PhaseGrace)
	}
	if got := EvaluatePhase(now, Subscription{Status: "limited", EndsAt: endsAt, GracePeriod: grace}); got != PhaseGrace {
		t.Fatalf("remote limited inside the grace window: phase = %q, want %q", got, PhaseGrace)
	}
}

// A change built on a paused entitlement gives back exactly the time the
// pause was preserving — the same amount a resume would — and closes the
// pause, so the days cannot be handed out a second time.
func TestEffectiveEndsAtGivesBackExactlyTheRemainingPausedTime(t *testing.T) {
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	endsAt := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	pausedAt := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC) // eleven days were left
	if got := EffectiveEndsAt(now, endsAt, pausedAt); !got.Equal(now.Add(11 * 24 * time.Hour)) {
		t.Fatalf("effective end while paused = %s, want now plus eleven days", got)
	}
	// Running: the stored end is the end.
	if got := EffectiveEndsAt(now, endsAt, time.Time{}); !got.Equal(endsAt) {
		t.Fatalf("effective end while running = %s, want the stored end", got)
	}
	// Paused with nothing left is nothing left.
	if got := EffectiveEndsAt(now, endsAt, endsAt.Add(time.Hour)); !got.Equal(endsAt) {
		t.Fatalf("effective end with no remainder = %s, want the stored end", got)
	}
	// And the extension arithmetic then adds the new period on top of it.
	base := EffectiveEndsAt(now, endsAt, pausedAt)
	schedule, err := ScheduleEntitlement(now, 30*24*time.Hour, "extension", "extend", "at_expiry", &base)
	if err != nil || !schedule.EndsAt.Equal(base.Add(30*24*time.Hour)) {
		t.Fatalf("extension on a paused base: %+v %v", schedule, err)
	}
}

// The expiry pushed to Remnawave carries the grace; the paid end does not.
func TestRemoteExpiryAddsTheGraceOnly(t *testing.T) {
	endsAt := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	if got := RemoteExpiry(endsAt, 48*time.Hour); !got.Equal(endsAt.Add(48 * time.Hour)) {
		t.Fatalf("remote expiry = %s", got)
	}
	for _, grace := range []time.Duration{0, -time.Hour} {
		if got := RemoteExpiry(endsAt, grace); !got.Equal(endsAt) {
			t.Fatalf("a plan with grace %s must push the paid end, got %s", grace, got)
		}
	}
}

func TestEvaluatePhaseReportsLimitedAndProvisioning(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	limited := EvaluatePhase(now, Subscription{Status: "active", EndsAt: now.Add(30 * 24 * time.Hour), TrafficUsedBytes: 100, TrafficLimitBytes: 100})
	if limited != PhaseLimited {
		t.Fatalf("phase = %q, want %q", limited, PhaseLimited)
	}
	provisioning := EvaluatePhase(now, Subscription{Status: "pending", EndsAt: now.Add(30 * 24 * time.Hour)})
	if provisioning != PhaseProvisioning {
		t.Fatalf("phase = %q, want %q", provisioning, PhaseProvisioning)
	}
	soon := EvaluatePhase(now, Subscription{Status: "active", EndsAt: now.Add(3 * 24 * time.Hour)})
	if soon != PhaseExpiringSoon {
		t.Fatalf("phase = %q, want %q", soon, PhaseExpiringSoon)
	}
}

func TestRenewalReminderMatchesWholeDayOffsets(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	for offset, endsAt := range map[int]time.Time{
		7: now.Add(7 * 24 * time.Hour),
		3: now.Add(3 * 24 * time.Hour),
		1: now.Add(24 * time.Hour),
		0: now.Add(2 * time.Hour),
	} {
		got, due := RenewalReminderDue(now, endsAt)
		if !due || got != offset {
			t.Fatalf("reminder for %s = (%d, %t), want (%d, true)", endsAt, got, due, offset)
		}
	}
	if _, due := RenewalReminderDue(now, now.Add(5*24*time.Hour)); due {
		t.Fatal("five days remaining must not trigger a reminder")
	}
	if _, due := RenewalReminderDue(now, now.Add(-time.Hour)); due {
		t.Fatal("an expired subscription must not trigger a renewal reminder")
	}
}

func TestRecoveryDueOnlyInsideTheWindow(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	endsAt := now.Add(-48 * time.Hour)
	if !RecoveryDue(now, endsAt, 24*time.Hour, 14*24*time.Hour) {
		t.Fatal("a subscription past its grace period must be recoverable")
	}
	if RecoveryDue(now, endsAt, 72*time.Hour, 14*24*time.Hour) {
		t.Fatal("a subscription still inside its grace period must not be offered recovery")
	}
	if RecoveryDue(now, now.Add(-90*24*time.Hour), 0, 14*24*time.Hour) {
		t.Fatal("a long-expired subscription must fall outside the recovery window")
	}
}

func TestEvaluateTrialAppliesAbuseControls(t *testing.T) {
	base := TrialRequest{PlanKind: "trial", Rule: TrialNewCustomer, MinimumAccountAge: time.Hour, AccountAge: 48 * time.Hour}
	if reason, err := EvaluateTrial(base); err != nil {
		t.Fatalf("eligible trial rejected: %s (%v)", reason, err)
	}
	for name, mutate := range map[string]func(*TrialRequest){
		"trial_already_used":        func(request *TrialRequest) { request.AlreadyClaimed = true },
		"subscription_active":       func(request *TrialRequest) { request.ActiveEntitlement = true },
		"identity_already_trialled": func(request *TrialRequest) { request.SharedIdentitySignal = true },
		"account_too_new":           func(request *TrialRequest) { request.AccountAge = 0 },
		"existing_customer":         func(request *TrialRequest) { request.CompletedOrders = 1 },
	} {
		request := base
		mutate(&request)
		reason, err := EvaluateTrial(request)
		if !errors.Is(err, ErrTrialNotEligible) || reason != name {
			t.Fatalf("trial guard %q returned (%q, %v)", name, reason, err)
		}
	}
}

func TestNormalizeOperationAndPolicyGuards(t *testing.T) {
	for input, expected := range map[string]string{"trial": "purchase", "renewal": "extension", "upgrade": "upgrade", "Downgrade": "downgrade"} {
		operation, err := NormalizeOperation(input)
		if err != nil || operation != expected {
			t.Fatalf("normalize %q = (%q, %v), want %q", input, operation, err, expected)
		}
	}
	if _, err := NormalizeOperation("transfer"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("unknown operation error = %v, want ErrInvalidTransition", err)
	}
	if AllowedOperation("upgrade", "forbid", "at_expiry") {
		t.Fatal("a forbidding upgrade policy must not allow an upgrade")
	}
	if !AllowedOperation("extension", "forbid", "forbid") {
		t.Fatal("extension must not be blocked by upgrade or downgrade policy")
	}
}
