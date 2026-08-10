package fulfillment

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	databaseutil "github.com/omniflow/omniflow/internal/database"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/riverqueue/river"
)

type Scheduler struct {
	pool   *pgxpool.Pool
	client *river.Client[pgx.Tx]
	clock  func() time.Time
}

func nullableInt8(value pgtype.Int8) any {
	if !value.Valid {
		return nil
	}
	return value.Int64
}

func nullableInt4(value pgtype.Int4) any {
	if !value.Valid {
		return nil
	}
	return value.Int32
}

func NewScheduler(pool *pgxpool.Pool, client *river.Client[pgx.Tx]) *Scheduler {
	return &Scheduler{pool: pool, client: client, clock: time.Now}
}

func (scheduler *Scheduler) Run(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()
	for {
		_ = scheduler.Schedule(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (scheduler *Scheduler) Schedule(ctx context.Context) error {
	rows, err := dbgen.New(scheduler.pool).ListEntitlementsForReconciliation(ctx, 500)
	if err != nil {
		return err
	}
	bucket := scheduler.clock().UTC().Truncate(15 * time.Minute).Format(time.RFC3339)
	for _, entitlement := range rows {
		tx, err := scheduler.pool.Begin(ctx)
		if err != nil {
			return err
		}
		queries := dbgen.New(tx)
		desired, _ := json.Marshal(map[string]any{"endsAt": entitlement.EndsAt.Time, "trafficAllowanceBytes": nullableInt8(entitlement.TrafficAllowanceBytes), "deviceLimit": nullableInt4(entitlement.DeviceLimit), "squadIds": databaseutil.UUIDStrings(entitlement.RemnawaveSquadIds)})
		operation, err := queries.CreateFulfillmentOperation(ctx, dbgen.CreateFulfillmentOperationParams{EntitlementID: entitlement.ID, Operation: "reconcile", IdempotencyKey: fmt.Sprintf("reconcile:%s:%s", uuidString(entitlement.ID), bucket), CorrelationID: "scheduler:" + bucket, DesiredState: desired})
		if err == nil {
			_, err = scheduler.client.InsertTx(ctx, tx, JobArgs{OperationID: uuidString(operation.ID)}, InsertOpts())
		}
		if err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err = tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}
