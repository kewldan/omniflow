// Package loyalty decides which tier a customer stands in.
//
// The evaluation is deterministic and has no database in it, which is the
// point: a customer asking "why am I silver?" deserves an answer that can be
// reproduced from the numbers, and an operator changing a threshold needs to be
// able to see what the change would do before publishing it.
//
// Two rules shape everything here.
//
// A definition is immutable once published. A customer who reached gold under
// one set of thresholds should not silently fall out of it because somebody
// edited the numbers, so editing publishes a new version and the old one keeps
// explaining the assignments made under it.
//
// Demotion is slower than promotion. A customer who qualifies is promoted
// immediately; one who falls below keeps their tier for the configured grace
// period first. A loyalty programme that demotes somebody for one quiet month
// is one that punishes the customers it was built to keep.
package loyalty

import (
	"errors"
	"sort"
	"time"
)

// Metrics a programme can be measured on. Each is a fact Omniflow already
// records — deliberately, because a loyalty model that needs new tracking is a
// loyalty model that needs new consent.
const (
	MetricSpend  = "spend"
	MetricTenure = "tenure"
	MetricOrders = "orders"
)

// ErrNoTiers reports a programme with nothing to assign. It is a configuration
// error rather than a runtime one: publishing a programme with no tiers would
// leave every customer in no tier at all.
var ErrNoTiers = errors.New("a loyalty programme needs at least one tier")

// Tier is one rung.
type Tier struct {
	ID     string
	Code   string
	NameEN string
	NameRU string
	// Threshold is the metric value this tier starts at. The lowest tier is
	// expected to be zero so every customer has one.
	Threshold int64
	// DiscountBPS discounts a plan's price unless PeriodDiscountBPS names the
	// plan's billing period.
	DiscountBPS int
	// PeriodDiscountBPS overrides DiscountBPS for a billing period — Bedolaga's
	// per-period discounts, such as more off a year than a month.
	PeriodDiscountBPS map[string]int
	// Add-on discounts by kind: traffic packs, extra devices, extra squads.
	TrafficDiscountBPS int
	DeviceDiscountBPS  int
	SquadDiscountBPS   int
	// Priority decides which tier prices an order when a customer holds more
	// than one: the highest wins.
	Priority int
	// ManualOnly tiers are never reached by the metric; a customer holds one
	// only when an operator or a promo code grants it.
	ManualOnly bool
	// Restrictions on what a member may buy and how they may pay. Empty means
	// no restriction.
	AllowedPlanIDs   []string
	AllowedSquadIDs  []string
	AllowedProviders []string
}

// PlanDiscountBPS is the tier's discount on a plan with this billing period.
func (tier Tier) PlanDiscountBPS(billingPeriod string) int {
	if bps, ok := tier.PeriodDiscountBPS[billingPeriod]; ok {
		return bps
	}
	return tier.DiscountBPS
}

// AddonDiscountBPS is the tier's discount on an add-on of this kind.
func (tier Tier) AddonDiscountBPS(kind string) int {
	switch kind {
	case "traffic":
		return tier.TrafficDiscountBPS
	case "devices":
		return tier.DeviceDiscountBPS
	case "squads":
		return tier.SquadDiscountBPS
	default:
		return 0
	}
}

// AllowsPlan reports whether a member may buy this plan.
func (tier Tier) AllowsPlan(planID string) bool {
	return allowed(tier.AllowedPlanIDs, planID)
}

// AllowsProvider reports whether a member may pay through this provider.
func (tier Tier) AllowsProvider(provider string) bool {
	return allowed(tier.AllowedProviders, provider)
}

// AllowedSquads narrows a set of squads to the ones a member may choose. It
// returns the set unchanged when the tier restricts nothing.
func (tier Tier) AllowedSquads(squads []string) []string {
	if len(tier.AllowedSquadIDs) == 0 {
		return squads
	}
	kept := make([]string, 0, len(squads))
	for _, squad := range squads {
		if allowed(tier.AllowedSquadIDs, squad) {
			kept = append(kept, squad)
		}
	}
	return kept
}

