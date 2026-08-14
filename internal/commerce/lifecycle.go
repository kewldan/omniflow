package commerce

import (
	"errors"
	"strings"
	"time"
)

// SubscriptionPhase is the customer-visible state of an entitlement. It is
// derived from Omniflow's own entitlement record plus the Remnawave observed
// status; Remnawave stays authoritative for the VPN user itself.
type SubscriptionPhase string

const (
	PhaseNone         SubscriptionPhase = "none"
	PhaseProvisioning SubscriptionPhase = "provisioning"
	PhaseActive       SubscriptionPhase = "active"
	PhaseExpiringSoon SubscriptionPhase = "expiring_soon"
	PhaseGrace        SubscriptionPhase = "grace"
	PhaseLimited      SubscriptionPhase = "limited"
	PhaseDisabled     SubscriptionPhase = "disabled"
	// PhasePaused is a subscription an operator stopped without spending its
	// remaining days. It is distinct from disabled because the two owe the
	// customer different things: a disabled subscription is running out, and a
	// paused one is not running at all.
	PhasePaused  SubscriptionPhase = "paused"
	PhaseExpired SubscriptionPhase = "expired"
	PhaseFailed  SubscriptionPhase = "failed"
)

// expiringSoonWindow matches the earliest renewal reminder so the bot never
// shows "active" while it is already asking the customer to renew.
const expiringSoonWindow = 7 * 24 * time.Hour

// Subscription is the minimum entitlement projection the domain needs.
type Subscription struct {
	// Status mirrors entitlements.status.
	Status      string
	EndsAt      time.Time
	GracePeriod time.Duration
	// TrafficUsedBytes and TrafficLimitBytes come from the Remnawave observed
	// state. A limit of zero or less means unlimited.
	TrafficUsedBytes  int64
	TrafficLimitBytes int64
	// PausedAt is when the current pause began, zero when the subscription is
	// running. It is the instant every remaining-time figure is measured from.
	PausedAt time.Time
}

// ClockNow is the instant a subscription's remaining time is measured from.
//
// While paused it is the moment the pause began, so "eleven days left" stays
// eleven days left for as long as the pause lasts. Measuring from the real
// clock would count a paused subscription down to zero on the customer's own
// screen while nothing was being consumed, which is the feature contradicting
// itself in the one place the customer looks.
func ClockNow(now time.Time, subscription Subscription) time.Time {
	if subscription.Status == "paused" && !subscription.PausedAt.IsZero() {
		return subscription.PausedAt
	}
	return now
}

// EvaluatePhase reduces an entitlement to one phase the bot can explain.
func EvaluatePhase(now time.Time, subscription Subscription) SubscriptionPhase {
	now = ClockNow(now, subscription)
	switch subscription.Status {
	case "":
		return PhaseNone
	case "pending":
		return PhaseProvisioning
	case "failed":
		return PhaseFailed
	case "superseded":
		return PhaseNone
	case "disabled":
		return PhaseDisabled
	case "paused":
		// Returned before any date arithmetic below, and that ordering is the
		// point. A pause freezes `ends_at` where it stood and real time walks
		// past it, so a paused subscription evaluated against the clock would
		// read as expired — the feature undone on the customer's own screen a
		// week after they were told their days were safe.
		return PhasePaused
	}
	if !subscription.EndsAt.IsZero() && !now.Before(subscription.EndsAt) {
		if subscription.GracePeriod > 0 && now.Before(subscription.EndsAt.Add(subscription.GracePeriod)) {
			return PhaseGrace
		}
		return PhaseExpired
	}
	if subscription.Status == "limited" || subscription.TrafficLimitBytes > 0 && subscription.TrafficUsedBytes >= subscription.TrafficLimitBytes {
		return PhaseLimited
	}
	if subscription.Status == "expired" {
		return PhaseExpired
	}
	if !subscription.EndsAt.IsZero() && subscription.EndsAt.Sub(now) <= expiringSoonWindow {
		return PhaseExpiringSoon
	}
	return PhaseActive
}

