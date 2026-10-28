package commerce

import (
	"errors"
	"testing"
	"time"
)

// The rule order creation enforces, for every caller: the operation has to be
// what the change between the plans actually is.
func TestValidateOperationEnforcesTheLifecycleRule(t *testing.T) {
	basic := PlanPricing{PlanID: "basic", AmountMinor: 30000, UpgradePolicy: "replace", DowngradePolicy: "at_expiry"}
	pro := PlanPricing{PlanID: "pro", AmountMinor: 60000, UpgradePolicy: "replace", DowngradePolicy: "at_expiry"}
	lite := PlanPricing{PlanID: "lite", AmountMinor: 15000, UpgradePolicy: "replace", DowngradePolicy: "at_expiry"}
	locked := PlanPricing{PlanID: "locked", AmountMinor: 90000, UpgradePolicy: "forbid", DowngradePolicy: "forbid"}
	holdsBasic := &HeldPlan{PlanID: "basic", AmountMinor: 30000}

	cases := []struct {
		name      string
		operation string
		plan      PlanPricing
		held      *HeldPlan
		want      error
	}{
		{"purchase into an empty slot", "purchase", basic, nil, nil},
		{"purchase onto a live subscription", "purchase", pro, holdsBasic, ErrPurchaseOnLiveSubscription},
		{"extension of the held plan", "extension", basic, holdsBasic, nil},
		{"extension of a lapsed subscription", "extension", basic, nil, nil},
		{"extension onto another plan", "extension", pro, holdsBasic, ErrOperationMismatch},
		{"upgrade to a dearer plan", "upgrade", pro, holdsBasic, nil},
		{"upgrade to a cheaper plan", "upgrade", lite, holdsBasic, ErrOperationMismatch},
		{"upgrade to the held plan", "upgrade", basic, holdsBasic, ErrOperationMismatch},
		{"upgrade with nothing held", "upgrade", pro, nil, ErrNothingToChange},
		{"downgrade to a cheaper plan", "downgrade", lite, holdsBasic, nil},
		{"downgrade to a dearer plan", "downgrade", pro, holdsBasic, ErrOperationMismatch},
		{"downgrade with nothing held", "downgrade", lite, nil, ErrNothingToChange},
		{"upgrade the policy forbids", "upgrade", locked, holdsBasic, ErrOperationMismatch},
		{"renewal is the worker's own", "renewal", pro, holdsBasic, nil},
		{"an unknown operation", "trial", basic, nil, ErrInvalidTransition},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if err := ValidateOperation(testCase.operation, testCase.plan, testCase.held); !errors.Is(err, testCase.want) || (testCase.want == nil && err != nil) {
				t.Fatalf("ValidateOperation = %v, want %v", err, testCase.want)
			}
		})
	}
}

// Whatever the catalogue offers, order creation accepts: the two are one rule.
func TestEveryOfferedOperationValidates(t *testing.T) {
	plans := []PlanPricing{
		{PlanID: "basic", AmountMinor: 30000, UpgradePolicy: "replace", DowngradePolicy: "at_expiry"},
		{PlanID: "pro", AmountMinor: 60000, UpgradePolicy: "extend", DowngradePolicy: "immediate"},
		{PlanID: "lite", AmountMinor: 15000, UpgradePolicy: "forbid", DowngradePolicy: "at_expiry"},
	}
	for _, plan := range plans {
		for _, held := range []HeldPlan{{PlanID: "basic", AmountMinor: 30000}, {PlanID: "pro", AmountMinor: 60000}} {
			for _, operation := range OfferedOperations(plan, OperationContext{Subscriptions: 1, Held: []HeldPlan{held}}) {
				if err := ValidateOperation(operation, plan, &held); err != nil {
					t.Fatalf("%s offered for %s over %s but refused: %v", operation, plan.PlanID, held.PlanID, err)
				}
			}
		}
		for _, operation := range OfferedOperations(plan, OperationContext{}) {
			if err := ValidateOperation(operation, plan, nil); err != nil {
				t.Fatalf("%s offered with nothing held but refused: %v", operation, err)
			}
		}
	}
}

func TestEntitlementLiveCountsAPauseAsLive(t *testing.T) {
	now := time.Date(2026, time.October, 28, 12, 0, 0, 0, time.UTC)
	if !EntitlementLive("paused", now.Add(-time.Hour), now) {
		t.Fatal("a paused entitlement preserves its time whatever its frozen end says")
	}
	if !EntitlementLive("active", now.Add(time.Hour), now) || EntitlementLive("active", now.Add(-time.Hour), now) {
		t.Fatal("a running entitlement is live until its end")
	}
	if !EntitlementLive("pending", time.Time{}, now) {
		t.Fatal("a paid entitlement not scheduled yet is live")
	}
	for _, status := range []string{"", "expired", "superseded", "failed"} {
		if EntitlementLive(status, now.Add(time.Hour), now) {
			t.Fatalf("a %q entitlement is not live", status)
		}
	}
}
