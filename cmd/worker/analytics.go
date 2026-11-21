package main

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/omniflow/omniflow/internal/analytics"
	"github.com/omniflow/omniflow/internal/jobs"
)

// analyticsTasks keeps the history business analytics reads: the daily
// snapshot of the subscription base and hourly node traffic samples. Both
// are hourly. The snapshot pass rewrites today's reading each time and, on a
// new installation, backfills a year of history a couple of months per pass.
func analyticsTasks(pool *pgxpool.Pool, panel analytics.NodeLister) []jobs.Task {
	service := analytics.New(pool)
	tasks := []jobs.Task{{
		Name: "subscription_snapshot", Interval: time.Hour, Timeout: 20 * time.Minute,
		Run: func(ctx context.Context) error {
			_, err := service.TakeSnapshots(ctx)
			return err
		},
	}}
	if panel != nil {
		tasks = append(tasks, jobs.Task{
			Name: "node_traffic_sample", Interval: time.Hour, Timeout: 5 * time.Minute,
			Run: func(ctx context.Context) error {
				_, err := service.SampleNodes(ctx, panel)
				return err
			},
		})
	}
	return tasks
}
