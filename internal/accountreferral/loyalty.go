package accountreferral

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omniflow/omniflow/internal/loyalty"
)

// Tier is one rung as the customer sees it.
//
// Both names are carried rather than one resolved server-side, because the
// panel already knows the customer's locale and resolving here would mean a
// cached response is wrong for the next reader.
type Tier struct {
	Code        string
	NameEN      string
	NameRU      string
	Threshold   int64
	DiscountBPS int
	// Current marks the tier the customer stands in, so the ladder renders
	// without the panel having to compare thresholds itself.
	Current bool
}

// LoyaltyRules are the terms that govern the ladder.
type LoyaltyRules struct {
	// Metric is what the tiers are measured on: spend, tenure, or orders.
	Metric   string
	Currency string
	// WindowDays is how far back the metric is measured. It is meaningless for
	// tenure, which measures forward from the first subscription.
	WindowDays int
	// GraceDays is how long a tier is held after the customer stops qualifying.
	// Zero means demotion is immediate, which an operator chooses explicitly.
	GraceDays int
	Version   int
}

// LoyaltyStanding is the loyalty screen.
type LoyaltyStanding struct {
	// Enabled is false when no programme is published. The panel shows a plain
	// "not enabled" state rather than an empty ladder that looks broken.
	Enabled bool
	Rules   LoyaltyRules
	Tiers   []Tier

	// Evaluated is false for a customer the evaluation worker has not placed
	// yet, or one whose standing was recorded under a superseded version. The
	// screen shows the ladder and says the placement is pending rather than
	// inventing one: computing a tier here would be a promotion decided by
	// whoever opened a page.
	Evaluated bool
	// Tier is the customer's current rung, valid only when Evaluated.
	Tier Tier
	// Next is the rung above, nil at the top of the ladder.
	Next *Tier
	// Metric is the value the standing was decided on, stored rather than
	// recomputed so "why am I silver?" is answered by the number actually used.
	Metric int64
	// Remaining is how much more of the metric the next tier needs, and Percent
	// is the progress through the current band. Both are computed on the server
	// so a progress bar and its textual equivalent cannot disagree.
	Remaining   int64
	Percent     int
	EvaluatedAt time.Time
	// GraceUntil is set when the tier is being held rather than earned. Nil means
	// the customer qualifies on their own.
	GraceUntil *time.Time
}

// Loyalty reads the customer's standing under the programme in force.
func (service *Service) Loyalty(ctx context.Context, customerID string) (LoyaltyStanding, error) {
	userID, err := parseUUID(customerID)
	if err != nil {
		return LoyaltyStanding{}, err
	}

	program, err := service.queries.GetEnabledLoyaltyProgram(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		// No programme in force is a normal state, not an error: loyalty is off
		// until an operator publishes one.
		return LoyaltyStanding{}, nil
	}
	if err != nil {
		return LoyaltyStanding{}, err
	}

	standing := LoyaltyStanding{
		Enabled: true,
		Rules: LoyaltyRules{
			Metric: program.Metric, Currency: program.Currency,
			WindowDays: int(program.WindowDays), GraceDays: int(program.GraceDays),
			Version: int(program.Version),
		},
	}

	ladder, err := service.queries.ListLoyaltyTiers(ctx, program.ID)
	if err != nil {
		return LoyaltyStanding{}, err
	}
	tiers := make([]loyalty.Tier, 0, len(ladder))
	for _, row := range ladder {
		tiers = append(tiers, loyalty.Tier{
			ID: uuidString(row.ID), Code: row.Code, NameEN: row.NameEn, NameRU: row.NameRu,
			Threshold: row.Threshold, DiscountBPS: int(row.DiscountBps),
		})
	}

	// A programme with no tiers cannot place anybody. The panel gets the "not
	// enabled" state rather than an empty ladder, because that is what it means
	// to the customer even though the row says enabled.
	if len(tiers) == 0 {
		return LoyaltyStanding{}, nil
	}

	// The shared read joins the held tier for the operator view. `tier_id` is a
	// NOT NULL foreign key, so the join can never drop a standing that exists.
	placed, err := service.queries.GetLoyaltyStanding(ctx, userID)
	held := placed.LoyaltyStanding
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Never evaluated. Show the ladder, place nobody.
		standing.Tiers = tierLadder(tiers, "")
		return standing, nil
	case err != nil:
		return LoyaltyStanding{}, err
	case uuidString(held.ProgramID) != uuidString(program.ID):
		// The standing was recorded under a superseded version. The old rung may
		// not exist on this ladder at all, so the honest answer is that the
		// placement is pending re-evaluation rather than a tier that has been
		// redefined underneath the customer.
		standing.Tiers = tierLadder(tiers, "")
		return standing, nil
	}

	current, next, remaining, percent := LoyaltyProgress(
		tiers, uuidString(held.TierID), held.EvaluatedMetric,
	)
	standing.Evaluated = true
	standing.Tier = customerTier(current, true)
	if next != nil {
		upper := customerTier(*next, false)
		standing.Next = &upper
	}
	standing.Metric = held.EvaluatedMetric
	standing.Remaining = remaining
	standing.Percent = percent
	standing.EvaluatedAt = held.EvaluatedAt.Time.UTC()
	standing.GraceUntil = timePointer(held.GraceUntil)
	standing.Tiers = tierLadder(tiers, current.Code)
	return standing, nil
}

