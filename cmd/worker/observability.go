package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/omniflow/omniflow/internal/platform"
)

// queueSampleInterval is how often every worker replica samples the queues.
// Each replica samples on its own, rather than through a leader-only periodic
// pass, so whichever replica Prometheus scrapes reports a current value.
const queueSampleInterval = 15 * time.Second

// sampleQueues keeps the queue and outbox gauges current until ctx ends. Both
// queries read aggregate counts only, never a job's arguments.
func sampleQueues(ctx context.Context, logger *slog.Logger, pool *pgxpool.Pool, metrics *platform.Metrics) {
	if metrics == nil || pool == nil {
		return
	}
	ticker := time.NewTicker(queueSampleInterval)
	defer ticker.Stop()
	for {
		if err := sampleOnce(ctx, pool, metrics); err != nil && ctx.Err() == nil {
			logger.Warn("queue metrics sample failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func sampleOnce(ctx context.Context, pool *pgxpool.Pool, metrics *platform.Metrics) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	rows, err := pool.Query(ctx, `
SELECT queue, state::text, count(*)::bigint,
  COALESCE(extract(epoch FROM now() - min(scheduled_at)) FILTER (WHERE state = 'available'), 0)::float8
FROM river_job
WHERE state IN ('available', 'running', 'retryable', 'scheduled', 'discarded')
GROUP BY queue, state`)
	if err != nil {
		return err
	}
	byQueue := map[string]*platform.QueueSample{}
	for rows.Next() {
		var queue, state string
		var count int64
		var oldest float64
		if err := rows.Scan(&queue, &state, &count, &oldest); err != nil {
			rows.Close()
			return err
		}
		sample, ok := byQueue[queue]
		if !ok {
			sample = &platform.QueueSample{Queue: queue, States: map[string]int64{}}
			byQueue[queue] = sample
		}
		sample.States[state] = count
		if oldest > sample.OldestAvailableS {
			sample.OldestAvailableS = oldest
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	samples := make([]platform.QueueSample, 0, len(byQueue))
	for _, sample := range byQueue {
		samples = append(samples, *sample)
	}
	metrics.SetQueues(samples)

	var pending int64
	if err := pool.QueryRow(ctx,
		`SELECT count(*)::bigint FROM outbox_events WHERE published_at IS NULL`).Scan(&pending); err != nil {
		return err
	}
	metrics.SetOutboxPending(pending)

	// Any completed backup still on disk counts, scheduled or manual: the
	// question is how old the newest copy an operator could restore is.
	var lastBackup *time.Time
	if err := pool.QueryRow(ctx, `SELECT max(completed_at) FROM backups
WHERE status = 'completed' AND completed_at IS NOT NULL`).Scan(&lastBackup); err != nil {
		return err
	}
	if lastBackup == nil {
		metrics.SetBackupLastSuccess(time.Time{})
	} else {
		metrics.SetBackupLastSuccess(*lastBackup)
	}
	return nil
}
