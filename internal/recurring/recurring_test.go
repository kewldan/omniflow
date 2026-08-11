package recurring

import (
	"testing"
	"time"
)

func TestCapabilityRequiresAdapterOperatorAndAPassingTest(t *testing.T) {
	full := Capability{AdapterSupports: true, OperatorEnabled: true, TestStatus: "passed"}
	if !full.Allows() {
		t.Fatal("a fully configured provider must be chargeable")
	}

	for name, capability := range map[string]Capability{
		"adapter cannot bind":   {AdapterSupports: false, OperatorEnabled: true, TestStatus: "passed"},
		"operator switched off": {AdapterSupports: true, OperatorEnabled: false, TestStatus: "passed"},
		"never tested":          {AdapterSupports: true, OperatorEnabled: true, TestStatus: "untested"},
		"test failed":           {AdapterSupports: true, OperatorEnabled: true, TestStatus: "failed"},
	} {
		if capability.Allows() {
			t.Fatalf("%s: an automatic charge must not be permitted", name)
		}
	}
}

func TestChargeableRequiresConsent(t *testing.T) {
	consented := time.Now().UTC()

	enabled := Settings{Enabled: true, Funding: FundingWallet, ConsentAt: &consented, State: StateScheduled}
	if !enabled.Chargeable() {
		t.Fatal("an enabled, consented, wallet-funded setting must be chargeable")
	}

	// The row says enabled but carries no evidence the customer ever agreed.
	// Money must not move on that.
	noConsent := enabled
	noConsent.ConsentAt = nil
	if noConsent.Chargeable() {
		t.Fatal("a setting with no consent record must not be charged")
	}

	off := enabled
	off.Enabled = false
	if off.Chargeable() {
		t.Fatal("a disabled setting must not be charged")
	}

	suspended := enabled
	suspended.State = StateSuspended
	if suspended.Chargeable() {
		t.Fatal("a suspended setting must not be picked up again")
	}
}

func TestChargeableRequiresAMethodWhenFundingFromOne(t *testing.T) {
	consented := time.Now().UTC()
	settings := Settings{
		Enabled: true, Funding: FundingSavedMethod, ConsentAt: &consented, State: StateScheduled,
	}
	if settings.Chargeable() {
		t.Fatal("saved-method funding with no saved method must not be charged")
	}
	settings.PaymentMethodID = "method-1"
	if !settings.Chargeable() {
		t.Fatal("saved-method funding with a method must be chargeable")
	}

	unknown := settings
	unknown.Funding = "invoice"
	if unknown.Chargeable() {
		t.Fatal("an unrecognised funding source must not be charged")
	}
}

func TestLeadTimeIsClampedIntoTheSupportedRange(t *testing.T) {
	if got := NormalizeLeadTime(0); got != DefaultLeadTime {
		t.Fatalf("an unset lead time must fall back to the default, got %v", got)
	}
	if got := NormalizeLeadTime(-time.Hour); got != DefaultLeadTime {
		t.Fatalf("a negative lead time must fall back to the default, got %v", got)
	}
	if got := NormalizeLeadTime(90 * 24 * time.Hour); got != MaxLeadTime {
		t.Fatalf("an implausible lead time must be capped, got %v", got)
	}
	if got := NormalizeLeadTime(6 * time.Hour); got != 6*time.Hour {
		t.Fatalf("a sensible lead time must be honoured, got %v", got)
	}
}

func TestDueAtAndIsDue(t *testing.T) {
	endsAt := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	due := DueAt(endsAt, 24*time.Hour)
	if !due.Equal(endsAt.Add(-24 * time.Hour)) {
		t.Fatalf("unexpected due instant %v", due)
	}
	if IsDue(endsAt, 24*time.Hour, due.Add(-time.Second)) {
		t.Fatal("a renewal must not be due before its lead time")
	}
	if !IsDue(endsAt, 24*time.Hour, due) {
		t.Fatal("a renewal must be due exactly at its lead time")
	}
}

func TestTheWholeRetryScheduleFitsInsideTheDefaultLeadTime(t *testing.T) {
	// This is the property that keeps a customer from losing access while a
	// retry is still outstanding, so it is asserted rather than assumed.
	total := time.Duration(0)
	for attempt := 1; attempt <= MaxAttempts; attempt++ {
		total += RetryDelay(attempt)
	}
	if total >= DefaultLeadTime {
		t.Fatalf("the retry schedule spans %v, which does not fit inside the %v lead time",
			total, DefaultLeadTime)
	}
}

func TestScheduleNextAdvancesThenAbandons(t *testing.T) {
	now := time.Date(2026, 8, 13, 9, 0, 0, 0, time.UTC)

	first := ScheduleNext(1, now)
	if first.Abandon || first.Attempt != 2 {
		t.Fatalf("the first failure must schedule a second attempt, got %+v", first)
	}
	if first.Notify {
		// A single decline is usually resolved by the retry; telling the
		// customer about every one trains them to ignore the message that
		// matters.
		t.Fatal("the first failure must not notify")
	}
	if !first.At.Equal(now.Add(RetryDelay(2))) {
		t.Fatalf("unexpected next attempt time %v", first.At)
	}

	second := ScheduleNext(2, now)
	if second.Attempt != 3 || !second.Notify {
		t.Fatalf("the second failure must schedule and notify, got %+v", second)
	}

	last := ScheduleNext(MaxAttempts, now)
	if !last.Abandon || last.Attempt != 0 || !last.Notify {
		t.Fatalf("the final failure must abandon and notify, got %+v", last)
	}
	if beyond := ScheduleNext(MaxAttempts+5, now); !beyond.Abandon {
		t.Fatal("an attempt number past the ceiling must still abandon rather than reschedule")
	}
}

func TestStateAfterOutcome(t *testing.T) {
	if got := StateAfter(OutcomeSucceeded, false); got != StateScheduled {
		t.Fatalf("a successful renewal must re-arm for the next period, got %q", got)
	}
	if got := StateAfter(OutcomeFailed, false); got != StateDunning {
		t.Fatalf("a failed charge must enter dunning, got %q", got)
	}
	if got := StateAfter(OutcomeFailed, true); got != StateSuspended {
		t.Fatalf("an exhausted schedule must suspend, got %q", got)
	}
	if got := StateAfter(OutcomeAbandoned, false); got != StateSuspended {
		t.Fatalf("an abandoned cycle must suspend, got %q", got)
	}
}

func TestCycleKeyIsStablePerPeriod(t *testing.T) {
	endsAt := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	key := CycleKey("entitlement-1", endsAt)

	// The same period, expressed in another zone and with sub-second noise, is
	// the same cycle: the SQL that derives this key truncates to seconds in UTC.
	elsewhere := endsAt.In(time.FixedZone("MSK", 3*60*60)).Add(400 * time.Millisecond)
	if CycleKey("entitlement-1", elsewhere) != key {
		t.Fatal("the cycle key must not depend on the zone or on sub-second precision")
	}
	if CycleKey("entitlement-1", endsAt.Add(time.Hour)) == key {
		t.Fatal("a different period must produce a different cycle key")
	}
	if CycleKey("entitlement-2", endsAt) == key {
		t.Fatal("a different entitlement must produce a different cycle key")
	}
}