func allowed(list []string, value string) bool {
	if len(list) == 0 {
		return true
	}
	for _, candidate := range list {
		if candidate == value {
			return true
		}
	}
	return false
}

// DiscountAt is a share of an amount in basis points, rounded down.
func DiscountAt(amountMinor int64, bps int) int64 {
	if bps <= 0 || amountMinor <= 0 {
		return 0
	}
	return amountMinor * int64(min(bps, 10_000)) / 10_000
}

// Program is one published definition.
type Program struct {
	ID      string
	Version int
	Metric  string
	// Window is how far back the metric is measured. It is meaningless for
	// tenure, which measures forward from the first subscription.
	Window time.Duration
	// Grace is how long a tier is held after the customer stops qualifying.
	Grace time.Duration
	Tiers []Tier
}

// Standing is where a customer stands and how it was arrived at.
type Standing struct {
	TierID string
	// Metric is the value the decision was made on. It is stored rather than
	// recomputed on read, so the answer to "why?" is the number that was
	// actually used.
	Metric int64
	// GraceUntil is set when the tier is being held rather than earned. A nil
	// value means the customer currently qualifies on their own.
	GraceUntil *time.Time
	// Changed reports whether this differs from what the customer had, which is
	// what decides whether history is written.
	Changed bool
}

// Evaluate places a customer in a tier.
//
// `current` is what they hold now, and may be a zero Standing for a customer
// who has never been evaluated. `now` is passed rather than read so the same
// inputs always produce the same result — a loyalty decision that depends on
// the clock is one nobody can reproduce when a customer disputes it.
func Evaluate(
	program Program, current Standing, metric int64, now time.Time,
) (Standing, error) {
	earned, err := TierFor(program, metric)
	if err != nil {
		return Standing{}, err
	}

	// Nothing held yet, or the customer qualifies for at least what they hold:
	// take the earned tier outright and clear any grace.
	if current.TierID == "" {
		return Standing{
			TierID: earned.ID, Metric: metric,
			Changed: true,
		}, nil
	}
	held, holding := tierByID(program, current.TierID)
	if !holding {
		// The held tier belongs to a previous programme version. Re-place the
		// customer under the definition now in force rather than pretending the
		// old rung still exists.
		return Standing{TierID: earned.ID, Metric: metric, Changed: true}, nil
	}

	if earned.Threshold >= held.Threshold {
		// Promotion, or holding the same tier on merit. Either way the grace
		// clock is cleared: the customer is not being carried.
		return Standing{
			TierID: earned.ID, Metric: metric,
			Changed: earned.ID != current.TierID,
		}, nil
	}

	// Below what they hold. Start the grace clock if it is not already running,
	// and demote only once it has run out.
	if program.Grace <= 0 {
		return Standing{TierID: earned.ID, Metric: metric, Changed: true}, nil
	}
	if current.GraceUntil == nil {
		until := now.Add(program.Grace)
		return Standing{
			TierID: current.TierID, Metric: metric, GraceUntil: &until, Changed: false,
		}, nil
	}
	if now.Before(*current.GraceUntil) {
		return Standing{
			TierID: current.TierID, Metric: metric,
			GraceUntil: current.GraceUntil, Changed: false,
		}, nil
	}
	return Standing{TierID: earned.ID, Metric: metric, Changed: true}, nil
}

// TierFor returns the highest tier a metric reaches. Manual-only tiers are
// never reached by the metric.
func TierFor(program Program, metric int64) (Tier, error) {
	ordered := Sorted(automatic(program.Tiers))
	if len(ordered) == 0 {
		return Tier{}, ErrNoTiers
	}
	earned := ordered[0]
	for _, tier := range ordered {
		if metric >= tier.Threshold {
			earned = tier
			continue
		}
		break
	}
	return earned, nil
}