// LoyaltyProgress places a stored standing on the ladder and measures the
// distance to the next rung.
//
// It reads the tier the standing already names rather than recomputing one from
// the metric. That distinction is the whole reason this is a read model: a
// customer inside their grace period holds a tier their current metric no longer
// earns, and recomputing here would demote them the moment they opened the page
// — before the evaluation worker, which owns that decision, had made it.
//
// Progress is measured through the band between the held tier's threshold and
// the next one, so a customer sees how far into their current rung they are
// rather than a fraction of some absolute total.
func LoyaltyProgress(
	tiers []loyalty.Tier, tierID string, metric int64,
) (current loyalty.Tier, next *loyalty.Tier, remaining int64, percent int) {
	ordered := loyalty.Sorted(tiers)
	if len(ordered) == 0 {
		return loyalty.Tier{}, nil, 0, 0
	}

	index := 0
	for position, tier := range ordered {
		if tier.ID == tierID {
			index = position
			break
		}
	}
	current = ordered[index]
	if index+1 >= len(ordered) {
		// The top rung. There is nothing left to reach, so the bar is full rather
		// than stuck at whatever fraction the last band happened to produce.
		return current, nil, 0, 100
	}

	upper := ordered[index+1]
	next = &upper
	remaining = max(upper.Threshold-metric, 0)

	band := upper.Threshold - current.Threshold
	if band <= 0 {
		return current, next, remaining, 0
	}
	// Clamped at both ends: a metric below the held threshold means the tier is
	// being carried through grace, and a metric above the next threshold means
	// the worker has not re-evaluated yet. Neither should render as a bar
	// pointing off the end of its track.
	progress := (metric - current.Threshold) * 100 / band
	return current, next, remaining, int(min(max(progress, 0), 100))
}

// tierLadder projects the whole ladder, marking the rung the customer holds.
func tierLadder(tiers []loyalty.Tier, currentCode string) []Tier {
	ordered := loyalty.Sorted(tiers)
	ladder := make([]Tier, 0, len(ordered))
	for _, tier := range ordered {
		ladder = append(ladder, customerTier(tier, tier.Code == currentCode && currentCode != ""))
	}
	return ladder
}

func customerTier(tier loyalty.Tier, current bool) Tier {
	return Tier{
		Code: tier.Code, NameEN: tier.NameEN, NameRU: tier.NameRU,
		Threshold: tier.Threshold, DiscountBPS: tier.DiscountBPS, Current: current,
	}
}
