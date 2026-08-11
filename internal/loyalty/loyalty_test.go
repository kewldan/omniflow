package loyalty

import (
	"testing"
	"time"
)

func testProgram(grace time.Duration) Program {
	return Program{
		ID: "program-1", Version: 1, Metric: MetricSpend,
		Window: 365 * 24 * time.Hour, Grace: grace,
		Tiers: []Tier{
			{ID: "gold", Code: "gold", Threshold: 100_000, DiscountBPS: 1000},
			{ID: "bronze", Code: "bronze", Threshold: 0, DiscountBPS: 0},
			{ID: "silver", Code: "silver", Threshold: 50_000, DiscountBPS: 500},
		},
	}
}

// Every customer must land somewhere. A programme whose lowest tier is not zero
// would leave a new customer in no tier at all, which Validate refuses.
func TestEveryCustomerReachesATier(t *testing.T) {
	program := testProgram(0)
	tier, err := TierFor(program, 0)
	if err != nil {
		t.Fatalf("place a new customer: %v", err)
	}
	if tier.Code != "bronze" {
		t.Fatalf("a customer with no spend landed in %q", tier.Code)
	}

	for metric, want := range map[int64]string{
		49_999:  "bronze",
		50_000:  "silver",
		99_999:  "silver",
		100_000: "gold",
		999_999: "gold",
	} {
		tier, err := TierFor(program, metric)
		if err != nil {
			t.Fatalf("place %d: %v", metric, err)
		}
		if tier.Code != want {
			t.Fatalf("metric %d landed in %q, want %q", metric, tier.Code, want)
		}
	}
}

// Promotion is immediate. A customer who has earned a tier should not wait for
// a sweep to be told so.
func TestPromotionIsImmediate(t *testing.T) {
	program := testProgram(30 * 24 * time.Hour)
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)

	standing, err := Evaluate(program, Standing{TierID: "bronze"}, 60_000, now)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if standing.TierID != "silver" || !standing.Changed {
		t.Fatalf("expected an immediate promotion to silver, got %s changed=%v",
			standing.TierID, standing.Changed)
	}
	if standing.GraceUntil != nil {
		t.Fatal("a promoted customer must not be on grace")
	}
}

// Demotion waits out the grace period. A quiet month must not cost somebody the
// tier they spent a year earning.
func TestDemotionWaitsOutGrace(t *testing.T) {
	program := testProgram(30 * 24 * time.Hour)
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)

	// First evaluation below the threshold starts the clock and holds the tier.
	started, err := Evaluate(program, Standing{TierID: "gold"}, 10_000, now)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if started.TierID != "gold" || started.Changed {
		t.Fatalf("the tier should be held, got %s changed=%v", started.TierID, started.Changed)
	}
	if started.GraceUntil == nil || !started.GraceUntil.Equal(now.Add(program.Grace)) {
		t.Fatal("the grace clock did not start")
	}

	// Still inside the window: still held.
	inside, err := Evaluate(program, started, 10_000, now.Add(15*24*time.Hour))
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if inside.TierID != "gold" || inside.Changed {
		t.Fatalf("the tier should still be held, got %s", inside.TierID)
	}

	// Past it: demoted to what they actually qualify for.
	after, err := Evaluate(program, started, 10_000, now.Add(31*24*time.Hour))
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if after.TierID != "bronze" || !after.Changed {
		t.Fatalf("expected demotion to bronze, got %s changed=%v", after.TierID, after.Changed)
	}
}

// Recovering during grace clears the clock rather than leaving the customer
// permanently one bad month from demotion.
func TestRecoveringDuringGraceClearsTheClock(t *testing.T) {
	program := testProgram(30 * 24 * time.Hour)
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)

	held, _ := Evaluate(program, Standing{TierID: "gold"}, 10_000, now)
	recovered, err := Evaluate(program, held, 150_000, now.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if recovered.TierID != "gold" {
		t.Fatalf("expected gold to be kept, got %s", recovered.TierID)
	}
	if recovered.GraceUntil != nil {
		t.Fatal("a customer who qualifies again must not still be on grace")
	}
}

// A zero grace period demotes immediately, which is a legitimate configuration
// rather than a bug.
func TestZeroGraceDemotesImmediately(t *testing.T) {
	program := testProgram(0)
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)

	standing, err := Evaluate(program, Standing{TierID: "gold"}, 0, now)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if standing.TierID != "bronze" || !standing.Changed {
		t.Fatalf("expected immediate demotion, got %s changed=%v", standing.TierID, standing.Changed)
	}
}

// A tier from a retired programme version no longer exists. The customer is
// re-placed under the definition in force rather than left on a rung that is
// not there.
func TestATierFromAnotherVersionIsRePlaced(t *testing.T) {
	program := testProgram(30 * 24 * time.Hour)
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)

	standing, err := Evaluate(program, Standing{TierID: "platinum-from-v1"}, 60_000, now)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if standing.TierID != "silver" || !standing.Changed {
		t.Fatalf("expected re-placement into silver, got %s", standing.TierID)
	}
}

func TestValidateRefusesUnpublishableProgrammes(t *testing.T) {
	if err := Validate(Program{Metric: MetricSpend, Window: time.Hour}); err == nil {
		t.Fatal("a programme with no tiers must not publish")
	}

	// Every customer must reach a tier, so the lowest one has to start at zero.
	noFloor := testProgram(0)
	noFloor.Tiers = []Tier{
		{ID: "gold", Code: "gold", Threshold: 100_000, DiscountBPS: 1000},
		{ID: "silver", Code: "silver", Threshold: 50_000, DiscountBPS: 500},
	}
	if err := Validate(noFloor); err == nil {
		t.Fatal("a programme whose lowest tier is not zero must not publish")
	}

	inverted := testProgram(0)
	inverted.Tiers[0].DiscountBPS = 0 // gold worth nothing
	inverted.Tiers[2].DiscountBPS = 900
	if err := Validate(inverted); err == nil {
		t.Fatal("a higher tier worth less than a lower one must not publish")
	}

	if err := Validate(testProgram(0)); err != nil {
		t.Fatalf("a well-formed programme should publish: %v", err)
	}
}

// The discount rounds down. Rounding should never favour the operator at the
// customer's expense, and here the operator is the one giving it.
func TestDiscountRoundsDown(t *testing.T) {
	tier := Tier{DiscountBPS: 1000}
	if got := Discount(999, tier); got != 99 {
		t.Fatalf("Discount(999, 10%%) = %d, want 99", got)
	}
	if got := Discount(0, tier); got != 0 {
		t.Fatalf("Discount(0) = %d, want 0", got)
	}
	if got := Discount(10_000, Tier{}); got != 0 {
		t.Fatalf("a tier worth nothing discounted %d", got)
	}
}
