package botapp

import (
	"reflect"
	"testing"
	"time"
)

func TestPlanOperationsFollowTheSharedRule(t *testing.T) {
	t.Parallel()
	open := func(planID string, amount int64) Plan {
		return Plan{PlanID: planID, AmountMinor: amount, UpgradePolicy: "extend", DowngradePolicy: "at_expiry"}
	}
	closed := func(planID string, amount int64) Plan {
		return Plan{PlanID: planID, AmountMinor: amount, UpgradePolicy: "forbid", DowngradePolicy: "forbid"}
	}
	basic := planHolding{Held: true, PlanID: "basic", AmountMinor: 500}
	cases := []struct {
		name    string
		plan    Plan
		holding planHolding
		forNew  bool
		want    []string
	}{
		{"nothing held is a purchase", open("pro", 900), planHolding{}, false, []string{"purchase"}},
		{"the held plan is an extension", open("basic", 500), basic, false, []string{"extension"}},
		{"the held plan at a new price is still an extension", open("basic", 650), basic, false, []string{"extension"}},
		{"a dearer plan is an upgrade", open("pro", 900), basic, false, []string{"upgrade"}},
		{"a cheaper plan is a downgrade", open("lite", 200), basic, false, []string{"downgrade"}},
		{"an equal price is offered as an upgrade", open("twin", 500), basic, false, []string{"upgrade"}},
		{"a forbidden upgrade is withheld", closed("pro", 900), basic, false, []string{}},
		{"a forbidden downgrade is withheld", closed("lite", 200), basic, false, []string{}},
		{"a forbidden equal-price switch is withheld", closed("twin", 500), basic, false, []string{}},
		{"an unpriced holding ranks below everything", open("pro", 1), planHolding{Held: true, PlanID: "basic"}, false, []string{"upgrade"}},
		{"a new subscription is always a purchase", open("pro", 900), basic, true, []string{"purchase"}},
		{"a new subscription of the held plan is a purchase", open("basic", 500), basic, true, []string{"purchase"}},
		{"never both directions at once", open("pro", 900), basic, false, []string{"upgrade"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := planOperations(tc.plan, tc.holding, tc.forNew)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("planOperations = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestHoldingFromTreatsALapsedEntitlementAsNothingHeld(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	live := Entitlement{Found: true, PlanID: "basic", PlanVersionID: "v1", AmountMinor: 500, Status: "active", EndsAt: now.Add(time.Hour)}
	if got := holdingFrom(live, now); !got.Held || got.PlanID != "basic" || got.AmountMinor != 500 {
		t.Fatalf("a live entitlement is held: %+v", got)
	}
	for name, entitlement := range map[string]Entitlement{
		"none":          {},
		"expired state": {Found: true, PlanID: "basic", Status: "expired", EndsAt: now.Add(time.Hour)},
		"ended":         {Found: true, PlanID: "basic", Status: "active", EndsAt: now.Add(-time.Minute)},
	} {
		if got := holdingFrom(entitlement, now); got.Held {
			t.Fatalf("%s: nothing is held: %+v", name, got)
		}
	}
}
