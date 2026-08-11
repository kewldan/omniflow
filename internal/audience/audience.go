// Package audience turns an operator's segment definition into a query.
//
// A segment is a set of named filters, never a stored SQL fragment. Two reasons,
// and both matter more than the convenience a stored query would buy.
//
// An operator has to be able to read what a segment selects before sending
// anything to it, and an auditor has to be able to check afterwards what it
// selected. Neither is possible if the definition is a query somebody wrote
// once and nobody can now interpret.
//
// And a stored fragment is an injection surface with an audit trail attached.
// Everything here compiles to parameterised SQL from a closed vocabulary, so a
// segment cannot express anything the vocabulary does not allow — including
// reading a table it has no business reading.
package audience

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Filter names the segment vocabulary. Adding one means adding a case to
// Compile, which is the point: there is exactly one place that decides what a
// segment can say.
const (
	// FilterStatus selects customers by account status.
	FilterStatus = "status"
	// FilterLocale selects by interface language.
	FilterLocale = "locale"
	// FilterSubscription selects by whether they hold a live entitlement.
	FilterSubscription = "subscription"
	// FilterExpiringWithinDays selects customers whose entitlement ends soon.
	FilterExpiringWithinDays = "expiringWithinDays"
	// FilterInactiveForDays selects customers with no paid order in that many
	// days.
	FilterInactiveForDays = "inactiveForDays"
	// FilterMinimumSpendMinor selects by lifetime settled spend.
	FilterMinimumSpendMinor = "minimumSpendMinor"
	// FilterLoyaltyTier selects by current standing.
	FilterLoyaltyTier = "loyaltyTier"
	// FilterPlanCode selects customers holding a given plan.
	FilterPlanCode = "planCode"
)

var (
	// ErrUnknownFilter reports a key outside the vocabulary. It is refused on
	// write, so an unreadable segment can never be saved in the first place.
	ErrUnknownFilter = errors.New("unknown audience filter")
	// ErrInvalidValue reports a value the filter cannot use.
	ErrInvalidValue = errors.New("invalid audience filter value")
)

var (
	validStatus       = map[string]bool{"active": true, "suspended": true, "deleted": true}
	validLocale       = map[string]bool{"en": true, "ru": true}
	validSubscription = map[string]bool{"active": true, "expired": true, "none": true}
)

// Query is a compiled segment: a WHERE fragment and its arguments.
//
// The fragment always refers to the customer as `u`, so the caller controls the
// FROM clause and a segment can never widen what is selected from.
type Query struct {
	Where string
	Args  []any
	// Explain is the human-readable rendering of the same filters, in the same
	// order, for the review screen and the audit record. It is generated from
	// the filters rather than typed by the operator, so it cannot describe
	// something the query does not do.
	Explain []string
}

