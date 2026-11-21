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

// "Subscription ended between 30 and 90 days ago" is a range of whole days,
// either bound optional. A bound that is not a whole non-negative number, an
// unknown key, or a lower bound above the upper one is refused, so a mistyped
// range cannot silently widen the audience.
func TestExpiredDaysAgoIsAClosedRange(t *testing.T) {
	query, err := Compile(map[string]any{
		FilterExpiredDaysAgo: map[string]any{"min": float64(30), "max": float64(90)},
	}, testClock)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if len(query.Args) != 2 || query.Args[0] != int64(30) || query.Args[1] != int64(90) {
		t.Fatalf("both bounds are parameters, lower first: %v", query.Args)
	}
	if query.Explain[0] != "last subscription ended 30 to 90 days ago" {
		t.Fatalf("explain: %q", query.Explain[0])
	}
	for _, table := range []string{"legacy_subscription_periods", "entitlements"} {
		if !strings.Contains(query.Where, table) {
			t.Fatalf("the range reads %s, so customers who left before a migration are found:\n%s", table, query.Where)
		}
	}

	only, err := Compile(map[string]any{FilterExpiredDaysAgo: map[string]any{"min": float64(14)}}, testClock)
	if err != nil || len(only.Args) != 1 || only.Explain[0] != "last subscription ended at least 14 days ago" {
		t.Fatalf("a lower bound alone: %v %v %v", err, only.Args, only.Explain)
	}

	for _, value := range []any{
		map[string]any{},
		map[string]any{"min": float64(90), "max": float64(30)},
		map[string]any{"min": float64(-1)},
		map[string]any{"min": 1.5},
		map[string]any{"from": float64(30)},
		float64(30),
	} {
		if err := Validate(map[string]any{FilterExpiredDaysAgo: value}); err != ErrInvalidValue {
			t.Fatalf("%v must be refused, got %v", value, err)
		}
	}
}

// Payments the previous bot took count as spend and as activity, and its
// subscriptions as subscriptions: a customer who paid before a migration is
// not a customer who never paid.
func TestCarriedHistoryIsRead(t *testing.T) {
	for filter, value := range map[string]any{
		FilterMinimumSpendMinor: float64(100),
		FilterInactiveForDays:   float64(30),
		FilterActiveWithinDays:  float64(30),
	} {
		query, err := Compile(map[string]any{filter: value}, testClock)
		if err != nil {
			t.Fatalf("%s: %v", filter, err)
		}
		if !strings.Contains(query.Where, "legacy_payments") {
			t.Fatalf("%s ignores carried payments:\n%s", filter, query.Where)
		}
	}
	for _, state := range []string{"expired", "none"} {
		query, err := Compile(map[string]any{FilterSubscription: state}, testClock)
		if err != nil || !strings.Contains(query.Where, "legacy_subscription_periods") {
			t.Fatalf("subscription %s ignores carried periods: %v\n%s", state, err, query.Where)
		}
	}
}

// A shop buyer is found among Omniflow's delivered goods orders and the
// previous bot's, by kind and, optionally, by how recently.
func TestGoodsBuyers(t *testing.T) {
	query, err := Compile(map[string]any{
		FilterGoods:  map[string]any{"kind": "stars", "withinDays": float64(90)},
		FilterStatus: "active",
	}, testClock)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(query.Where, "legacy_goods_orders") || !strings.Contains(query.Where, "goods_deliveries") {
		t.Fatalf("goods reads both histories:\n%s", query.Where)
	}
	if len(query.Args) != 3 || query.Explain[0] != "bought Telegram Stars in the shop within 90 days" {
		t.Fatalf("args %v, explain %v", query.Args, query.Explain)
	}
	if !strings.Contains(query.Where, "$3") || strings.Contains(query.Where, "$4") {
		t.Fatalf("placeholders:\n%s", query.Where)
	}
	anyKind, err := Compile(map[string]any{FilterGoods: map[string]any{"kind": "any"}}, testClock)
	if err != nil || anyKind.Explain[0] != "bought anything in the shop" || strings.Contains(anyKind.Where, "make_interval") {
		t.Fatalf("any kind, no window: %v %v\n%s", err, anyKind.Explain, anyKind.Where)
	}
	for _, bad := range []any{
		"stars",
		map[string]any{},
		map[string]any{"kind": "vpn"},
		map[string]any{"kind": "stars", "withinDays": float64(0)},
		map[string]any{"kind": "stars", "days": float64(5)},
	} {
		if _, err := Compile(map[string]any{FilterGoods: bad}, testClock); err == nil {
			t.Fatalf("%v is accepted", bad)
		}
	}
}
