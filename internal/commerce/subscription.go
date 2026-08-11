package commerce

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ErrSubscriptionRejected wraps every reason a new concurrent subscription is
// refused. The wrapped message carries a stable machine reason.
var ErrSubscriptionRejected = errors.New("subscription rejected")

// Subscription concurrency rejection reasons.
const (
	SubscriptionMultiDisabled = "multi_subscription_disabled"
	SubscriptionLimitReached  = "subscription_limit_reached"
	SubscriptionPlanLimit     = "plan_limit_reached"
)

// SubscriptionPolicy is the installation-wide concurrency configuration.
// Multiple concurrent subscriptions are opt-in: the zero value keeps one
// subscription per customer, which is the historical behaviour.
type SubscriptionPolicy struct {
	// MultiEnabled allows a customer to hold more than one subscription.
	MultiEnabled bool
	// MaxPerCustomer bounds the concurrent subscriptions of one customer. It is
	// ignored while MultiEnabled is false, where the effective limit is one.
	MaxPerCustomer int
}

// EffectiveMax is the concurrency limit this policy actually enforces.
func (policy SubscriptionPolicy) EffectiveMax() int {
	if !policy.MultiEnabled {
		return 1
	}
	if policy.MaxPerCustomer < 1 {
		return 1
	}
	return policy.MaxPerCustomer
}

// AllowAdditional reports whether a customer who already holds activeCount
// subscriptions — of which planActiveCount belong to the plan being bought —
// may open one more. planMax is the plan's own concurrency cap, or nil when the
// plan sets none.
func (policy SubscriptionPolicy) AllowAdditional(activeCount, planActiveCount int, planMax *int) error {
	if activeCount >= policy.EffectiveMax() {
		if !policy.MultiEnabled {
			return fmt.Errorf("%w: %s", ErrSubscriptionRejected, SubscriptionMultiDisabled)
		}
		return fmt.Errorf("%w: %s", ErrSubscriptionRejected, SubscriptionLimitReached)
	}
	if planMax != nil && planActiveCount >= *planMax {
		return fmt.Errorf("%w: %s", ErrSubscriptionRejected, SubscriptionPlanLimit)
	}
	return nil
}

// SubscriptionRejectionReason recovers the machine reason from a wrapped
// rejection so a surface can look up localized copy for it.
func SubscriptionRejectionReason(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	if index := strings.LastIndex(message, ": "); index >= 0 {
		return message[index+2:]
	}
	return SubscriptionLimitReached
}

// TargetsNewSubscription reports whether a checkout operation opens a new
// subscription rather than changing an existing one. Only a purchase can; a
// renewal, upgrade, or downgrade always names the subscription it changes.
func TargetsNewSubscription(operation string) bool { return operation == "purchase" }

// DefaultSubscriptionLabel is the customer-visible name a new subscription gets
// before the customer renames it. It is deliberately neutral and carries no
// plan name, because the plan can change while the label must not.
func DefaultSubscriptionLabel(slot int) string {
	return fmt.Sprintf("Subscription %d", slot)
}

// NormalizeSubscriptionLabel trims and bounds a customer-supplied label. The
// label is shown back to the customer verbatim, so control characters and
// oversized values are rejected rather than silently truncated.
func NormalizeSubscriptionLabel(value string) (string, error) {
	label := strings.TrimSpace(value)
	if label == "" {
		return "", errors.New("label is required")
	}
	runes := []rune(label)
	if len(runes) > 40 {
		return "", errors.New("label must be 40 characters or fewer")
	}
	for _, symbol := range runes {
		if symbol < 0x20 || symbol == 0x7f {
			return "", errors.New("label must not contain control characters")
		}
	}
	return label, nil
}

// Squad selection rejection reasons.
const (
	SquadSelectionUnknown  = "squad_not_offered"
	SquadSelectionTooFew   = "squad_selection_too_few"
	SquadSelectionTooMany  = "squad_selection_too_many"
	SquadSelectionRefused  = "squad_selection_not_allowed"
	SquadSelectionRequired = "squad_selection_required"
)

var ErrSquadSelection = errors.New("squad selection rejected")

// SquadPolicy is the plan version's squad configuration: an always-assigned
// set plus the selectable set the customer may choose from.
type SquadPolicy struct {
	// Selection is "automatic", "optional", or "required".
	Selection string
	// Included squads are assigned to every subscription on this plan.
	Included []string
	// Offered squads are the ones a customer may add.
	Offered []string
	// Minimum and Maximum bound how many offered squads may be selected.
	// A nil Maximum means every offered squad may be selected.
	Minimum int
	Maximum *int
}

// ResolveSquads validates a customer's selection and returns the final squad
// set for the subscription: the included squads plus the accepted selection,
// deduplicated and ordered so two equal selections always produce equal state.
func (policy SquadPolicy) ResolveSquads(selected []string) ([]string, error) {
	normalized := dedupeStrings(selected)
	switch policy.Selection {
	case "", "automatic":
		if len(normalized) > 0 {
			return nil, fmt.Errorf("%w: %s", ErrSquadSelection, SquadSelectionRefused)
		}
		return dedupeStrings(policy.Included), nil
	case "optional", "required":
	default:
		return nil, fmt.Errorf("%w: %s", ErrSquadSelection, SquadSelectionRefused)
	}
	offered := make(map[string]struct{}, len(policy.Offered))
	for _, squad := range policy.Offered {
		offered[squad] = struct{}{}
	}
	for _, squad := range normalized {
		if _, ok := offered[squad]; !ok {
			return nil, fmt.Errorf("%w: %s", ErrSquadSelection, SquadSelectionUnknown)
		}
	}
	minimum := policy.Minimum
	if policy.Selection == "required" && minimum < 1 {
		minimum = 1
	}
	if len(normalized) < minimum {
		reason := SquadSelectionTooFew
		if len(normalized) == 0 && policy.Selection == "required" {
			reason = SquadSelectionRequired
		}
		return nil, fmt.Errorf("%w: %s", ErrSquadSelection, reason)
	}
	if policy.Maximum != nil && len(normalized) > *policy.Maximum {
		return nil, fmt.Errorf("%w: %s", ErrSquadSelection, SquadSelectionTooMany)
	}
	return dedupeStrings(append(append([]string{}, policy.Included...), normalized...)), nil
}

func dedupeStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	unique := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		unique = append(unique, trimmed)
	}
	sort.Strings(unique)
	return unique
}
