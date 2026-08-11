package channelgate

import (
	"testing"
	"time"
)

var now = time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)

func channels() []Channel {
	return []Channel{
		{ID: "news", Enabled: true, RequireForPurchase: true},
		{ID: "vip", Enabled: true, RequireForPurchase: true, RequireForActivation: true},
		{ID: "old", Enabled: false, RequireForPurchase: true},
	}
}

// An unreachable Telegram must never gate a purchase. Suspending people because
// of an outage is the failure mode this mechanism has that nothing else does.
func TestAnUnknownAnswerNeverBlocks(t *testing.T) {
	status := Evaluate(channels(), []Membership{
		{ChannelID: "news", State: StateUnknown},
		{ChannelID: "vip", State: StateMember},
	}, PurposePurchase, false)

	if !status.Compliant() {
		t.Fatalf("an unknown answer blocked a purchase: missing=%v", status.Missing)
	}
	if len(status.Unknown) != 1 || status.Unknown[0] != "news" {
		t.Fatalf("the unknown channel was not reported: %v", status.Unknown)
	}
}

// A customer with no record at all is in the same position as one whose check
// failed: nobody has established they are absent.
func TestNoRecordIsNotAbsence(t *testing.T) {
	status := Evaluate(channels(), nil, PurposePurchase, false)
	if !status.Compliant() {
		t.Fatalf("a customer with no membership record was blocked: %v", status.Missing)
	}
}

// A disabled channel requires nothing, and the two purposes are independent.
func TestOnlyEnabledChannelsForThatPurposeApply(t *testing.T) {
	absent := []Membership{
		{ChannelID: "news", State: StateAbsent},
		{ChannelID: "vip", State: StateAbsent},
		{ChannelID: "old", State: StateAbsent},
	}

	purchase := Evaluate(channels(), absent, PurposePurchase, false)
	if len(purchase.Missing) != 2 {
		t.Fatalf("purchase should require two channels, got %v", purchase.Missing)
	}

	// Activation gates fewer channels, because taking access from somebody who
	// already paid is a different promise from asking them to join first.
	activation := Evaluate(channels(), absent, PurposeActivation, false)
	if len(activation.Missing) != 1 || activation.Missing[0] != "vip" {
		t.Fatalf("activation should require only the vip channel, got %v", activation.Missing)
	}
}

// An exemption short-circuits everything, including the channel list.
func TestAnExemptionAppliesToEverything(t *testing.T) {
	status := Evaluate(channels(), []Membership{
		{ChannelID: "news", State: StateAbsent},
		{ChannelID: "vip", State: StateAbsent},
	}, PurposePurchase, true)

	if !status.Compliant() || !status.Exempt {
		t.Fatal("an exempt customer was gated")
	}
	if len(status.Missing) != 0 {
		t.Fatalf("an exempt customer should have nothing missing, got %v", status.Missing)
	}
}

// Leaving warns first. A grace period costs the operator a few days of one
// membership; its absence costs a paying customer their access without warning.
func TestLeavingWarnsBeforeItSuspends(t *testing.T) {
	lapsed := Status{Missing: []string{"news"}}

	warned := Next(lapsed, Compliant, nil, DefaultGrace, now)
	if warned.State != Warned || !warned.Warn || warned.Suspend {
		t.Fatalf("the first lapse should warn, got %+v", warned)
	}
	if warned.GraceUntil == nil || !warned.GraceUntil.Equal(now.Add(DefaultGrace)) {
		t.Fatal("the grace clock did not start")
	}

	// Inside the window nothing further happens, and in particular the warning
	// does not repeat: a daily reminder is how a customer learns to ignore the
	// bot.
	inside := Next(lapsed, Warned, warned.GraceUntil, DefaultGrace, now.Add(24*time.Hour))
	if inside.State != Warned || inside.Warn || inside.Suspend {
		t.Fatalf("inside grace nothing should change, got %+v", inside)
	}

	after := Next(lapsed, Warned, warned.GraceUntil, DefaultGrace, now.Add(96*time.Hour))
	if after.State != Suspended || !after.Suspend {
		t.Fatalf("past grace the customer should be suspended, got %+v", after)
	}
}

// Rejoining restores automatically. A customer who fixed the problem should not
// have to ask somebody to undo the consequence.
func TestRejoiningRestoresWithoutAsking(t *testing.T) {
	compliant := Status{Missing: []string{}}

	fromWarned := Next(compliant, Warned, &now, DefaultGrace, now.Add(time.Hour))
	if fromWarned.State != Compliant || fromWarned.Restore {
		t.Fatalf("a warned customer who rejoined should simply be compliant, got %+v", fromWarned)
	}

	fromSuspended := Next(compliant, Suspended, nil, DefaultGrace, now.Add(time.Hour))
	if fromSuspended.State != Compliant || !fromSuspended.Restore {
		t.Fatalf("a suspended customer who rejoined should be restored, got %+v", fromSuspended)
	}
}

// A customer who is already suspended and still absent produces no further
// action, so a re-check cannot record a second suspension.
func TestAnAlreadySuspendedCustomerIsNotSuspendedAgain(t *testing.T) {
	transition := Next(Status{Missing: []string{"news"}}, Suspended, nil, DefaultGrace, now)
	if transition.Suspend || transition.Changed {
		t.Fatalf("a repeat check re-suspended the customer: %+v", transition)
	}
}

// A zero grace period is a legitimate configuration. It still warns, so the
// customer knows why access stopped.
func TestZeroGraceStillWarns(t *testing.T) {
	transition := Next(Status{Missing: []string{"news"}}, Compliant, nil, 0, now)
	if transition.State != Suspended || !transition.Suspend {
		t.Fatalf("zero grace should suspend at once, got %+v", transition)
	}
	if !transition.Warn {
		t.Fatal("a customer suspended without warning is one who cannot tell why")
	}
}

// Becoming exempt while suspended restores access.
func TestAnExemptionRestoresASuspendedCustomer(t *testing.T) {
	transition := Next(Status{Exempt: true}, Suspended, nil, DefaultGrace, now)
	if transition.State != Exempt || !transition.Restore {
		t.Fatalf("an exemption should restore a suspended customer, got %+v", transition)
	}
}

func TestStaleBeforeUsesTheDefaultWhenUnset(t *testing.T) {
	if got := StaleBefore(now, 0); !got.Equal(now.Add(-DefaultRecheck)) {
		t.Fatalf("StaleBefore(0) = %v", got)
	}
	if got := StaleBefore(now, time.Hour); !got.Equal(now.Add(-time.Hour)) {
		t.Fatalf("StaleBefore(1h) = %v", got)
	}
}
