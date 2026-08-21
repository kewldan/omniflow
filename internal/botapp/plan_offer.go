package botapp

import (
	"time"

	"github.com/omniflow/omniflow/internal/accountcheckout"
)

// What a plan page may offer.
//
// The rule is accountcheckout.OfferedOperations, shared with the customer web
// panel. This file only reduces what the bot knows about the customer to the
// holding that rule compares; the table test beside it pins the outcomes the
// bot relies on.

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
// The rule itself lives in accountcheckout.OfferedOperations and is the one
// both surfaces apply; this is the bot's projection of its own holding onto
// that rule. A bot plan page always addresses one subscription — the one the
// customer is looking at, or their only one — so the context carries one held
// plan at most, and a purchase against an existing holding is never offered
// here: that is `sub-new`'s job, which passes `forNew`.
func planOperations(plan Plan, holding planHolding, forNew bool) []string {
	if forNew || !holding.Held {
		return []string{"purchase"}
	}
	return accountcheckout.OfferedOperations(
		accountcheckout.PlanPricing{
			PlanID: plan.PlanID, AmountMinor: plan.AmountMinor,
			UpgradePolicy: plan.UpgradePolicy, DowngradePolicy: plan.DowngradePolicy,
		},
		accountcheckout.OperationContext{
			Subscriptions: 1,
			Held:          []accountcheckout.HeldPlan{{PlanID: holding.PlanID, AmountMinor: holding.AmountMinor}},
		},
	)
}
