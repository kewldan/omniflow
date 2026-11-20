package commercepg

import (
	"context"
	"errors"
	"math/big"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/commerce"
	databaseutil "github.com/omniflow/omniflow/internal/database"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// The server catalogue labels and limits the sale of Remnawave squads; it
// never manages one (decision 0004). At checkout it does three things to the
// squads an order would grant, and a squad without a catalogue row is sold
// exactly as before:
//
//   - a trial keeps only the squads open to trials;
//   - a squad full to its customer limit is refused to a customer not
//     already on it, except on a renewal, which keeps who is there;
//   - a squad the customer chose adds its price, for the version's own
//     duration scaled to the period, to the plan's price.

// ErrServerFull refuses a squad that holds as many customers as the
// catalogue allows.
var ErrServerFull = errors.New("squad_full")

// applyServerRules applies the catalogue to the squads a quote resolved and
// returns the squads the order grants.
func applyServerRules(ctx context.Context, queries *dbgen.Queries, plan *dbgen.GetPlanVersionForOrderRow, userID pgtype.UUID,
	operation string, squads []string, ownSeconds int64) ([]string, error) {
	if len(squads) == 0 {
		return squads, nil
	}
	ids, err := databaseutil.ParseUUIDs(squads)
	if err != nil {
		return nil, err
	}
	rules, err := queries.ListServerRules(ctx, dbgen.ListServerRulesParams{Currency: plan.Currency, SquadIds: ids})
	if err != nil {
		return nil, err
	}
	if len(rules) == 0 {
		return squads, nil
	}
	bySquad := make(map[string]dbgen.ListServerRulesRow, len(rules))
	for _, rule := range rules {
		bySquad[uuidString(rule.SquadID)] = rule
	}
	if plan.Kind == "trial" {
		kept := make([]string, 0, len(squads))
		for _, squad := range squads {
			if rule, listed := bySquad[squad]; !listed || rule.TrialEligible {
				kept = append(kept, squad)
			}
		}
		if len(kept) == 0 {
			return nil, ErrSquadUnavailable
		}
		squads = kept
	}
	included := databaseutil.UUIDStrings(plan.RemnawaveSquadIds)
	surcharge := big.NewInt(0)
	for _, squad := range squads {
		rule, listed := bySquad[squad]
		if !listed {
			continue
		}
		if rule.MaxUsers.Valid && operation != "renewal" {
			holds, holdErr := queries.CustomerHoldsSquad(ctx, dbgen.CustomerHoldsSquadParams{UserID: userID, SquadID: rule.SquadID})
			if holdErr != nil {
				return nil, holdErr
			}
			if !holds {
				count, countErr := queries.CountSquadCustomers(ctx, dbgen.CountSquadCustomersParams{UserID: userID, SquadID: rule.SquadID})
				if countErr != nil {
					return nil, countErr
				}
				if count >= rule.MaxUsers.Int32 {
					return nil, ErrServerFull
				}
			}
		}
		if rule.PriceMinor.Valid && !slices.Contains(included, squad) {
			surcharge.Add(surcharge, big.NewInt(rule.PriceMinor.Int64))
		}
	}
	if surcharge.Sign() > 0 {
		amount, scaleErr := commerce.ScaleToPeriod(surcharge, time.Duration(plan.DurationSeconds)*time.Second, time.Duration(ownSeconds)*time.Second)
		if scaleErr != nil {
			return nil, scaleErr
		}
		if plan.AmountMinor > (1<<62)-amount {
			return nil, commerce.ErrInvalidAmount
		}
		plan.AmountMinor += amount
	}
	return squads, nil
}

// RenewalSquads is the squad choice an automatic renewal of a subscription
// keeps: the squads its last paid subscription order granted that the
// version it renews at still offers to choose. A version whose squads are
// assigned automatically has no choice to keep.
func (store *Store) RenewalSquads(ctx context.Context, subscriptionID, planVersionID string) ([]string, error) {
	subscription, err := parseUUID(subscriptionID)
	if err != nil {
		return nil, nil
	}
	versionID, err := parseUUID(planVersionID)
	if err != nil {
		return nil, nil
	}
	queries := dbgen.New(store.pool)
	last, err := queries.LastOrderSquads(ctx, subscription)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && len(last) == 0 {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	offered, err := queries.ListPlanVersionSquads(ctx, versionID)
	if err != nil {
		return nil, err
	}
	choosable := make(map[string]bool, len(offered))
	for _, squad := range offered {
		choosable[uuidString(squad.SquadID)] = true
	}
	kept := make([]string, 0, len(last))
	for _, squad := range databaseutil.UUIDStrings(last) {
		if choosable[squad] {
			kept = append(kept, squad)
		}
	}
	if len(kept) == 0 {
		return nil, nil
	}
	return kept, nil
}
