package panelpg

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// loyaltyCodePattern matches loyalty_tiers.code.
var loyaltyCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,31}$`)

// LoyaltyMembership is a group an operator or a promo code granted a customer,
// beside whatever tier their metric earns.
type LoyaltyMembership struct {
	TierCode  string     `json:"tierCode"`
	Source    string     `json:"source"`
	Note      string     `json:"note,omitempty"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
}

// AssignLoyaltyGroup grants a customer a group of the programme in force, by
// code, optionally until a moment. Granting writes a history entry, which is
// what tells the customer and the operators about it.
//
// A customer may hold several groups; the one with the highest priority prices
// their orders. Granting the same group again replaces the note and keeps the
// later expiry.
func (service *Service) AssignLoyaltyGroup(
	ctx context.Context, customerID, tierCode, note string, expiresAt *time.Time, actor Actor,
) (LoyaltyMembership, error) {
	tierCode = strings.ToLower(strings.TrimSpace(tierCode))
	if !loyaltyCodePattern.MatchString(tierCode) || len([]rune(note)) > 400 {
		return LoyaltyMembership{}, ErrValidaton
	}
	if expiresAt != nil && !expiresAt.After(time.Now()) {
		return LoyaltyMembership{}, ErrValidaton
	}
	id, err := parseUUID(customerID)
	if err != nil {
		return LoyaltyMembership{}, err
	}
	var membership LoyaltyMembership
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		tier, txErr := queries.GetEnabledLoyaltyTierByCode(ctx, tierCode)
		if errors.Is(txErr, pgx.ErrNoRows) {
			// Only a group of the programme in force can be granted: one that
			// exists nowhere would price nothing and mislead whoever reads it.
			return ErrValidaton
		}
		if txErr != nil {
			return txErr
		}
		var until pgtype.Timestamptz
		if expiresAt != nil {
			until = pgtype.Timestamptz{Time: expiresAt.UTC(), Valid: true}
		}
		row, txErr := queries.UpsertLoyaltyMembership(ctx, dbgen.UpsertLoyaltyMembershipParams{
			UserID: id, TierCode: tierCode, Source: "operator",
			AssignedBy: optionalUUID(actor.AdminID), Note: note, ExpiresAt: until,
		})
		if txErr != nil {
			return notFound(txErr)
		}
		membership = membershipFrom(row)
		var from pgtype.UUID
		if standing, standingErr := queries.GetLoyaltyStanding(ctx, id); standingErr == nil {
			from = standing.LoyaltyStanding.TierID
		}
		if _, txErr = queries.RecordLoyaltyChange(ctx, dbgen.RecordLoyaltyChangeParams{
			UserID: id, FromTierID: from, ToTierID: tier.ID, Reason: "operator_override",
			ActorID: optionalUUID(actor.AdminID),
		}); txErr != nil {
			return txErr
		}
		return appendAudit(ctx, queries, actor.audit(
			"panel.loyalty_group.assigned", "marketing", "customer", customerID,
			map[string]any{"tier": tierCode, "note": note, "expiresAt": membership.ExpiresAt},
		))
	})
	return membership, err
}

// RemoveLoyaltyGroup takes a granted group away. The tier a customer's metric
// earns is not a membership and cannot be removed this way; re-evaluation
// decides it.
func (service *Service) RemoveLoyaltyGroup(ctx context.Context, customerID, tierCode string, actor Actor) error {
	id, err := parseUUID(customerID)
	if err != nil {
		return err
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		removed, txErr := queries.DeleteLoyaltyMembership(ctx, dbgen.DeleteLoyaltyMembershipParams{
			UserID: id, TierCode: strings.ToLower(strings.TrimSpace(tierCode)),
		})
		if txErr != nil {
			return txErr
		}
		if removed == 0 {
			return ErrNotFound
		}
		return appendAudit(ctx, queries, actor.audit(
			"panel.loyalty_group.removed", "marketing", "customer", customerID,
			map[string]any{"tier": tierCode},
		))
	})
}

// CustomerLoyaltyMemberships lists the groups a customer was granted.
func (service *Service) CustomerLoyaltyMemberships(ctx context.Context, customerID string) ([]LoyaltyMembership, error) {
	id, err := parseUUID(customerID)
	if err != nil {
		return nil, err
	}
	rows, err := service.queries().ListLoyaltyMemberships(ctx, id)
	if err != nil {
		return nil, err
	}
	memberships := make([]LoyaltyMembership, 0, len(rows))
	for _, row := range rows {
		memberships = append(memberships, membershipFrom(row))
	}
	return memberships, nil
}

// RequestLoyaltyRecalculation asks the worker to re-evaluate every customer
// under the programme in force — Bedolaga's bulk recalculation after a change
// to the thresholds or a data import.
func (service *Service) RequestLoyaltyRecalculation(ctx context.Context, actor Actor) error {
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		program, txErr := queries.RequestLoyaltyRecalculation(ctx)
		if errors.Is(txErr, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if txErr != nil {
			return txErr
		}
		return appendAudit(ctx, queries, actor.audit(
			"panel.loyalty_program.recalculation_requested", "marketing", "loyalty_program",
			uuidString(program.ID), map[string]any{"version": program.Version},
		))
	})
}

// loyaltySweepBatch bounds one page of the loyalty pass.
const loyaltySweepBatch = 200

// LoyaltySweep is the worker's loyalty pass: Bedolaga's automatic promo-group
// assignment by spend. It evaluates every customer whose standing may have
// moved — a payment settled since their last evaluation, or a grace period
// that ran out — and, when an operator asked for a recalculation or a new
// programme was published, everybody once.
//
// Evaluation is idempotent and writes history only on a change, so a pass
// that repeats one customer changes nothing.
func (service *Service) LoyaltySweep(ctx context.Context, now time.Time) error {
	queries := service.queries()
	program, err := queries.GetEnabledLoyaltyProgram(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	everybody := program.RecalculationRequestedAt.Valid &&
		(!program.RecalculatedAt.Valid || program.RecalculatedAt.Time.Before(program.RecalculationRequestedAt.Time))
	started := now
	after := pgtype.UUID{Valid: true}
	system := Actor{Type: "system"}
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		ids, err := queries.ListLoyaltyEvaluationCandidates(ctx, dbgen.ListLoyaltyEvaluationCandidatesParams{
			After: after, Everybody: everybody, PageSize: loyaltySweepBatch,
		})
		if err != nil {
			return err
		}
		for _, id := range ids {
			if _, err := service.EvaluateLoyalty(ctx, uuidString(id), now, system); err != nil {
				return err
			}
		}
		if len(ids) < loyaltySweepBatch {
			break
		}
		after = ids[len(ids)-1]
	}
	if everybody {
		return queries.MarkLoyaltyRecalculated(ctx, dbgen.MarkLoyaltyRecalculatedParams{
			ProgramID: program.ID, FinishedAt: pgtype.Timestamptz{Time: started, Valid: true},
		})
	}
	return nil
}

func membershipFrom(row dbgen.LoyaltyMembership) LoyaltyMembership {
	membership := LoyaltyMembership{
		TierCode: row.TierCode, Source: row.Source, Note: row.Note, CreatedAt: timeValue(row.CreatedAt),
	}
	if row.ExpiresAt.Valid {
		expires := timeValue(row.ExpiresAt)
		membership.ExpiresAt = &expires
	}
	return membership
}
