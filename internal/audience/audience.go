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
	"regexp"
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
	// FilterExpiredDaysAgo selects customers with no live subscription whose
	// last one ended between min and max days ago, both inclusive:
	// {"min": 30, "max": 90}. Either bound may be left out.
	FilterExpiredDaysAgo = "expiredDaysAgo"
	// FilterInactiveForDays selects customers with no paid order in that many
	// days.
	FilterInactiveForDays = "inactiveForDays"
	// FilterMinimumSpendMinor selects by lifetime settled spend.
	FilterMinimumSpendMinor = "minimumSpendMinor"
	// FilterLoyaltyTier selects by current standing.
	FilterLoyaltyTier = "loyaltyTier"
	// FilterPlanCode selects customers holding a given plan.
	FilterPlanCode = "planCode"
	// FilterTrial selects by trial: active (on a live trial), used (ever had
	// one), or never.
	FilterTrial = "trial"
	// FilterTrafficUsedPercent selects customers whose live subscription has
	// used at least that share of its traffic allowance, as last observed.
	FilterTrafficUsedPercent = "trafficUsedPercent"
	// FilterRegisteredWithinDays and FilterRegisteredBeforeDays select by
	// sign-up date.
	FilterRegisteredWithinDays = "registeredWithinDays"
	FilterRegisteredBeforeDays = "registeredBeforeDays"
	// FilterActiveWithinDays selects customers who used the web panel or paid
	// for something within that many days.
	FilterActiveWithinDays = "activeWithinDays"
	// FilterReferrals selects inviters (who brought somebody) or invitees
	// (who were brought).
	FilterReferrals = "referrals"
	// FilterAdCampaign selects customers a /start advertising campaign brought.
	FilterAdCampaign = "adCampaign"
	// FilterSignInMethod selects by a linked sign-in method: telegram, email,
	// vkid, or oidc.
	FilterSignInMethod = "signInMethod"
	// FilterZeroBalance selects customers whose wallet is empty (true) or not.
	FilterZeroBalance = "zeroBalance"
	// FilterCustomerIDs is an explicit selection, at most 1000 customers.
	FilterCustomerIDs = "customerIds"
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

// liveEntitlement is a subscription that serves the customer right now.
const liveEntitlement = `SELECT 1 FROM entitlements e
	WHERE e.user_id = u.id AND e.status IN ('active', 'limited') AND e.ends_at > now()`

// everSubscribed is any subscription the customer has had: an entitlement, or
// a period carried over from the previous bot, which keeps the customers who
// left before the migration findable after it.
const everSubscribed = `(EXISTS (SELECT 1 FROM entitlements e WHERE e.user_id = u.id)
	OR EXISTS (SELECT 1 FROM legacy_subscription_periods l WHERE l.user_id = u.id))`

// subscriptionEnded is a subscription the customer had and that is over, or an
// entitlement in any state. A carried period still running is not one: its
// subscription did not become an entitlement because the import refused it,
// and the customer has not left.
const subscriptionEnded = `(EXISTS (SELECT 1 FROM entitlements e WHERE e.user_id = u.id)
	OR EXISTS (SELECT 1 FROM legacy_subscription_periods l WHERE l.user_id = u.id AND l.ends_at <= now()))`

// lastSubscriptionEnd is when the customer's latest subscription ended or
// ends. A pending or failed entitlement never served anybody and is ignored.
const lastSubscriptionEnd = `GREATEST(
	(SELECT max(e.ends_at) FROM entitlements e WHERE e.user_id = u.id AND e.status NOT IN ('pending', 'failed')),
	(SELECT max(l.ends_at) FROM legacy_subscription_periods l WHERE l.user_id = u.id))`