// Sorted returns the tiers lowest threshold first. It copies rather than
// sorting in place, because the caller's slice usually came straight from a
// query and reordering it would surprise them.
func Sorted(tiers []Tier) []Tier {
	ordered := make([]Tier, len(tiers))
	copy(ordered, tiers)
	sort.Slice(ordered, func(left, right int) bool {
		return ordered[left].Threshold < ordered[right].Threshold
	})
	return ordered
}

// Validate reports whether a programme can be published.
//
// The checks are the ones a CHECK constraint cannot express: that there is a
// tier every customer reaches, and that the discounts increase with the
// thresholds. A gold tier worth less than silver is representable and is
// certainly a mistake.
func Validate(program Program) error {
	ordered := Sorted(automatic(program.Tiers))
	if len(ordered) == 0 {
		return ErrNoTiers
	}
	if ordered[0].Threshold != 0 {
		return errors.New("the lowest tier must start at zero so every customer has one")
	}
	previous := -1
	for _, tier := range ordered {
		if tier.DiscountBPS < previous {
			return errors.New("a higher tier must not be worth less than a lower one")
		}
		previous = tier.DiscountBPS
	}
	for _, tier := range program.Tiers {
		for period, bps := range tier.PeriodDiscountBPS {
			if !billingPeriods[period] || bps < 0 || bps > 10_000 {
				return errors.New("a period discount names an unknown period or is out of range")
			}
		}
		for _, bps := range []int{tier.DiscountBPS, tier.TrafficDiscountBPS, tier.DeviceDiscountBPS, tier.SquadDiscountBPS} {
			if bps < 0 || bps > 10_000 {
				return errors.New("a discount is out of range")
			}
		}
	}
	switch program.Metric {
	case MetricSpend, MetricTenure, MetricOrders:
	default:
		return errors.New("unsupported loyalty metric")
	}
	if program.Window <= 0 && program.Metric != MetricTenure {
		return errors.New("a windowed metric needs a window")
	}
	return nil
}

// billingPeriods are the plan billing periods a period discount may name.
var billingPeriods = map[string]bool{
	"none": true, "day": true, "week": true, "month": true, "quarter": true,
	"half_year": true, "year": true, "custom": true,
}

// automatic is the tiers the metric can reach.
func automatic(tiers []Tier) []Tier {
	kept := make([]Tier, 0, len(tiers))
	for _, tier := range tiers {
		if !tier.ManualOnly {
			kept = append(kept, tier)
		}
	}
	return kept
}

// Discount is the tier's share of an order, in minor units.
//
// It rounds down rather than up, which is the opposite of the markup rule in
// the shop and for the same reason: rounding should never favour the operator
// at the customer's expense, and here the operator is the one giving the
// discount.
func Discount(amountMinor int64, tier Tier) int64 {
	if tier.DiscountBPS <= 0 || amountMinor <= 0 {
		return 0
	}
	return amountMinor * int64(tier.DiscountBPS) / 10_000
}

// Best picks the tier that prices a customer's orders when they hold more than
// one — the earned tier and any granted groups — and reports false when they
// hold none. The highest priority wins, as Bedolaga's promo groups do; between
// equal priorities the more generous plan discount wins, so holding a second
// group never makes a price worse by accident.
func Best(tiers []Tier) (Tier, bool) {
	if len(tiers) == 0 {
		return Tier{}, false
	}
	best := tiers[0]
	for _, tier := range tiers[1:] {
		if tier.Priority > best.Priority ||
			tier.Priority == best.Priority && tier.DiscountBPS > best.DiscountBPS {
			best = tier
		}
	}
	return best, true
}

func tierByID(program Program, id string) (Tier, bool) {
	for _, tier := range program.Tiers {
		if tier.ID == id {
			return tier, true
		}
	}
	return Tier{}, false
}
