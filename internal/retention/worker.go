// Package retention deletes disposable operational data once its documented
// window elapses. It never touches financial, audit, consent, identity, or
// entitlement history, which is append-only for the life of the installation.
package retention

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// Config bounds each retention window.
type Config struct {
	Outbox    time.Duration
	Telemetry time.Duration
	Drift     time.Duration
	Interval  time.Duration
}

// AttachmentSweeper deletes expired attachment rows and reclaims the files
// behind them.
//
// It exists because the two halves cannot be separated safely. Attachment files
// are named by their content digest, so one file may back several rows, and only
// the code that deletes the rows can tell which files nothing references any
// more. A retention step that deleted rows alone — which is what ran until the
// web panel could accept an upload — leaves the disk growing without bound.
type AttachmentSweeper interface {
	SweepExpiredAttachments(ctx context.Context) (rows int64, files int64, err error)
}

// Worker runs the cleanup sweep on a fixed interval.
type Worker struct {
	pool        *pgxpool.Pool
	logger      *slog.Logger
	config      Config
	attachments AttachmentSweeper
}

func New(pool *pgxpool.Pool, logger *slog.Logger, config Config) *Worker {
	if config.Interval <= 0 {
		config.Interval = time.Hour
	}
	return &Worker{pool: pool, logger: logger, config: config}
}

// WithAttachments supplies the sweeper that also reclaims uploaded files.
//
// Without one the worker still deletes the rows, which is what a Telegram-only
// installation needs: those attachments are references to bytes Telegram holds,
// and there is nothing local to reclaim.
func (worker *Worker) WithAttachments(sweeper AttachmentSweeper) *Worker {
	worker.attachments = sweeper
	return worker
}

// Run sweeps until the context is cancelled.
func (worker *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(worker.config.Interval)
	defer ticker.Stop()
	for {
		worker.Sweep(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Result reports how many rows each window removed.
type Result struct {
	BotSessions      int64
	CheckoutSessions int64
	WebhookEvents    int64
	Attachments      int64
	OutboxEvents     int64
	TelemetryEvents  int64
	ResolvedDrifts   int64
}

// Sweep runs one cleanup pass. Every step is independent: one failure is logged
// and the rest still run, so a single locked table cannot stall retention.
func (worker *Worker) Sweep(ctx context.Context) Result {
	queries := dbgen.New(worker.pool)
	result := Result{}
	steps := []struct {
		name string
		run  func() (int64, error)
	}{
		{"bot_sessions", func() (int64, error) { return queries.DeleteExpiredBotSessions(ctx) }},
		{"bot_checkout_sessions", func() (int64, error) { return queries.DeleteExpiredCheckoutSessions(ctx) }},
		{"provider_webhook_events", func() (int64, error) { return queries.DeleteExpiredWebhookEvents(ctx) }},
		{"support_attachments", func() (int64, error) {
			if worker.attachments == nil {
				return queries.DeleteExpiredSupportAttachments(ctx)
			}
			rows, files, err := worker.attachments.SweepExpiredAttachments(ctx)
			if err != nil {
				return 0, err
			}
			if files > 0 {
				worker.logger.Info("retention sweep reclaimed attachment files", "files", files)
			}
			return rows, nil
		}},
		{"outbox_events", func() (int64, error) {
			return queries.DeletePublishedOutboxEvents(ctx, worker.config.Outbox.Seconds())
		}},
		{"telemetry_events", func() (int64, error) {
			return queries.DeleteOldTelemetryEvents(ctx, worker.config.Telemetry.Seconds())
		}},
		{"entitlement_drifts", func() (int64, error) {
			return queries.DeleteResolvedDrifts(ctx, worker.config.Drift.Seconds())
		}},
	}
	targets := []*int64{
		&result.BotSessions, &result.CheckoutSessions, &result.WebhookEvents, &result.Attachments,
		&result.OutboxEvents, &result.TelemetryEvents, &result.ResolvedDrifts,
	}
	for index, step := range steps {
		removed, err := step.run()
		if err != nil {
			worker.logger.Warn("retention sweep step failed", "table", step.name, "error", err)
			continue
		}
		*targets[index] = removed
		if removed > 0 {
			worker.logger.Info("retention sweep removed rows", "table", step.name, "rows", removed)
		}
	}
	return result
}