// RenewalOffsets are the whole-day reminders before expiry. They match the
// v0.2 expiry alerts so a customer never receives two schedules.
var RenewalOffsets = []int{7, 3, 1, 0}

// RenewalReminderDue reports the whole days remaining when they match a reminder
// offset, or false when no reminder is due. Days are floored so the final
// reminder fires on the last day rather than only after expiry.
func RenewalReminderDue(now, endsAt time.Time) (int, bool) {
	if endsAt.IsZero() {
		return 0, false
	}
	remaining := endsAt.Sub(now)
	if remaining < 0 {
		return 0, false
	}
	days := int(remaining / (24 * time.Hour))
	for _, offset := range RenewalOffsets {
		if days == offset {
			return offset, true
		}
	}
	return 0, false
}

// RecoveryDue reports whether an expired subscription is inside the window in
// which the bot offers a one-tap recovery checkout.
func RecoveryDue(now, endsAt time.Time, grace, window time.Duration) bool {
	if endsAt.IsZero() || window <= 0 {
		return false
	}
	start := endsAt.Add(grace)
	return !now.Before(start) && now.Before(start.Add(window))
}

// TrialRule constrains who may activate a trial plan.
type TrialRule string

const (
	TrialNewCustomer   TrialRule = "new_customer"
	TrialNeverTrialled TrialRule = "never_trialled"
	TrialAnyone        TrialRule = "any"
)

var ErrTrialNotEligible = errors.New("trial is not available for this account")

// TrialRequest is the abuse-control input for a trial activation.
type TrialRequest struct {
	PlanKind string
	Rule     TrialRule
	// AlreadyClaimed is true when trial_claims already holds a row for the
	// customer, which makes a second trial impossible regardless of rule.
	AlreadyClaimed bool
	// CompletedOrders counts paid, fulfilled, or refunded orders.
	CompletedOrders int
	// ActiveEntitlement blocks stacking a trial on a running subscription.
	ActiveEntitlement bool
	AccountAge        time.Duration
	MinimumAccountAge time.Duration
	// SharedIdentitySignal is set when the account reuses an identity that has
	// already consumed a trial. It never punishes silently: the caller shows the
	// returned reason and offers support.
	SharedIdentitySignal bool
}

// EvaluateTrial returns a stable machine reason when a trial must be refused.
func EvaluateTrial(request TrialRequest) (string, error) {
	if request.PlanKind != "trial" {
		return "not_a_trial_plan", ErrTrialNotEligible
	}
	if request.AlreadyClaimed {
		return "trial_already_used", ErrTrialNotEligible
	}
	if request.ActiveEntitlement {
		return "subscription_active", ErrTrialNotEligible
	}
	if request.SharedIdentitySignal {
		return "identity_already_trialled", ErrTrialNotEligible
	}
	if request.AccountAge < request.MinimumAccountAge {
		return "account_too_new", ErrTrialNotEligible
	}
	switch request.Rule {
	case TrialNewCustomer:
		if request.CompletedOrders > 0 {
			return "existing_customer", ErrTrialNotEligible
		}
	case TrialNeverTrialled, TrialAnyone:
	default:
		return "unsupported_trial_rule", ErrTrialNotEligible
	}
	return "eligible", nil
}

// NormalizeOperation maps a customer-facing checkout intent onto an order
// operation the v0.3 backend understands.
func NormalizeOperation(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "purchase", "trial", "recovery":
		return "purchase", nil
	case "extension", "renewal":
		return "extension", nil
	case "upgrade":
		return "upgrade", nil
	case "downgrade":
		return "downgrade", nil
	default:
		return "", ErrInvalidTransition
	}
}

// AllowedOperation reports whether a plan policy permits the operation. It
// mirrors the CreateOrder guard so the bot can hide an impossible action
// instead of failing after the customer taps it.
func AllowedOperation(operation, upgradePolicy, downgradePolicy string) bool {
	switch operation {
	case "upgrade":
		return upgradePolicy != "forbid"
	case "downgrade":
		return downgradePolicy != "forbid"
	default:
		return true
	}
}
