package botapp

import (
	"time"

	"github.com/omniflow/omniflow/internal/commerce"
)

// What a plan page may offer.
//
// This file is the bot's copy of the rule the customer web panel applies in
// internal/accountcheckout/catalog.go. It is kept pure and self-contained —
// no store, no clock, no locale — so it can be lifted into a shared domain
// function without changing its meaning. Until then the two copies have to
// agree, which is what the table test beside this file pins down.

// planHolding is what the customer currently holds, reduced to what the rule
// compares: the plan (not the version) and its price in the settlement
// currency. `Held` is false when there is no live entitlement — none at all,
// or one that has run out.
type planHolding struct {
	Held        bool
	PlanID      string
	AmountMinor int64
}

// holdingFrom reduces an entitlement to a holding as of `now`.
//
// An expired entitlement holds nothing: the customer is buying afresh, and the
// scheduling arithmetic starts a purchase from now either way. A plan whose
// version has no price in the settlement currency contributes an amount of
// zero, exactly as the web panel's HeldPlans does, so the two surfaces rank the
// same plan the same way.
func holdingFrom(entitlement Entitlement, now time.Time) planHolding {
	if !entitlement.Found || entitlement.Status == "expired" || entitlement.EndsAt.Before(now) {
		return planHolding{}
	}
	return planHolding{Held: true, PlanID: entitlement.PlanID, AmountMinor: entitlement.AmountMinor}
}

// planOperations decides which checkout operations a non-trial plan page
// offers.
//
//   - Nothing held: a purchase.
//   - The held plan itself: an extension. The comparison is by plan, not by
//     plan version, so publishing a new version of the plan a customer is on
//     turns "extend" into neither "switch up" nor "switch down".
//   - A dearer plan: an upgrade, when the plan's policy allows one.
//   - A cheaper plan: a downgrade, when the plan's policy allows one.
//   - An equally priced plan: offered as an upgrade, when allowed. Neither
//     direction is more true than the other, and the upgrade arithmetic is the
//     one that does not end an entitlement early.
//
// A policy of "forbid" removes the action rather than leaving it to fail after
// the tap. With `forNew` — the "Add a subscription" path — the only offer is a
// purchase, because there is nothing yet to extend or switch.
func planOperations(plan Plan, holding planHolding, forNew bool) []string {
	if forNew || !holding.Held {
		return []string{"purchase"}
	}
	if plan.PlanID == holding.PlanID {
		return []string{"extension"}
	}
	operations := make([]string, 0, 1)
	switch {
	case plan.AmountMinor >= holding.AmountMinor:
		if commerce.AllowedOperation("upgrade", plan.UpgradePolicy, plan.DowngradePolicy) {
			operations = append(operations, "upgrade")
		}
	default:
		if commerce.AllowedOperation("downgrade", plan.UpgradePolicy, plan.DowngradePolicy) {
			operations = append(operations, "downgrade")
		}
	}
	return operations
}
