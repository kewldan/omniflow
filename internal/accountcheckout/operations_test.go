package accountcheckout

import (
	"slices"
	"testing"
	"time"
)

// The single lifecycle rule both surfaces apply, stated as a table.
func TestOfferedOperationsFollowsTheHeldPlanAndItsPrice(t *testing.T) {
	t.Parallel()
	basic := PlanPricing{PlanID: "basic", AmountMinor: 30000, UpgradePolicy: "replace", DowngradePolicy: "at_expiry"}
	same := PlanPricing{PlanID: "basic-twin", AmountMinor: 30000, UpgradePolicy: "replace", DowngradePolicy: "at_expiry"}
	pro := PlanPricing{PlanID: "pro", AmountMinor: 60000, UpgradePolicy: "replace", DowngradePolicy: "at_expiry"}
	lite := PlanPricing{PlanID: "lite", AmountMinor: 15000, UpgradePolicy: "replace", DowngradePolicy: "at_expiry"}
	locked := PlanPricing{PlanID: "locked", AmountMinor: 90000, UpgradePolicy: "forbid", DowngradePolicy: "forbid"}
	mid := PlanPricing{PlanID: "mid", AmountMinor: 45000, UpgradePolicy: "replace", DowngradePolicy: "at_expiry"}

	holdsBasic := OperationContext{Subscriptions: 1, Held: []HeldPlan{{PlanID: "basic", AmountMinor: 30000}}}
	ghost := OperationContext{Subscriptions: 1}
	nobody := OperationContext{}
	multi := OperationContext{
		Subscriptions: 2, AdditionalAllowed: true,
		Held: []HeldPlan{{PlanID: "basic", AmountMinor: 30000}, {PlanID: "pro", AmountMinor: 60000}},
	}

	cases := []struct {
		name  string
		plan  PlanPricing
		state OperationContext
		want  []string
	}{
		{"nobody: purchase", basic, nobody, []string{"purchase"}},
		{"empty slot (unpaid purchase): purchase, not extension", basic, ghost, []string{"purchase"}},
		{"expired entitlement is not held: purchase", basic, OperationContext{Subscriptions: 1}, []string{"purchase"}},
		{"the held plan: extension", basic, holdsBasic, []string{"extension"}},
		{"a dearer plan: upgrade", pro, holdsBasic, []string{"upgrade"}},
		{"a cheaper plan: downgrade", lite, holdsBasic, []string{"downgrade"}},
		{"an equal price: upgrade, not a limit refusal", same, holdsBasic, []string{"upgrade"}},
		{"a plan whose policy forbids both: nothing", locked, holdsBasic, []string{}},
		{"multi: a plan between two held is both", mid, multi, []string{"purchase", "upgrade", "downgrade"}},
		{"multi: the held plan is extension, and a downgrade from the other", basic, multi, []string{"purchase", "extension", "downgrade"}},
		{"multi with no room: no purchase", pro, OperationContext{
			Subscriptions: 2, Held: multi.Held,
		}, []string{"extension", "upgrade"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := OfferedOperations(testCase.plan, testCase.state)
			if !slices.Equal(got, testCase.want) {
				t.Fatalf("OfferedOperations = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestTargetLiveJudgesStatusAndEnd(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		target SubscriptionTarget
		want   bool
	}{
		{"no entitlement", SubscriptionTarget{}, false},
		{"expired status", SubscriptionTarget{Status: "expired", EndsAt: now.Add(time.Hour)}, false},
		{"active and running", SubscriptionTarget{Status: "active", EndsAt: now.Add(time.Hour)}, true},
		{"active but past its end", SubscriptionTarget{Status: "active", EndsAt: now.Add(-time.Hour)}, false},
		{"pending, not scheduled yet", SubscriptionTarget{Status: "pending"}, true},
		{"paused", SubscriptionTarget{Status: "paused", EndsAt: now.Add(time.Hour)}, true},
		{"failed", SubscriptionTarget{Status: "failed"}, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := TargetLive(testCase.target, now); got != testCase.want {
				t.Fatalf("TargetLive = %v, want %v", got, testCase.want)
			}
		})
	}
}
