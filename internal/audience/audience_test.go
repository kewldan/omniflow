package audience

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

var testClock = time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)

// An empty segment is every customer. It is legitimate — an outage notice means
// everybody — but it says so out loud, so nobody sends to the whole base by
// forgetting to add a filter.
func TestAnEmptySegmentSaysItMeansEverybody(t *testing.T) {
	query, err := Compile(map[string]any{}, testClock)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if query.Where != "true" {
		t.Fatalf("empty segment compiled to %q", query.Where)
	}
	if len(query.Explain) != 1 || !strings.Contains(query.Explain[0], "every customer") {
		t.Fatalf("an empty segment must explain itself, got %v", query.Explain)
	}
}

// The vocabulary is closed. A key outside it is refused rather than ignored:
// silently dropping a filter would send to a wider audience than the operator
// asked for.
func TestAnUnknownFilterIsRefused(t *testing.T) {
	if _, err := Compile(map[string]any{"deleteEverything": true}, testClock); err != ErrUnknownFilter {
		t.Fatalf("expected ErrUnknownFilter, got %v", err)
	}
	if err := Validate(map[string]any{"status": "not-a-status"}); err != ErrInvalidValue {
		t.Fatalf("expected ErrInvalidValue, got %v", err)
	}
}

// Every value reaches SQL as a parameter. A segment cannot express anything the
// vocabulary does not allow, including reading a table it has no business in.
func TestValuesAreAlwaysParameterised(t *testing.T) {
	query, err := Compile(map[string]any{
		FilterPlanCode: "'; DROP TABLE users; --",
	}, testClock)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if strings.Contains(query.Where, "DROP TABLE") {
		t.Fatal("a filter value reached the SQL text")
	}
	if len(query.Args) != 1 || query.Args[0] != "'; DROP TABLE users; --" {
		t.Fatalf("the value should have become an argument, got %v", query.Args)
	}
}

// The same definition always compiles to the same SQL and the same explanation.
// A segment whose rendering changes between reviews is one nobody can approve.
func TestCompilationIsDeterministic(t *testing.T) {
	filters := map[string]any{
		FilterStatus:             "active",
		FilterLocale:             "ru",
		FilterExpiringWithinDays: float64(7),
		FilterSubscription:       "active",
	}
	first, err := Compile(filters, testClock)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	for attempt := 0; attempt < 10; attempt++ {
		again, err := Compile(filters, testClock)
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		if again.Where != first.Where {
			t.Fatalf("the same filters compiled differently:\n%s\n%s", first.Where, again.Where)
		}
		if strings.Join(again.Explain, "|") != strings.Join(first.Explain, "|") {
			t.Fatal("the same filters explained differently")
		}
	}
}

// Placeholders are numbered across the whole set, so several parameterised
// filters concatenate into one valid statement.
func TestPlaceholdersAreNumberedAcrossFilters(t *testing.T) {
	query, err := Compile(map[string]any{
		FilterExpiringWithinDays: float64(7),
		FilterInactiveForDays:    float64(30),
		FilterMinimumSpendMinor:  float64(50_000),
	}, testClock)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if len(query.Args) != 3 {
		t.Fatalf("expected three arguments, got %d", len(query.Args))
	}
	// The placeholders must run $1..$3 with no gap: a gap means one filter is
	// pointing at another's value, which is the quiet way to send a campaign to
	// the wrong people.
	for index := 1; index <= len(query.Args); index++ {
		placeholder := "$" + strconv.Itoa(index)
		if !strings.Contains(query.Where, placeholder) {
			t.Fatalf("placeholder %s is missing from %q", placeholder, query.Where)
		}
	}
}

// The explanation is generated from the filters, so it cannot describe
// something the query does not do.
func TestExplanationMatchesTheFilters(t *testing.T) {
	query, err := Compile(map[string]any{
		FilterStatus:             "active",
		FilterExpiringWithinDays: float64(3),
	}, testClock)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	joined := strings.Join(query.Explain, "; ")
	if !strings.Contains(joined, "account status is active") {
		t.Fatalf("the status filter was not explained: %s", joined)
	}
	if !strings.Contains(joined, "3 days") {
		t.Fatalf("the expiry filter was not explained: %s", joined)
	}
}

// A day count must be a whole positive number. "3.5 days" and "-1 days" are
// both refused rather than rounded into something the operator did not ask for.
func TestDayCountsMustBeWholeAndPositive(t *testing.T) {
	for _, value := range []any{float64(3.5), float64(0), float64(-1), "seven"} {
		if err := Validate(map[string]any{FilterExpiringWithinDays: value}); err == nil {
			t.Fatalf("value %v should have been refused", value)
		}
	}
	if err := Validate(map[string]any{FilterExpiringWithinDays: float64(7)}); err != nil {
		t.Fatalf("a whole positive day count should be accepted: %v", err)
	}
}
