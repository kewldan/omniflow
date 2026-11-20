package commercepg

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	databaseutil "github.com/omniflow/omniflow/internal/database"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// trafficPackBatch bounds one maintenance pass over expired traffic packs.
const trafficPackBatch = 200

// ExpireTrafficPacks takes every traffic pack whose lifetime has run out back
// off its entitlement and queues the lower limit for Remnawave.
//
// Each pack is its own transaction: the pack is marked expired, the allowance
// is lowered by exactly what it added, and a `set_limits` operation keyed on
// the pack is enqueued, all or nothing. The pack's `expired_at` predicate makes
// a replayed pass match nothing, so the traffic is never subtracted twice.
// It returns how many packs were taken back.
func (store *Store) ExpireTrafficPacks(ctx context.Context) (int, error) {
	due, err := dbgen.New(store.pool).ListDueTrafficPacks(ctx, trafficPackBatch)
	if err != nil {
		return 0, err
	}
	expired := 0
	for _, packID := range due {
		done, packErr := store.expireTrafficPack(ctx, packID)
		if packErr != nil {
			store.options.Logger.Warn("traffic pack expiry failed", "error", packErr, "pack_id", uuidString(packID))
			continue
		}
		if done {
			expired++
		}
	}
	return expired, ctx.Err()
}

func (store *Store) expireTrafficPack(ctx context.Context, packID pgtype.UUID) (bool, error) {
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := dbgen.New(tx)
	pack, err := queries.ExpireTrafficPack(ctx, packID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, tx.Commit(ctx)
	}
	if err != nil {
		return false, err
	}
	entitlement, err := queries.SubtractEntitlementTraffic(ctx, dbgen.SubtractEntitlementTrafficParams{
		EntitlementID: pack.EntitlementID, TrafficBytes: pack.TrafficBytes.Int64,
	})
	if err != nil {
		return false, err
	}
	desired, err := json.Marshal(map[string]any{
		"effectiveAt": store.clock().UTC(), "endsAt": entitlement.EndsAt.Time,
		"trafficAllowanceBytes": nullableInt8(entitlement.TrafficAllowanceBytes),
		"deviceLimit":           nullableInt4(entitlement.DeviceLimit),
		"squadIds":              databaseutil.UUIDStrings(entitlement.RemnawaveSquadIds),
	})
	if err != nil {
		return false, err
	}
	// A paused entitlement keeps its lower allowance in the row; the resume
	// pushes it, and Remnawave is not asked to change a user it holds disabled.
	if entitlement.Status != "paused" {
		operation, opErr := queries.CreateFulfillmentOperation(ctx, dbgen.CreateFulfillmentOperationParams{
			EntitlementID: entitlement.ID, Operation: "set_limits",
			IdempotencyKey: "addon:" + uuidString(pack.ID) + ":expire",
			CorrelationID:  "addon-expiry:" + uuidString(pack.ID), DesiredState: desired,
		})
		if opErr != nil {
			return false, opErr
		}
		if err = store.enqueue(ctx, tx, uuidString(operation.ID)); err != nil {
			return false, err
		}
	}
	return true, tx.Commit(ctx)
}
