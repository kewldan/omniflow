package panelpg

import (
	"context"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// PlanOptions are the plan-level settings that are not presentation: they
// change what a customer on the plan gets or who may buy it, so they are saved
// apart from visibility and ordering.
type PlanOptions struct {
	// RemnawaveTag is the user tag every subscription on the plan carries in
	// the Remnawave panel. Empty falls back to the installation's trial or
	// paid tag.
	RemnawaveTag string `json:"remnawaveTag"`
}

// panelTagPattern is the shape the Remnawave panel accepts for a user tag.
var panelTagPattern = regexp.MustCompile(`^[A-Z0-9_]{1,16}$`)

// normalizePanelTag upper-cases a tag, as the panel only accepts capitals, and
// refuses one it would reject. Empty means no tag.
func normalizePanelTag(value string) (pgtype.Text, error) {
	tag := strings.ToUpper(strings.TrimSpace(value))
	if tag == "" {
		return pgtype.Text{}, nil
	}
	if !panelTagPattern.MatchString(tag) {
		return pgtype.Text{}, ErrValidaton
	}
	return pgtype.Text{String: tag, Valid: true}, nil
}

// SavePlanOptions stores a plan's options. The tag reaches existing
// subscriptions the next time their entitlement is pushed to Remnawave — a
// renewal, a change, or the reconciliation sweep.
func (service *Service) SavePlanOptions(
	ctx context.Context, planID string, options PlanOptions, actor Actor,
) (PlanOptions, error) {
	id, err := parseUUID(planID)
	if err != nil {
		return PlanOptions{}, err
	}
	tag, err := normalizePanelTag(options.RemnawaveTag)
	if err != nil {
		return PlanOptions{}, err
	}
	var saved PlanOptions
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		plan, txErr := queries.SetPlanRemnawaveTag(ctx, dbgen.SetPlanRemnawaveTagParams{
			PlanID: id, RemnawaveTag: tag,
		})
		if txErr != nil {
			return notFound(txErr)
		}
		saved = PlanOptions{RemnawaveTag: plan.RemnawaveTag.String}
		return appendAudit(ctx, queries, actor.audit(
			"panel.plan.options_updated", "configuration", "plan", planID,
			map[string]any{"remnawaveTag": saved.RemnawaveTag},
		))
	})
	return saved, err
}
