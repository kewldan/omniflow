// Package jobs builds the River clients the three processes use.
//
// There are exactly two shapes. The api and the bot insert jobs inside their
// own transactions and never run one, so they get an insert-only client. The
// worker runs every job and every cluster-wide periodic pass, so it gets a
// client with queues, workers, and the periodic schedule.
package jobs

import (
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

// Queue names. Fulfillment and digital-goods delivery run on critical, because
// a customer has already paid by the time either does; the cluster-wide
// periodic passes run on default.
const (
	QueueCritical = "critical"
	QueueDefault  = "default"
	QueueBulk     = "bulk"
)

// Queue concurrency is sized against the worker's connection pool (see
// config.DefaultWorkerDatabaseMaxConns): a running job may hold a connection
// for its whole transaction, and River itself needs a few for fetching,
// completing, and leader election. The sum stays well under the pool so a full
// set of running jobs never starves River's own bookkeeping.
var queues = map[string]river.QueueConfig{
	QueueCritical: {MaxWorkers: 16},
	QueueDefault:  {MaxWorkers: 8},
	QueueBulk:     {MaxWorkers: 4},
}

// MaxWorkers is the number of jobs the worker can run at once across every
// queue. The worker's pool must be larger than this.
func MaxWorkers() int {
	total := 0
	for _, queue := range queues {
		total += queue.MaxWorkers
	}
	return total
}

// Client is the River client every process holds.
type Client = river.Client[pgx.Tx]

// NewInsertOnlyClient builds the client the api and the bot use to insert jobs
// in the same transaction as the state change that requires them.
//
// It registers neither queues nor workers. That is what River recognises as
// insert-only: given an empty worker bundle instead of none, River checks every
// insert against it and refuses each kind as unknown, which is exactly how the
// api and the bot used to fail every fulfillment insert they attempted.
func NewInsertOnlyClient(pool *pgxpool.Pool) (*Client, error) {
	if pool == nil {
		return nil, errors.New("jobs: a database pool is required")
	}
	return river.NewClient(riverpgxv5.New(pool), &river.Config{})
}

// NewWorkerClient builds the worker's client: every queue, the given workers,
// and the cluster-wide periodic schedule. River elects one leader among the
// running worker clients and only the leader enqueues periodic jobs, so a pass
// runs once per interval however many worker replicas are up.
func NewWorkerClient(
	pool *pgxpool.Pool, workers *river.Workers, periodic []*river.PeriodicJob,
) (*Client, error) {
	if pool == nil {
		return nil, errors.New("jobs: a database pool is required")
	}
	if workers == nil {
		return nil, errors.New("jobs: a worker bundle is required")
	}
	return river.NewClient(riverpgxv5.New(pool), &river.Config{
		Queues:       queues,
		Workers:      workers,
		PeriodicJobs: periodic,
	})
}
