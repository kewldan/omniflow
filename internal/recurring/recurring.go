// Package recurring holds the rules for charging a renewal without asking the
// customer again.
//
// Automatic charging is the one place in the product where money moves with
// nobody watching, so the rules that govern it are kept here, in a package with
// no database, transport, or provider import, and are covered by unit tests
// rather than only by the worker that applies them.
//
// Three of those rules are load-bearing.
//
// Auto-renew is off unless the customer turned it on. There is no default, no
// inherited setting, and no "enabled because the plan supports it".
//
// A provider may only be charged automatically when its adapter declares the
// capability and the operator's own merchant account has passed a capability
// test. Several acquirers grant card binding per merchant rather than per
// integration, so the adapter's word alone is not enough.
//
// A failed charge retries a bounded number of times and then stops, handing the
// customer back to manual renewal with a notification. It never escalates, and
// it never silently keeps trying.
package recurring

import (
	"strconv"
	"time"
)

// Funding sources for an automatic renewal. They match the check constraint on
// `auto_renew_settings.funding`.
const (
	FundingWallet      = "wallet"
	FundingSavedMethod = "saved_method"
)

// Auto-renew states, matching `auto_renew_settings.state`.
const (
	// StateIdle is auto-renew switched off.
	StateIdle = "idle"
	// StateScheduled is armed and waiting for the lead time.
	StateScheduled = "scheduled"
	// StateDunning is a charge that failed and is being retried.
	StateDunning = "dunning"
	// StateSuspended is a charge that exhausted its retries. The customer keeps
	// their consent record but is back on manual renewal until they act.
	StateSuspended = "suspended"
)

// Outcomes recorded against a dunning attempt, matching
// `dunning_attempts.outcome`.
const (
	OutcomeScheduled = "scheduled"
	OutcomeSucceeded = "succeeded"
	OutcomeFailed    = "failed"
	OutcomeAbandoned = "abandoned"
)

// Capability describes what a provider may be asked to do.
type Capability struct {
	// AdapterSupports is compiled in: it is what the payment adapter declares
	// through payments.Capabilities.Recurring.
	AdapterSupports bool
	// OperatorEnabled is the per-provider, per-merchant switch in
	// `payment_provider_settings.recurring_enabled`.
	OperatorEnabled bool
	// TestStatus is the recorded result of the capability test against the
	// operator's own merchant account.
	TestStatus string
}

// Allows reports whether an automatic charge may be attempted at all.
//
// All three conditions are required, and the operator switch can only ever
// narrow what the adapter declares. Widening in the other direction would mean
// a database row granting a capability the integration does not have, which
// fails at the provider rather than at the switch.
func (capability Capability) Allows() bool {
	return capability.AdapterSupports &&
		capability.OperatorEnabled &&
		capability.TestStatus == "passed"
}

// Settings is one customer's auto-renew intent for one subscription.
type Settings struct {
	Enabled  bool
	Funding  string
	LeadTime time.Duration
	// ConsentAt is when the customer agreed. A setting with no consent is not
	// chargeable however it came to be enabled.
	ConsentAt       *time.Time
	PaymentMethodID string
	State           string
}

// Chargeable reports whether these settings may produce an automatic charge.
//
// The consent check is not redundant with `Enabled`: a row can be written by an
// import, a migration, or a future code path, and the money-moving decision
// should depend on the evidence of agreement rather than on a boolean that
// happens to be true.
func (settings Settings) Chargeable() bool {
	if !settings.Enabled || settings.ConsentAt == nil {
		return false
	}
	if settings.State == StateSuspended {
		return false
	}
	if settings.Funding == FundingSavedMethod && settings.PaymentMethodID == "" {
		return false
	}
	return settings.Funding == FundingWallet || settings.Funding == FundingSavedMethod
}

// DefaultLeadTime is how far ahead of expiry a renewal is first attempted.
//
// Three days leaves room for the whole retry schedule below to run before
// access actually lapses, which is the point of attempting early at all.
const DefaultLeadTime = 72 * time.Hour

