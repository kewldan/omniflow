package commerce

import (
	"errors"
	"time"
)

// The lifecycle rule: which operation moving a subscription to a plan is.
//
// It lives here, pure, because it is a business invariant rather than a
// presentation choice. The storefronts render it — the catalogue offers
// exactly the operations OfferedOperations returns — and order creation
// enforces it with ValidateOperation, so a caller that skips the storefront
// (the bot, the panel, the bearer-token API, a saved cart) meets the same rule
// instead of a weaker one.

// Lifecycle refusals. Each is a reason an operation does not fit the target
// subscription; order creation reports them as operation_forbidden.
var (
	// ErrPurchaseOnLiveSubscription refuses a purchase aimed at a
	// subscription whose entitlement is still in force. A purchase schedules
	// from now and supersedes what is there, so it would throw the remaining
	// time away; extension, upgrade, and downgrade exist for that case.
	ErrPurchaseOnLiveSubscription = errors.New("a purchase cannot target a subscription with time left")
	// ErrNothingToChange refuses an upgrade or a downgrade with no live plan to
	// move from: with nothing held, the only thing to do is buy.
	ErrNothingToChange = errors.New("there is no live plan to change from")
	// ErrOperationMismatch refuses an operation that names the change wrongly:
	// an extension onto another plan, an upgrade to a cheaper plan or to the
	// held one, a downgrade to a dearer plan or to the held one.
	ErrOperationMismatch = errors.New("the operation does not match the change between the plans")
)

// HeldPlan is one plan the customer is entitled to, at the price its current
// version is carried at, which is what makes "higher" and "lower" mean
// anything.
type HeldPlan struct {
	PlanID      string
	AmountMinor int64
}

// PlanPricing is the part of a catalogue row the rule compares.
type PlanPricing struct {
	PlanID          string
	AmountMinor     int64
	UpgradePolicy   string
	DowngradePolicy string
}

// OperationContext is everything the lifecycle rule needs to know about one
// customer, independent of how it was read.
type OperationContext struct {
	// Subscriptions is how many subscription rows the customer holds. A row
	// can exist with no entitlement behind it — a purchase that was never paid
	// leaves one — which is why it is counted separately from Held.
	Subscriptions int
	// Held are the plans behind the customer's live entitlements: the current
	// non-superseded, non-expired entitlement of each subscription. An expired
	// entitlement is not held; the customer has nothing to move up or down
	// from, only something to buy again.
	Held []HeldPlan
	// AdditionalAllowed reports whether the installation's subscription policy
	// would let this customer open one more subscription.
	AdditionalAllowed bool
}

// Holds reports whether the customer is entitled to this plan right now.
func (state OperationContext) Holds(planID string) bool {
	for _, plan := range state.Held {
		if plan.PlanID == planID {
			return true
		}
	}
	return false
}

// ClassifyChange names the change from a held plan to another one: the held
// plan itself is an extension, a plan priced at or above it an upgrade, and a
// cheaper one a downgrade. An equal price is an upgrade rather than nothing.
func ClassifyChange(plan PlanPricing, held HeldPlan) string {
	switch {
	case held.PlanID == plan.PlanID:
		return "extension"
	case plan.AmountMinor >= held.AmountMinor:
		return "upgrade"
	default:
		return "downgrade"
	}
}

// OfferedOperations decides which lifecycle actions a catalogue row offers.
//
//   - No live entitlement — no subscription at all, an unpaid purchase that
//     left an empty slot, or one that has expired — offers `purchase`.
//   - Against each held plan the row offers what ClassifyChange names it,
//     where the plan's policy allows it. With several live subscriptions a
//     plan dearer than one and cheaper than another is honestly both, and the
//     target picker resolves which one it applies to.
//   - `purchase` is additionally offered when the policy allows one more
//     subscription, so a multi-subscription customer can open another.
//
// The answer is advisory: ValidateOperation, the concurrency cap, and the
// subscription count are re-checked inside order creation under a lock this
// read does not take.
func OfferedOperations(plan PlanPricing, state OperationContext) []string {
	operations := make([]string, 0, 4)
	if len(state.Held) == 0 {
		return append(operations, "purchase")
	}
	if state.AdditionalAllowed {
		operations = append(operations, "purchase")
	}
	offered := map[string]bool{}
	for _, held := range state.Held {
		change := ClassifyChange(plan, held)
		if AllowedOperation(change, plan.UpgradePolicy, plan.DowngradePolicy) {
			offered[change] = true
		}
	}
	for _, change := range []string{"extension", "upgrade", "downgrade"} {
		if offered[change] {
			operations = append(operations, change)
		}
	}
	return operations
}

// ValidateOperation is the rule order creation enforces for one target
// subscription, holding `held` — nil when nothing on it is live.
//
// A renewal is the renewal worker's own operation, opened from the customer's
// standing consent for the subscription it names, and is not reclassified
// here; the plan policy still applies to it as to everything else.
func ValidateOperation(operation string, plan PlanPricing, held *HeldPlan) error {
	if !AllowedOperation(operation, plan.UpgradePolicy, plan.DowngradePolicy) {
		return ErrOperationMismatch
	}
	switch operation {
	case "purchase":
		if held != nil {
			return ErrPurchaseOnLiveSubscription
		}
		return nil
	case "renewal":
		return nil
	case "extension":
		// Extending a lapsed subscription buys a fresh period from now,
		// exactly as a purchase would, so it is not refused.
		if held != nil && ClassifyChange(plan, *held) != "extension" {
			return ErrOperationMismatch
		}
		return nil
	case "upgrade", "downgrade":
		if held == nil {
			return ErrNothingToChange
		}
		if ClassifyChange(plan, *held) != operation {
			return ErrOperationMismatch
		}
		return nil
	default:
		return ErrInvalidTransition
	}
}

// EntitlementLive reports whether an entitlement is still in force, which is
// what a purchase must never be aimed at. A paused entitlement is live
// whatever its frozen end says: the pause is preserving its time. One that has
// not been scheduled yet (no end) has been paid for and is live too.
func EntitlementLive(status string, endsAt, now time.Time) bool {
	switch status {
	case "", "expired", "superseded", "failed":
		return false
	case "paused":
		return true
	}
	if endsAt.IsZero() {
		return true
	}
	return now.Before(endsAt)
}
