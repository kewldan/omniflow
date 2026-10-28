package accountcheckout

import (
	"time"

	"github.com/omniflow/omniflow/internal/commerce"
)

// The lifecycle rule lives in internal/commerce, where order creation enforces
// it for every caller. These names keep the storefront's vocabulary — and the
// bot, which renders through this package — pointing at that one rule rather
// than a copy of it.
type (
	// OperationContext is commerce.OperationContext.
	OperationContext = commerce.OperationContext
	// HeldPlan is commerce.HeldPlan.
	HeldPlan = commerce.HeldPlan
	// PlanPricing is commerce.PlanPricing.
	PlanPricing = commerce.PlanPricing
)

// OfferedOperations decides which lifecycle actions a catalogue row offers. It
// is commerce.OfferedOperations: the catalogue renders exactly what order
// creation will accept.
func OfferedOperations(plan PlanPricing, state OperationContext) []string {
	return commerce.OfferedOperations(plan, state)
}

// TargetLive reports whether a subscription target carries an entitlement that
// is still in force, which is what a `purchase` must never be aimed at: a
// purchase schedules from now and supersedes what is there, so buying "again"
// onto a live subscription would throw the remaining time away. It is the same
// predicate order creation refuses such a purchase with.
func TargetLive(target SubscriptionTarget, now time.Time) bool {
	return commerce.EntitlementLive(target.Status, target.EndsAt, now)
}
