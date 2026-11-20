package commerce

import (
	"testing"
	"time"
)

func TestATrialIsRefusedOnlyWhenEveryWayOfSigningInIsExcluded(t *testing.T) {
	t.Parallel()
	if !TrialSignInMethodExcluded([]string{"email"}, []string{"email"}) {
		t.Fatal("an e-mail-only account must be refused when e-mail is excluded")
	}
	if TrialSignInMethodExcluded([]string{"email", "telegram"}, []string{"email"}) {
		t.Fatal("an account that also signs in with Telegram still qualifies")
	}
	if TrialSignInMethodExcluded(nil, []string{"email"}) || TrialSignInMethodExcluded([]string{"email"}, nil) {
		t.Fatal("nothing known, or nothing excluded, refuses nothing")
	}
	reason, err := EvaluateTrial(TrialRequest{PlanKind: "trial", Rule: TrialAnyone, SignInMethodExcluded: true})
	if err == nil || reason != "sign_in_method" {
		t.Fatalf("reason = %q, err = %v", reason, err)
	}
}

func TestTheTrialWarningFiresInsideItsWindowOnly(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	if !TrialEndingDue(now, now.Add(90*time.Minute), "trial", 2) {
		t.Fatal("ninety minutes before the end of a trial is inside a two-hour window")
	}
	for name, due := range map[string]bool{
		"outside the window": TrialEndingDue(now, now.Add(3*time.Hour), "trial", 2),
		"already over":       TrialEndingDue(now, now.Add(-time.Minute), "trial", 2),
		"a paid plan":        TrialEndingDue(now, now.Add(time.Hour), "recurring", 2),
		"switched off":       TrialEndingDue(now, now.Add(time.Hour), "trial", 0),
	} {
		if due {
			t.Fatalf("%s: a warning was due", name)
		}
	}
}
