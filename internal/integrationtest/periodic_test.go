//go:build integration

package integrationtest

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/jobs"
	"github.com/riverqueue/river"
)

// TestAPeriodicPassRunsOnceAcrossWorkerReplicas is the reason the background
// loops became River periodic jobs. Two worker clients against one database
// stand in for two worker replicas: River elects one leader, only the leader
// enqueues periodic jobs, and the uniqueness on a pass keeps a second copy
// from being queued while the first is waiting or running. As tickers, each
// replica swept the same carts and debited the same wallets on its own.
func TestAPeriodicPassRunsOnceAcrossWorkerReplicas(t *testing.T) {
	harness := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var runs atomic.Int32
	started := make(chan struct{}, 4)
	for replica := range 2 {
		worker, schedule, err := jobs.NewPeriodic(slog.New(slog.DiscardHandler), jobs.Task{
			Name: "cart_sweep", Interval: time.Hour,
			Run: func(context.Context) error {
				runs.Add(1)
				started <- struct{}{}
				return nil
			},
		})
		if err != nil {
			t.Fatalf("replica %d: build periodic tasks: %v", replica, err)
		}
		workers := river.NewWorkers()
		river.AddWorker(workers, worker)
		client, err := jobs.NewWorkerClient(harness.pool, workers, schedule)
		if err != nil {
			t.Fatalf("replica %d: build client: %v", replica, err)
		}
		if err = client.Start(ctx); err != nil {
			t.Fatalf("replica %d: start: %v", replica, err)
		}
		t.Cleanup(func() {
			stopCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
			defer stop()
			_ = client.Stop(stopCtx)
		})
	}

	select {
	case <-started:
	case <-time.After(45 * time.Second):
		t.Fatal("no replica ran the pass; River elected no leader or enqueued nothing")
	}
	// Give the other replica every chance to run a second copy.
	time.Sleep(3 * time.Second)
	if got := runs.Load(); got != 1 {
		t.Fatalf("the pass ran %d times across two replicas, want 1", got)
	}

	// While a pass is queued, inserting it again collapses into the queued one.
	client, err := jobs.NewInsertOnlyClient(harness.pool)
	if err != nil {
		t.Fatalf("build insert-only client: %v", err)
	}
	args := jobs.PeriodicArgs{Task: "never_runs"}
	opts := jobs.PeriodicInsertOpts()
	opts.ScheduledAt = time.Now().Add(time.Hour)
	first, err := client.Insert(ctx, args, opts)
	if err != nil {
		t.Fatalf("insert pass: %v", err)
	}
	second, err := client.Insert(ctx, args, opts)
	if err != nil {
		t.Fatalf("insert pass again: %v", err)
	}
	if !second.UniqueSkippedAsDuplicate || second.Job.ID != first.Job.ID {
		t.Fatalf("a queued pass was queued twice: %d and %d", first.Job.ID, second.Job.ID)
	}
}
