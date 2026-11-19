package jobs

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Gate turns a pass whose interval an operator can change into one River
// schedules on a short fixed tick.
//
// River fixes a periodic job's interval when the worker starts, so a pass
// whose cadence lives in the installation settings is registered with a short
// tick and asks the gate on every run whether it is actually due. The gate
// claims the run in PostgreSQL — one conditional upsert on
// `periodic_task_runs` — so with several worker replicas exactly one of them
// runs a due pass, and a pass that is not due costs one statement.
type Gate struct {
	pool *pgxpool.Pool
}

// NewGate builds a gate over the installation database.
func NewGate(pool *pgxpool.Pool) *Gate { return &Gate{pool: pool} }

// Claim records a run of name and reports true when the previous claimed run
// started at least interval ago, or there was none.
func (gate *Gate) Claim(ctx context.Context, name string, interval time.Duration) (bool, error) {
	if interval <= 0 {
		interval = time.Minute
	}
	tag, err := gate.pool.Exec(ctx, `
INSERT INTO periodic_task_runs (name, last_started_at) VALUES ($1, now())
ON CONFLICT (name) DO UPDATE SET last_started_at = now()
WHERE periodic_task_runs.last_started_at <= now() - make_interval(secs => $2)`,
		name, interval.Seconds())
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// ClaimAt is Claim for a pass that runs at a time of day rather than on an
// interval: it is due once the local clock has passed the slot and no run has
// been claimed since that slot began. slot is minutes past local midnight.
func (gate *Gate) ClaimAt(ctx context.Context, name string, now time.Time, slot int) (bool, error) {
	local := now
	start := time.Date(local.Year(), local.Month(), local.Day(), slot/60, slot%60, 0, 0, local.Location())
	if local.Before(start) {
		return false, nil
	}
	tag, err := gate.pool.Exec(ctx, `
INSERT INTO periodic_task_runs (name, last_started_at) VALUES ($1, $3)
ON CONFLICT (name) DO UPDATE SET last_started_at = EXCLUDED.last_started_at
WHERE periodic_task_runs.last_started_at < $2`,
		name, start.UTC(), now.UTC())
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// LastRun reads when a gated pass last started; the zero time if never.
func (gate *Gate) LastRun(ctx context.Context, name string) (time.Time, error) {
	var last time.Time
	err := gate.pool.QueryRow(ctx,
		`SELECT coalesce(max(last_started_at), 'epoch'::timestamptz) FROM periodic_task_runs WHERE name = $1`,
		name).Scan(&last)
	return last, err
}
