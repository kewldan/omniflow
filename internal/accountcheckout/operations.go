package accountcheckout

import (
	"time"

	"github.com/omniflow/omniflow/internal/commerce"
)

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

// OfferedOperations decides which lifecycle actions a catalogue row offers.
//
// This is the one rule both surfaces apply, and it is deliberately simple
// enough to state in a sentence: the plan the customer holds is extended; a
// plan priced at or above a held one is an upgrade; a plan priced below a
// held one is a downgrade; with nothing held, the only thing to do is buy.
//
//   - No live entitlement — no subscription at all, an unpaid purchase that
//     left an empty slot, or one that has expired — offers `purchase`. An
//     earlier rule offered `extension` against the empty slot and treated an
//     expired entitlement as held, which left a lapsed customer unable to buy
//     the plan they had and offered "upgrade" to a plan they no longer paid
//     for.
//   - The held plan itself offers `extension`.
//   - Any other plan offers `upgrade` when its price is at or above the held
//     price and the policy allows, and `downgrade` when below and the policy
//     allows. An equal price is an upgrade rather than nothing: the earlier
//     rule answered `subscription_limit_reached` to a plan that cost exactly
//     what the customer already paid, which is not what that reason means.
//   - With several live subscriptions the row is judged against each, and the
//     target picker resolves which one it applies to. A plan dearer than one
//     and cheaper than another is honestly both.
//   - `purchase` is additionally offered when the policy allows one more
//     subscription, so a multi-subscription customer can open another.
//
// The answer is advisory: the concurrency cap and the subscription count are
// re-checked inside order creation under a lock this read does not take.
func OfferedOperations(plan PlanPricing, state OperationContext) []string {
	operations := make([]string, 0, 4)
	if len(state.Held) == 0 {
		return append(operations, "purchase")
	}
	if state.AdditionalAllowed {
		operations = append(operations, "purchase")
	}
	extension, upgrade, downgrade := false, false, false
	for _, held := range state.Held {
		switch {
		case held.PlanID == plan.PlanID:
			extension = true
		case plan.AmountMinor >= held.AmountMinor:
			upgrade = upgrade || commerce.AllowedOperation("upgrade", plan.UpgradePolicy, plan.DowngradePolicy)
		default:
			downgrade = downgrade || commerce.AllowedOperation("downgrade", plan.UpgradePolicy, plan.DowngradePolicy)
		}
	}
	if extension {
		operations = append(operations, "extension")
	}
	if upgrade {
		operations = append(operations, "upgrade")
	}
	if downgrade {
		operations = append(operations, "downgrade")
	}
	return operations
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

// TargetLive reports whether a subscription target carries an entitlement that
// is still in force, which is what a `purchase` must never be aimed at: a
// purchase schedules from now and supersedes what is there, so buying "again"
// onto a live subscription would throw the remaining time away.
func TargetLive(target SubscriptionTarget, now time.Time) bool {
	switch target.Status {
	case "", "expired", "superseded", "failed":
		return false
	}
	if target.EndsAt.IsZero() {
		// A pending entitlement that has not been scheduled yet still counts:
		// the customer has paid for it.
		return true
	}
	return now.Before(target.EndsAt)
}