// MaxLeadTime bounds what an operator or customer may configure. A renewal
// attempted a month early charges for a period the customer has not reached and
// may not want.
const MaxLeadTime = 14 * 24 * time.Hour

// NormalizeLeadTime clamps a configured lead time into the supported range.
func NormalizeLeadTime(lead time.Duration) time.Duration {
	switch {
	case lead <= 0:
		return DefaultLeadTime
	case lead > MaxLeadTime:
		return MaxLeadTime
	default:
		return lead
	}
}

// DueAt is the instant a renewal for an entitlement ending at `endsAt` becomes
// eligible to charge.
func DueAt(endsAt time.Time, lead time.Duration) time.Time {
	return endsAt.Add(-NormalizeLeadTime(lead))
}

// IsDue reports whether a renewal may be attempted now.
func IsDue(endsAt time.Time, lead time.Duration, now time.Time) bool {
	return !now.Before(DueAt(endsAt, lead))
}

// CycleKey identifies one subscription's one renewal period.
//
// Its exact form matters: the same expression is computed in SQL by
// `ListAutoRenewDue`, so the worker and the `dunning_attempts` uniqueness
// constraint must agree on what "this renewal" is. Second precision is enough,
// because an entitlement's end instant does not move within a cycle — and if it
// does, that is a different cycle by definition.
func CycleKey(entitlementID string, endsAt time.Time) string {
	return entitlementID + ":" + strconv.FormatInt(endsAt.UTC().Unix(), 10)
}

// MaxAttempts is how many times one renewal cycle is charged before the
// customer is handed back to manual renewal.
//
// Four attempts across the schedule below span roughly two and a half days,
// which fits inside the default lead time. That is deliberate: the whole
// schedule should finish before access lapses, so a customer whose card was
// briefly declined is not cut off while a retry is still pending.
const MaxAttempts = 4

// RetryDelay is how long to wait before attempt number `attempt`.
//
// The first attempt is immediate. The rest spread over hours rather than
// minutes, because the reasons an automatic charge fails — an expired card, a
// daily limit, a bank hold — are resolved by a person in hours, not by a
// retry a minute later.
func RetryDelay(attempt int) time.Duration {
	switch {
	case attempt <= 1:
		return 0
	case attempt == 2:
		return 4 * time.Hour
	case attempt == 3:
		return 12 * time.Hour
	default:
		return 24 * time.Hour
	}
}

// NextAttempt describes what happens after an attempt resolves.
type NextAttempt struct {
	// Attempt is the number of the next charge, or zero when there is none.
	Attempt int
	// At is when that charge should be made.
	At time.Time
	// Abandon is true when the schedule is exhausted. The caller marks the
	// cycle abandoned, moves the settings to suspended, and notifies the
	// customer that renewal is now manual.
	Abandon bool
	// Notify is true when the customer should be told about this failure.
	//
	// Not every failure earns a message: the first one is usually resolved by
	// the retry, and a notification per attempt trains a customer to ignore
	// them. The second failure and the final abandonment are the two that
	// change what the customer has to do.
	Notify bool
}

// ScheduleNext decides what follows a failed charge.
func ScheduleNext(attempt int, now time.Time) NextAttempt {
	if attempt < 1 {
		attempt = 1
	}
	if attempt >= MaxAttempts {
		return NextAttempt{Abandon: true, Notify: true}
	}
	next := attempt + 1
	return NextAttempt{
		Attempt: next,
		At:      now.Add(RetryDelay(next)),
		Notify:  next >= 3,
	}
}

// StateAfter reports the auto-renew state a cycle outcome leaves behind.
//
// Success returns to `scheduled` rather than `idle`: the customer's consent is
// unchanged and the next period will be attempted in turn. Abandonment moves to
// `suspended`, which stops the worker picking the row up again without
// discarding the consent record.
func StateAfter(outcome string, abandon bool) string {
	switch {
	case outcome == OutcomeSucceeded:
		return StateScheduled
	case outcome == OutcomeAbandoned || abandon:
		return StateSuspended
	case outcome == OutcomeFailed:
		return StateDunning
	default:
		return StateScheduled
	}
}