// realOrder is an order that counts as a purchase. Payments the previous
// bot took are read beside it; the zero-value order an imported subscription
// hangs off is not a purchase.
const realOrder = `o.user_id = u.id AND o.state IN ('paid', 'fulfilled')
	AND NOT (o.subtotal_minor = 0 AND o.idempotency_key LIKE 'bedolaga:%')`

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
			return subscriptionEnded + " AND NOT EXISTS (" + liveEntitlement + ")",
				"had a subscription and no longer does", args, nil
		default:
			return "NOT " + everSubscribed, "has never had a subscription", args, nil
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

	case FilterExpiredDaysAgo:
		low, high, err := dayRange(value)
		if err != nil {
			return "", "", nil, err
		}
		clause = subscriptionEnded + " AND NOT EXISTS (" + liveEntitlement + ")"
		explain = "last subscription ended"
		if low != nil {
			args = append(args, *low)
			clause += fmt.Sprintf(" AND %s <= now() - make_interval(days => %s::int)", lastSubscriptionEnd, next())
		}
		if high != nil {
			args = append(args, *high)
			clause += fmt.Sprintf(" AND %s >= now() - make_interval(days => %s::int)", lastSubscriptionEnd, next())
		}
		switch {
		case low != nil && high != nil:
			explain += fmt.Sprintf(" %d to %d days ago", *low, *high)
		case low != nil:
			explain += fmt.Sprintf(" at least %d days ago", *low)
		default:
			explain += fmt.Sprintf(" at most %d days ago", *high)
		}
		return clause, explain, args, nil

	case FilterInactiveForDays:
		days, err := wholeNumber(value)
		if err != nil || days <= 0 {
			return "", "", nil, ErrInvalidValue
		}
		args = append(args, days)
		// realOrder carries a LIKE pattern, so it is joined to the formatted
		// text rather than passed through the format string.
		return "NOT EXISTS (SELECT 1 FROM orders o WHERE " + realOrder + fmt.Sprintf(`
			  AND o.created_at > now() - make_interval(days => %[1]s::int))
			AND NOT EXISTS (SELECT 1 FROM legacy_payments lp
			WHERE lp.user_id = u.id AND lp.paid_at > now() - make_interval(days => %[1]s::int))`, next()),
			fmt.Sprintf("has not paid for anything in %d days", days), args, nil

	case FilterMinimumSpendMinor:
		amount, err := wholeNumber(value)
		if err != nil || amount < 0 {
			return "", "", nil, ErrInvalidValue
		}
		args = append(args, amount)
		return fmt.Sprintf(`COALESCE((SELECT sum(o.paid_minor) FROM orders o
			WHERE o.user_id = u.id AND o.state IN ('paid', 'fulfilled')), 0)
			+ COALESCE((SELECT sum(lp.amount_minor) FROM legacy_payments lp WHERE lp.user_id = u.id), 0) >= %s::bigint`, next()),
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

	case FilterTrial:
		state, ok := value.(string)
		trialEntitlement := `SELECT 1 FROM entitlements e JOIN plan_versions v ON v.id = e.plan_version_id
			JOIN plans p ON p.id = v.plan_id WHERE e.user_id = u.id AND p.kind = 'trial'`
		// A trial the previous bot gave counts as one had.
		hadTrial := "(EXISTS (" + trialEntitlement + `) OR EXISTS (SELECT 1 FROM legacy_subscription_periods l
			WHERE l.user_id = u.id AND l.is_trial))`
		switch {
		case !ok:
			return "", "", nil, ErrInvalidValue
		case state == "active":
			return "EXISTS (" + trialEntitlement + " AND e.status IN ('active', 'limited') AND e.ends_at > now())",
				"is on a live trial", args, nil
		case state == "used":
			return hadTrial, "has had a trial", args, nil
		case state == "never":
			return "NOT " + hadTrial, "has never had a trial", args, nil
		default:
			return "", "", nil, ErrInvalidValue
		}

	case FilterTrafficUsedPercent:
		percent, err := wholeNumber(value)
		if err != nil || percent < 1 || percent > 100 {
			return "", "", nil, ErrInvalidValue
		}
		args = append(args, percent)
		return fmt.Sprintf(`EXISTS (SELECT 1 FROM subscriptions s
			JOIN entitlements e ON e.subscription_id = s.id AND e.status IN ('active', 'limited') AND e.ends_at > now()
			WHERE s.user_id = u.id AND e.traffic_allowance_bytes > 0
			  AND COALESCE((s.observed_state->>'usedTrafficBytes')::bigint, 0) * 100 >= e.traffic_allowance_bytes * %s::bigint)`, next()),
			fmt.Sprintf("has used at least %d%% of their traffic", percent), args, nil

	case FilterRegisteredWithinDays, FilterRegisteredBeforeDays:
		days, err := wholeNumber(value)
		if err != nil || days <= 0 {
			return "", "", nil, ErrInvalidValue
		}
		args = append(args, days)
		if key == FilterRegisteredWithinDays {
			return fmt.Sprintf("u.created_at > now() - make_interval(days => %s::int)", next()),
				fmt.Sprintf("signed up within the last %d days", days), args, nil
		}
		return fmt.Sprintf("u.created_at <= now() - make_interval(days => %s::int)", next()),
			fmt.Sprintf("signed up more than %d days ago", days), args, nil

	case FilterActiveWithinDays:
		days, err := wholeNumber(value)
		if err != nil || days <= 0 {
			return "", "", nil, ErrInvalidValue
		}
		args = append(args, days)
		return fmt.Sprintf(`(EXISTS (SELECT 1 FROM customer_sessions cs WHERE cs.user_id = u.id
				AND cs.last_seen_at > now() - make_interval(days => %[1]s::int))
			OR EXISTS (SELECT 1 FROM orders o WHERE o.user_id = u.id AND o.paid_at > now() - make_interval(days => %[1]s::int))
			OR EXISTS (SELECT 1 FROM legacy_payments lp WHERE lp.user_id = u.id AND lp.paid_at > now() - make_interval(days => %[1]s::int)))`, next()),
			fmt.Sprintf("used the web panel or paid within %d days", days), args, nil

	case FilterReferrals:
		role, ok := value.(string)
		switch {
		case ok && role == "inviter":
			return "EXISTS (SELECT 1 FROM referral_attributions a WHERE a.referrer_user_id = u.id)",
				"has invited somebody", args, nil
		case ok && role == "invitee":
			return "EXISTS (SELECT 1 FROM referral_attributions a WHERE a.referred_user_id = u.id)",
				"was invited by somebody", args, nil
		default:
			return "", "", nil, ErrInvalidValue
		}

	case FilterAdCampaign:
		code, ok := value.(string)
		if !ok || strings.TrimSpace(code) == "" {
			return "", "", nil, ErrInvalidValue
		}
		args = append(args, strings.ToLower(strings.TrimSpace(code)))
		return fmt.Sprintf(`EXISTS (SELECT 1 FROM customer_campaign_attributions ca
			JOIN ad_campaigns c ON c.id = ca.campaign_id WHERE ca.user_id = u.id AND c.code = %s)`, next()),
			"came from the " + code + " campaign", args, nil

	case FilterSignInMethod:
		method, ok := value.(string)
		if !ok {
			return "", "", nil, ErrInvalidValue
		}
		identity := "EXISTS (SELECT 1 FROM identities i WHERE i.user_id = u.id AND i.status = 'active' AND "
		switch method {
		case "telegram":
			return identity + "i.provider = 'telegram')", "signs in with Telegram", args, nil
		case "vkid":
			return identity + `i.provider IN (SELECT 'oidc:' || p.slug FROM customer_oidc_providers p
				WHERE p.issuer = 'https://id.vk.ru'))`, "signs in with VK ID", args, nil
		case "oidc":
			return identity + "i.provider LIKE 'oidc:%')", "signs in with an external provider", args, nil
		case "email":
			// E-mail sign-in is a link to a verified address, not an identity.
			return `EXISTS (SELECT 1 FROM contact_channels c WHERE c.user_id = u.id AND c.kind = 'email'
				AND c.verified_at IS NOT NULL AND c.revoked_at IS NULL)`, "has a verified e-mail address", args, nil
		default:
			return "", "", nil, ErrInvalidValue
		}

	case FilterZeroBalance:
		zero, ok := value.(bool)
		if !ok {
			return "", "", nil, ErrInvalidValue
		}
		balance := `COALESCE((SELECT sum(l.amount_minor) FROM ledger_entries l
			WHERE l.account_type = 'customer_wallet' AND l.user_id = u.id), 0)`
		if zero {
			return balance + " = 0", "has an empty wallet", args, nil
		}
		return balance + " > 0", "has money in their wallet", args, nil

	case FilterCustomerIDs:
		raw, ok := value.([]any)
		if !ok || len(raw) == 0 || len(raw) > 1000 {
			return "", "", nil, ErrInvalidValue
		}
		ids := make([]string, 0, len(raw))
		for _, item := range raw {
			id, ok := item.(string)
			if !ok || !uuidShape.MatchString(id) {
				return "", "", nil, ErrInvalidValue
			}
			ids = append(ids, strings.ToLower(id))
		}
		args = append(args, ids)
		return fmt.Sprintf("u.id = ANY(%s::uuid[])", next()),
			fmt.Sprintf("is one of %d selected customers", len(ids)), args, nil

	default:
		return "", "", nil, ErrUnknownFilter
	}
}

// uuidShape matches a customer identifier.
var uuidShape = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Validate reports whether a filter set can be saved.
//
// It compiles the set and throws the result away. Refusing an unreadable
// segment on write rather than on send is what stops an operator discovering at
// send time that their audience means nothing.
func Validate(filters map[string]any) error {
	_, err := Compile(filters, time.Now())
	return err
}

// dayRange reads {"min": a, "max": b}. At least one bound is required, both are
// whole non-negative days, and min may not exceed max. Unknown keys are
// refused, so a misspelt bound cannot silently widen the audience.
func dayRange(value any) (low, high *int64, err error) {
	bounds, ok := value.(map[string]any)
	if !ok || len(bounds) == 0 {
		return nil, nil, ErrInvalidValue
	}
	for key, raw := range bounds {
		days, numberErr := wholeNumber(raw)
		if numberErr != nil || days < 0 || days > 36500 {
			return nil, nil, ErrInvalidValue
		}
		switch key {
		case "min":
			low = &days
		case "max":
			high = &days
		default:
			return nil, nil, ErrInvalidValue
		}
	}
	if low != nil && high != nil && *low > *high {
		return nil, nil, ErrInvalidValue
	}
	return low, high, nil
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
