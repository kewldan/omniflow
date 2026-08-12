package accountreferral

import (
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/loyalty"
)

func ladder() []loyalty.Tier {
	// Deliberately out of order: the rows come from a query the caller wrote,
	// and progress must not depend on how they happened to arrive.
	return []loyalty.Tier{
		{ID: "gold", Code: "gold", Threshold: 500_00},
		{ID: "bronze", Code: "bronze", Threshold: 0},
		{ID: "silver", Code: "silver", Threshold: 100_00},
	}
}

func TestLoyaltyProgressMeasuresTheBandTheCustomerIsIn(t *testing.T) {
	cases := []struct {
		name      string
		tierID    string
		metric    int64
		current   string
		next      string
		remaining int64
		percent   int
	}{
		{
			name:   "half way through the silver band",
			tierID: "silver", metric: 300_00,
			current: "silver", next: "gold", remaining: 200_00, percent: 50,
		},
		{
			name:   "just placed on the bottom rung",
			tierID: "bronze", metric: 0,
			current: "bronze", next: "silver", remaining: 100_00, percent: 0,
		},
		{
			name:   "the top rung has nothing left to reach",
			tierID: "gold", metric: 900_00,
			current: "gold", next: "", remaining: 0, percent: 100,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			current, next, remaining, percent := LoyaltyProgress(ladder(), testCase.tierID, testCase.metric)
			if current.Code != testCase.current {
				t.Fatalf("current = %q, want %q", current.Code, testCase.current)
			}
			switch {
			case testCase.next == "" && next != nil:
				t.Fatalf("next = %q, want none", next.Code)
			case testCase.next != "" && next == nil:
				t.Fatalf("next = none, want %q", testCase.next)
			case next != nil && next.Code != testCase.next:
				t.Fatalf("next = %q, want %q", next.Code, testCase.next)
			}
			if remaining != testCase.remaining {
				t.Fatalf("remaining = %d, want %d", remaining, testCase.remaining)
			}
			if percent != testCase.percent {
				t.Fatalf("percent = %d, want %d", percent, testCase.percent)
			}
		})
	}
}

// A customer inside their grace period holds a tier their current metric no
// longer earns. Reading the held tier rather than recomputing one is what stops
// a page load from demoting somebody the evaluation worker has not demoted.
func TestLoyaltyProgressHoldsTheStoredTierRatherThanRecomputingIt(t *testing.T) {
	current, next, _, percent := LoyaltyProgress(ladder(), "gold", 50_00)
	if current.Code != "gold" {
		t.Fatalf("a held tier was recomputed away: %q", current.Code)
	}
	if next != nil {
		t.Fatalf("gold is the top rung, got next = %q", next.Code)
	}
	if percent != 100 {
		t.Fatalf("percent = %d, want the top rung to read as full", percent)
	}

	// The same rule below the top: a silver holder whose spend fell to bronze
	// levels keeps silver, and the bar clamps at zero rather than going negative.
	held, upper, remaining, share := LoyaltyProgress(ladder(), "silver", 10_00)
	if held.Code != "silver" || upper == nil || upper.Code != "gold" {
		t.Fatalf("expected a held silver below gold, got %q", held.Code)
	}
	if share != 0 {
		t.Fatalf("percent = %d, want a bar clamped at zero", share)
	}
	if remaining != 490_00 {
		t.Fatalf("remaining = %d, want the distance to gold", remaining)
	}
}

// A metric past the next threshold means the worker has not run yet. The bar
// clamps rather than pointing off the end of its track, and the tier still does
// not move: promotion is the worker's decision, not the reader's.
func TestLoyaltyProgressClampsAnUnevaluatedOvershoot(t *testing.T) {
	current, next, remaining, percent := LoyaltyProgress(ladder(), "silver", 900_00)
	if current.Code != "silver" {
		t.Fatalf("a page load promoted the customer to %q", current.Code)
	}
	if next == nil || next.Code != "gold" {
		t.Fatal("the next rung is still gold")
	}
	if remaining != 0 {
		t.Fatalf("remaining = %d, want 0", remaining)
	}
	if percent != 100 {
		t.Fatalf("percent = %d, want 100", percent)
	}
}

// An unknown tier identifier is what a superseded programme version leaves
// behind. Placing the customer on the bottom rung rather than panicking keeps a
// stale standing renderable.
func TestLoyaltyProgressToleratesATierThatIsNoLongerOnTheLadder(t *testing.T) {
	current, _, _, _ := LoyaltyProgress(ladder(), "platinum-from-v1", 250_00)
	if current.Code != "bronze" {
		t.Fatalf("current = %q, want the lowest rung", current.Code)
	}
	if empty, next, remaining, percent := LoyaltyProgress(nil, "", 0); empty.Code != "" ||
		next != nil || remaining != 0 || percent != 0 {
		t.Fatal("an empty ladder must produce an empty placement")
	}
}

func mustTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatalf("parse %q: %v", value, err)
	}
	return parsed.UTC()
}