// Compile turns a filter set into a query.
//
// Filters are applied in a fixed order regardless of map iteration, so the same
// definition always produces the same SQL and the same explanation — a segment
// whose rendering changes between reviews is a segment nobody can approve.
func Compile(filters map[string]any, _ time.Time) (Query, error) {
	keys := make([]string, 0, len(filters))
	for key := range filters {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	query := Query{Where: "", Args: []any{}, Explain: []string{}}
	clauses := make([]string, 0, len(keys))

	for _, key := range keys {
		clause, explain, args, err := compileOne(key, filters[key], len(query.Args))
		if err != nil {
			return Query{}, err
		}
		clauses = append(clauses, clause)
		query.Explain = append(query.Explain, explain)
		query.Args = append(query.Args, args...)
	}

	if len(clauses) == 0 {
		// An empty segment is every customer. It is legitimate — an operator
		// announcing an outage means everybody — but it is stated explicitly so
		// nobody sends to the whole base by forgetting to add a filter.
		query.Where = "true"
		query.Explain = append(query.Explain, "every customer")
		return query, nil
	}
	query.Where = strings.Join(clauses, " AND ")
	return query, nil
}

// compileOne renders a single filter. `offset` is how many placeholders have
// already been used, so the fragments concatenate into one valid statement.
func compileOne(
	key string, value any, offset int,
) (clause, explain string, args []any, err error) {
	// Each branch appends its value first and then asks for the placeholder, so
	// the index is the count of arguments used so far including this one. An
	// off-by-one here would silently point a filter at the wrong value, which is
	// why the numbering lives in one place rather than in eight.
	next := func() string { return fmt.Sprintf("$%d", offset+len(args)) }

	switch key {
	case FilterStatus:
		status, ok := value.(string)
		if !ok || !validStatus[status] {
			return "", "", nil, ErrInvalidValue
		}
		args = append(args, status)
		return "u.status = " + next(), "account status is " + status, args, nil

	case FilterLocale:
		locale, ok := value.(string)
		if !ok || !validLocale[locale] {
			return "", "", nil, ErrInvalidValue
		}
		args = append(args, locale)
		return "u.locale = " + next(), "interface language is " + locale, args, nil

	case FilterSubscription:
		state, ok := value.(string)
		if !ok || !validSubscription[state] {
			return "", "", nil, ErrInvalidValue
		}
		switch state {
		case "active":
			return `EXISTS (SELECT 1 FROM entitlements e
				WHERE e.user_id = u.id AND e.status IN ('active', 'limited') AND e.ends_at > now())`,
				"holds a live subscription", args, nil
		case "expired":
			return `EXISTS (SELECT 1 FROM entitlements e WHERE e.user_id = u.id)
				AND NOT EXISTS (SELECT 1 FROM entitlements e
					WHERE e.user_id = u.id AND e.status IN ('active', 'limited') AND e.ends_at > now())`,
				"had a subscription and no longer does", args, nil
		default:
			return "NOT EXISTS (SELECT 1 FROM entitlements e WHERE e.user_id = u.id)",
				"has never had a subscription", args, nil
		}

	case FilterExpiringWithinDays:
		days, err := wholeNumber(value)
		if err != nil || days <= 0 {
			return "", "", nil, ErrInvalidValue
		}
		args = append(args, days)
		return fmt.Sprintf(`EXISTS (SELECT 1 FROM entitlements e
			WHERE e.user_id = u.id AND e.status IN ('active', 'limited')
			  AND e.ends_at > now()
			  AND e.ends_at <= now() + make_interval(days => %s::int))`, next()),
			fmt.Sprintf("subscription ends within %d days", days), args, nil

	case FilterInactiveForDays:
		days, err := wholeNumber(value)
		if err != nil || days <= 0 {
			return "", "", nil, ErrInvalidValue
		}
		args = append(args, days)
		return fmt.Sprintf(`NOT EXISTS (SELECT 1 FROM orders o
			WHERE o.user_id = u.id AND o.state IN ('paid', 'fulfilled')
			  AND o.created_at > now() - make_interval(days => %s::int))`, next()),
			fmt.Sprintf("has not paid for anything in %d days", days), args, nil

	case FilterMinimumSpendMinor:
		amount, err := wholeNumber(value)
		if err != nil || amount < 0 {
			return "", "", nil, ErrInvalidValue
		}
		args = append(args, amount)
		return fmt.Sprintf(`COALESCE((SELECT sum(o.paid_minor) FROM orders o
			WHERE o.user_id = u.id AND o.state IN ('paid', 'fulfilled')), 0) >= %s::bigint`, next()),
			fmt.Sprintf("has spent at least %d (minor units)", amount), args, nil

	case FilterLoyaltyTier:
		tier, ok := value.(string)
		if !ok || strings.TrimSpace(tier) == "" {
			return "", "", nil, ErrInvalidValue
		}
		args = append(args, tier)
		return fmt.Sprintf(`EXISTS (SELECT 1 FROM loyalty_standings s
			JOIN loyalty_tiers t ON t.id = s.tier_id
			WHERE s.user_id = u.id AND t.code = %s)`, next()),
			"stands in the " + tier + " loyalty tier", args, nil

	case FilterPlanCode:
		code, ok := value.(string)
		if !ok || strings.TrimSpace(code) == "" {
			return "", "", nil, ErrInvalidValue
		}
		args = append(args, code)
		return fmt.Sprintf(`EXISTS (SELECT 1 FROM entitlements e
			JOIN plan_versions v ON v.id = e.plan_version_id
			JOIN plans p ON p.id = v.plan_id
			WHERE e.user_id = u.id AND e.status IN ('active', 'limited') AND p.code = %s)`, next()),
			"holds the " + code + " plan", args, nil

	default:
		return "", "", nil, ErrUnknownFilter
	}
}

// Validate reports whether a filter set can be saved.
//
// It compiles the set and throws the result away. Refusing an unreadable
// segment on write rather than on send is what stops an operator discovering at
// send time that their audience means nothing.
func Validate(filters map[string]any) error {
	_, err := Compile(filters, time.Now())
	return err
}

// wholeNumber accepts the several shapes a JSON number arrives in.
func wholeNumber(value any) (int64, error) {
	switch typed := value.(type) {
	case float64:
		if typed != float64(int64(typed)) {
			return 0, ErrInvalidValue
		}
		return int64(typed), nil
	case int:
		return int64(typed), nil
	case int64:
		return typed, nil
	default:
		return 0, ErrInvalidValue
	}
}
