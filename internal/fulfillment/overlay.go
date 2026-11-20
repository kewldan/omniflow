package fulfillment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	databaseutil "github.com/omniflow/omniflow/internal/database"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// limitedOverlay is an open limited access period: while it lasts, every
// operation on its entitlement pushes one squad and a small allowance instead
// of the plan, so a reconcile keeps it rather than undoing it.
type limitedOverlay struct {
	sessionID    pgtype.UUID
	trafficBytes int64
	// base is the used bytes the period was opened on, nil until the
	// operation that applies it has read them from Remnawave.
	base *int64
}

// overlaid reports whether an operation pushes the entitlement's state, and so
// carries whatever shapes it beyond the plan. A pause, a disable and a close
// do not push a state at all.
func overlaid(operation string) bool {
	switch operation {
	case "create", "extend", "set_limits", "set_squads", "reconcile", "resume", "limit_access", "restore_access":
		return true
	default:
		return false
	}
}

// endScheduledAccess ends the limited access period a `restore_access`
// operation was scheduled to end, once its time has come. A period a payment
// already restored is left as it is.
func endScheduledAccess(ctx context.Context, queries *dbgen.Queries, desired desiredState, now time.Time) error {
	if desired.SessionID == "" {
		return nil
	}
	id, err := parseUUID(desired.SessionID)
	if err != nil {
		return nil
	}
	session, err := queries.GetLimitedAccess(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if session.Status == "active" && !now.Before(session.EndsAt.Time) {
		return queries.EndLimitedAccess(ctx, id)
	}
	return nil
}

// withAccessOverlay folds an open limited access period into the desired
// state: one squad, an allowance of the used bytes plus the period's, and for
// an expired subscription the period's end as the expiry.
func withAccessOverlay(ctx context.Context, queries *dbgen.Queries, entitlementID pgtype.UUID, desired desiredState) (desiredState, error) {
	session, err := queries.GetActiveLimitedAccess(ctx, entitlementID)
	if errors.Is(err, pgx.ErrNoRows) {
		return desired, nil
	}
	if err != nil {
		return desired, err
	}
	desired.SquadIDs = []string{uuidString(session.SquadID)}
	if session.Trigger == "expiry" {
		desired.remoteExpireAt = session.EndsAt.Time
	}
	overlay := &limitedOverlay{sessionID: session.ID, trafficBytes: session.TrafficBytes}
	if session.BaseUsedBytes.Valid {
		base := session.BaseUsedBytes.Int64
		overlay.base = &base
		limit := base + session.TrafficBytes
		desired.TrafficAllowanceBytes = &limit
	}
	desired.limited = overlay
	return desired, nil
}

// LimitedAccessInterval is how often subscriptions that expired or ran out of
// traffic are looked for.
const LimitedAccessInterval = time.Minute

// limitedAccessBatch bounds one pass.
const limitedAccessBatch = 200

// LimitedAccess opens a limited access period for every subscription that has
// just expired or run out of traffic while the installation offers one, and
// closes periods nobody closed. Opening is one transaction per subscription:
// the period, the `limit_access` operation that applies it, and the
// `restore_access` operation scheduled for its end commit together.
func (scheduler *Scheduler) LimitedAccess(ctx context.Context) error {
	queries := dbgen.New(scheduler.pool)
	if _, err := queries.EndStaleLimitedAccess(ctx); err != nil {
		return err
	}
	candidates, err := queries.ListLimitedAccessCandidates(ctx, limitedAccessBatch)
	if err != nil {
		return err
	}
	for _, candidate := range candidates {
		if err = scheduler.openLimitedAccess(ctx, candidate); err != nil {
			return err
		}
	}
	return nil
}

func (scheduler *Scheduler) openLimitedAccess(ctx context.Context, candidate dbgen.ListLimitedAccessCandidatesRow) error {
	tx, err := scheduler.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := dbgen.New(tx)
	session, err := queries.OpenLimitedAccess(ctx, dbgen.OpenLimitedAccessParams{
		UserID: candidate.UserID, SubscriptionID: candidate.SubscriptionID,
		EntitlementID: candidate.EntitlementID, Trigger: candidate.Trigger,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	entitlement, err := queries.GetEntitlement(ctx, candidate.EntitlementID)
	if err != nil {
		return err
	}
	key := "limited:" + uuidString(session.ID)
	for _, step := range []struct {
		operation, suffix string
		effectiveAt       time.Time
	}{
		{"limit_access", ":start", scheduler.clock().UTC()},
		{"restore_access", ":end", session.EndsAt.Time},
	} {
		desired, marshalErr := json.Marshal(map[string]any{
			"effectiveAt": step.effectiveAt, "endsAt": entitlement.EndsAt.Time,
			"trafficAllowanceBytes": nullableInt8(entitlement.TrafficAllowanceBytes),
			"deviceLimit":           nullableInt4(entitlement.DeviceLimit),
			"squadIds":              databaseutil.UUIDStrings(entitlement.RemnawaveSquadIds),
			"sessionId":             uuidString(session.ID),
		})
		if marshalErr != nil {
			return marshalErr
		}
		operation, createErr := queries.CreateFulfillmentOperation(ctx, dbgen.CreateFulfillmentOperationParams{
			EntitlementID: entitlement.ID, Operation: step.operation,
			IdempotencyKey: key + step.suffix, CorrelationID: "limited-access:" + candidate.Trigger,
			DesiredState: desired,
		})
		if createErr != nil {
			return createErr
		}
		if _, err = scheduler.client.InsertTx(ctx, tx, JobArgs{OperationID: uuidString(operation.ID)}, InsertOpts()); err != nil {
			return fmt.Errorf("queue %s: %w", step.operation, err)
		}
	}
	return tx.Commit(ctx)
}
