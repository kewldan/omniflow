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
	"github.com/riverqueue/river/rivertype"
)

// JobInserter is the part of a River client the scheduler uses. It is an
// interface so a test can observe which operations were queued without a
// River schema behind it; `*river.Client[pgx.Tx]` satisfies it directly.
type JobInserter interface {
	InsertTx(context.Context, pgx.Tx, river.JobArgs, *river.InsertOpts) (*rivertype.JobInsertResult, error)
}

// reconcileInterval is how often entitlements are re-read from Remnawave.
const reconcileInterval = 15 * time.Minute

// reviveInterval is how often stalled operations are looked for.
const reviveInterval = 5 * time.Minute

// staleOperationAge is how long a pending or retrying operation may sit past
// its next scheduled attempt before the scheduler assumes its job is gone.
//
// It is measured from the attempt the worker itself scheduled, so a retry
// waiting out its backoff is never mistaken for an orphan. Ten minutes is far
// longer than a healthy queue takes to start a due job, and short enough that
// a customer whose payment settled without a job is provisioned before they
// give up and write to support.
const staleOperationAge = 10 * time.Minute

// reviveBatchSize bounds one revival pass.
const reviveBatchSize = 500

// ReconciliationHorizon is how long after its end an entitlement keeps being
// reconciled.
//
// Remnawave expires the user on its own at the pushed expiry; after that the
// only thing a reconcile can do is re-push an expiry that has already passed,
// or recreate a user an operator deleted on purpose — forever, every fifteen
// minutes, one operation row and one job each time. Thirty days outlasts the
// grace and recovery windows an installation configures, after which the row
// is history rather than state. A customer who returns later buys a new
// entitlement, which is provisioned by its own `create`.
const ReconciliationHorizon = 30 * 24 * time.Hour

type Scheduler struct {
	pool   *pgxpool.Pool
	client JobInserter
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

func NewScheduler(pool *pgxpool.Pool, client JobInserter) *Scheduler {
	return &Scheduler{pool: pool, client: client, clock: time.Now}
}

// Run drives both periodic passes until the context is cancelled: the
// reconciliation sweep every fifteen minutes and the revival scan every five.
func (scheduler *Scheduler) Run(ctx context.Context) {
	reconcile := time.NewTicker(reconcileInterval)
	defer reconcile.Stop()
	revive := time.NewTicker(reviveInterval)
	defer revive.Stop()
	_ = scheduler.Schedule(ctx)
	_ = scheduler.Revive(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-reconcile.C:
			_ = scheduler.Schedule(ctx)
		case <-revive.C:
			_ = scheduler.Revive(ctx)
		}
	}
}

func (scheduler *Scheduler) Schedule(ctx context.Context) error {
	rows, err := dbgen.New(scheduler.pool).ListEntitlementsForReconciliation(ctx, dbgen.ListEntitlementsForReconciliationParams{
		EndedAfter: pgtype.Timestamptz{Time: scheduler.clock().UTC().Add(-ReconciliationHorizon), Valid: true},
		PageSize:   500,
	})
	if err != nil {
		return err
	}
	bucket := scheduler.clock().UTC().Truncate(reconcileInterval).Format(time.RFC3339)
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

// Revive re-queues operations whose job never ran or was discarded.
//
// Settlement inserts the fulfillment job in the paying transaction, so in the
// ordinary course an operation always has a job. This pass is for the two
// cases where it does not: a process that settled without a job inserter, and
// a job River discarded after its last attempt. The insert carries the same
// unique options as the original, so an operation whose job is simply queued
// or retrying is left exactly as it was.
func (scheduler *Scheduler) Revive(ctx context.Context) error {
	before := scheduler.clock().UTC().Add(-staleOperationAge)
	rows, err := dbgen.New(scheduler.pool).ListStalledFulfillmentOperations(ctx, dbgen.ListStalledFulfillmentOperationsParams{
		Before: pgtype.Timestamptz{Time: before, Valid: true}, PageSize: reviveBatchSize,
	})
	if err != nil {
		return err
	}
	for _, operation := range rows {
		if !StaleOperation(operation.Status, operation.CreatedAt.Time, operation.NextAttemptAt.Time, before) {
			continue
		}
		tx, err := scheduler.pool.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err = scheduler.client.InsertTx(ctx, tx, JobArgs{OperationID: uuidString(operation.ID)}, InsertOpts()); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err = tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

// StaleOperation is the revival predicate: an operation still waiting to run
// whose creation and its next scheduled attempt both lie before the cut-off.
// Both instants are checked so a retry scheduled for the future is never
// treated as stuck, and a freshly created operation is given time for the job
// that was inserted with it.
func StaleOperation(status string, createdAt, nextAttemptAt, before time.Time) bool {
	if status != "pending" && status != "retrying" {
		return false
	}
	return createdAt.Before(before) && nextAttemptAt.Before(before)
}
