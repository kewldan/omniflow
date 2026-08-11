package goodsdelivery

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/riverqueue/river"
)

// scanInterval is how often the delivery ledger is examined.
//
// A customer is waiting on the other side of every row in it, so this is fast
// compared to the reconciliation sweeps — but it is still a sweep rather than a
// tight loop, because the retry backoffs it honours are measured in minutes.
const scanInterval = 30 * time.Second

// Scheduler turns due deliveries into jobs.
//
// The delivery ledger, not River, is the source of truth about what is owed:
// `goods_deliveries` carries the attempt count, the backoff deadline, and the
// terminal states. The scheduler only translates "this row is due" into "run
// the worker", which means a lost job, a purged queue, or a restarted process
// costs a delay rather than a delivery.
type Scheduler struct {
	pool   *pgxpool.Pool
	client *river.Client[pgx.Tx]
	logger *slog.Logger
}

// NewScheduler builds the delivery scheduler.
func NewScheduler(pool *pgxpool.Pool, client *river.Client[pgx.Tx], logger *slog.Logger) *Scheduler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Scheduler{pool: pool, client: client, logger: logger}
}

// Run schedules due deliveries until the context is cancelled.
func (scheduler *Scheduler) Run(ctx context.Context) {
	ticker := time.NewTicker(scanInterval)
	defer ticker.Stop()
	for {
		if err := scheduler.Schedule(ctx); err != nil && ctx.Err() == nil {
			scheduler.logger.Error("digital goods scheduling failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Schedule enqueues one batch of due deliveries.
//
// Both `pending` and `submitted` rows are due-able, and that is what makes
// delayed delivery work: a provider that accepted a submission without
// completing it leaves the row submitted with its deadline pushed out, and the
// next pass asks the adapter what happened rather than submitting again. An
// adapter that cannot be polled reports that, and the row waits for the
// operator review queue instead.
//
// The job is unique by order, so enqueueing a row that is already queued
// collapses into the existing job rather than racing it.
func (scheduler *Scheduler) Schedule(ctx context.Context) error {
	rows, err := dbgen.New(scheduler.pool).ListDueGoodsDeliveries(ctx, 200)
	if err != nil {
		return err
	}
	for _, delivery := range rows {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		orderID := uuidString(delivery.OrderID)
		if _, err := scheduler.client.Insert(ctx, JobArgs{OrderID: orderID}, InsertOpts()); err != nil {
			scheduler.logger.Error("enqueue digital goods delivery failed",
				"orderId", orderID, "error", err)
		}
	}
	return nil
}
