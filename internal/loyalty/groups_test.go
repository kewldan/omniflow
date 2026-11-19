package loyalty

import (
	"testing"
	"time"
)

func TestGroupsPriceByPeriodAndKindAndPriority(t *testing.T) {
	silver := Tier{Code: "silver", Threshold: 1000, DiscountBPS: 500, PeriodDiscountBPS: map[string]int{"year": 2000}, TrafficDiscountBPS: 2500}
	vip := Tier{Code: "vip", ManualOnly: true, Priority: 10, DiscountBPS: 100, AllowedPlanIDs: []string{"plan-a"}, AllowedSquadIDs: []string{"de"}}
	if got := silver.PlanDiscountBPS("month"); got != 500 {
		t.Fatalf("month = %d, want the base 500", got)
	}
	if got := silver.PlanDiscountBPS("year"); got != 2000 {
		t.Fatalf("year = %d, want the period's 2000", got)
	}
	if silver.AddonDiscountBPS("traffic") != 2500 || silver.AddonDiscountBPS("devices") != 0 {
		t.Fatal("add-on discounts are not by kind")
	}
	if best, _ := Best([]Tier{silver, vip}); best.Code != "vip" {
		t.Fatalf("best = %s, want the higher priority vip even with a smaller discount", best.Code)
	}
	if !silver.AllowsPlan("anything") || vip.AllowsPlan("plan-b") || !vip.AllowsPlan("plan-a") {
		t.Fatal("plan restriction")
	}
	if squads := vip.AllowedSquads([]string{"de", "nl"}); len(squads) != 1 || squads[0] != "de" {
		t.Fatalf("squads = %v", squads)
	}
	if got := DiscountAt(999, 12000); got != 999 {
		t.Fatalf("an over-range discount took %d", got)
	}
	program := Program{Metric: MetricSpend, Window: time.Hour, Tiers: []Tier{{Code: "bronze"}, silver, vip}}
	if err := Validate(program); err != nil {
		t.Fatalf("a manual group beside the ladder was refused: %v", err)
	}
	if tier, _ := TierFor(program, 1_000_000); tier.Code != "silver" {
		t.Fatalf("metric reached %s, want silver: a manual group is never earned", tier.Code)
	}
	program.Tiers = []Tier{vip}
	if Validate(program) == nil {
		t.Fatal("a programme of manual groups only has no floor and was accepted")
	}
	program.Tiers = []Tier{{Code: "bronze", PeriodDiscountBPS: map[string]int{"fortnight": 100}}}
	if Validate(program) == nil {
		t.Fatal("an unknown period was accepted")
	}
}
