// Package sweeper runs the periodic lifecycle passes that nothing else
// triggers: expiring gifts and personal offers, refreshing external blocklists,
// and evaluating anomaly rules.
//
// These are deliberately not River jobs. Each is a whole-table pass that is
// safe to repeat and carries no per-record argument, so a durable queue would
// add a row and a retry policy to something a ticker already expresses. What
// they do share with River jobs is that every pass is idempotent: a sweep that
// runs twice reaches the same state as one that ran once.
//
// A failing pass never stops the others. A blocklist source whose publisher is
// down must not prevent gifts from expiring, and an anomaly rule that cannot be
// evaluated must not stop a list refreshing.
package sweeper

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/blocklist"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/panelpg"
)

// maxBlocklistBody bounds one source download.
//
// A publisher that streams without end would otherwise hold the sweeper open
// and grow its memory until the process dies.
const maxBlocklistBody = 32 << 20

// Config tunes the sweep cadence.
type Config struct {
	// Interval is how often the whole set runs. Each pass is cheap and
	// idempotent, so this is about how quickly an expiry is noticed rather
	// than about load.
	Interval time.Duration
	// AnomalyInterval throttles anomaly evaluation independently, because it
	// aggregates over the orders table and there is no value in running it more
	// often than the shortest configured rule window.
	AnomalyInterval time.Duration
}

// Sweeper runs the periodic passes.
type Sweeper struct {
	pool       *pgxpool.Pool
	operations *panelpg.Service
	logger     *slog.Logger
	http       *http.Client
	config     Config
	clock      func() time.Time

	lastAnomalyRun time.Time
}

// New builds the sweeper. A nil operations service disables the passes that
// need it, which is what an installation with no panel gets.
func New(pool *pgxpool.Pool, operations *panelpg.Service, logger *slog.Logger, config Config) *Sweeper {
	if config.Interval <= 0 {
		config.Interval = 5 * time.Minute
	}
	if config.AnomalyInterval <= 0 {
		config.AnomalyInterval = 15 * time.Minute
	}
	return &Sweeper{
		pool: pool, operations: operations, logger: logger, config: config,
		// Bounded, because a blocklist publisher that never answers must not
		// hold a sweep open until the next tick.
		http:  &http.Client{Timeout: 60 * time.Second},
		clock: time.Now,
	}
}

// Run sweeps until the context is cancelled.
func (sweeper *Sweeper) Run(ctx context.Context) {
	ticker := time.NewTicker(sweeper.config.Interval)
	defer ticker.Stop()
	for {
		sweeper.Sweep(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Sweep runs one pass of each lifecycle sweep.
//
// Errors are logged and swallowed on purpose: this is a background pass with
// no caller to report to, and one failing source must not stop the rest.
func (sweeper *Sweeper) Sweep(ctx context.Context) {
	sweeper.expireGifts(ctx)
	sweeper.expireOffers(ctx)
	sweeper.refreshBlocklists(ctx)
	sweeper.evaluateAnomalies(ctx)
}

// expireGifts closes gifts whose claim window has passed.
//
// A gift is refused after its expiry whether or not this has run — the claim
// statement checks the instant — so this only makes the state visible to the
// operator register and releases the sender's refund path.
func (sweeper *Sweeper) expireGifts(ctx context.Context) {
	expired, err := dbgen.New(sweeper.pool).ExpireGifts(ctx)
	if err != nil {
		sweeper.logger.Warn("gift expiry sweep failed", "error", err)
		return
	}
	if len(expired) > 0 {
		sweeper.logger.Info("gifts expired", "count", len(expired))
	}
}

// expireOffers closes personal offers whose window has passed.
func (sweeper *Sweeper) expireOffers(ctx context.Context) {
	count, err := dbgen.New(sweeper.pool).ExpirePersonalOffers(ctx)
	if err != nil {
		sweeper.logger.Warn("offer expiry sweep failed", "error", err)
		return
	}
	if count > 0 {
		sweeper.logger.Info("personal offers expired", "count", count)
	}
}

// refreshBlocklists re-downloads every source whose interval has elapsed.
//
// Each source is independent: one publisher being down leaves the others
// refreshed, and leaves the failing source's previous entries intact rather
// than emptying a list nobody could replace.
func (sweeper *Sweeper) refreshBlocklists(ctx context.Context) {
	if sweeper.operations == nil {
		return
	}
	sources, err := sweeper.operations.DueBlocklistSources(ctx, 20)
	if err != nil {
		sweeper.logger.Warn("blocklist sweep failed", "error", err)
		return
	}
	for _, source := range sources {
		if err := sweeper.refreshSource(ctx, source); err != nil {
			sweeper.logger.Warn("blocklist source refresh failed",
				"source", source.Slug, "error", err)
		}
	}
}

func (sweeper *Sweeper) refreshSource(ctx context.Context, source panelpg.BlocklistSource) error {
	// A failure is recorded against the source whatever caused it, so the panel
	// shows an unhealthy list rather than one that silently stopped updating.
	record := func(status, code string, count int32) {
		if err := sweeper.operations.RecordBlocklistRefresh(ctx, source.ID, status, code, count); err != nil {
			sweeper.logger.Warn("recording blocklist refresh failed", "source", source.Slug, "error", err)
		}
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source.URL, nil)
	if err != nil {
		record("failing", "invalid_url", source.EntryCount)
		return err
	}
	if source.AuthConfigured {
		header, credentialErr := sweeper.operations.BlocklistSourceCredential(ctx, source.ID)
		if credentialErr != nil {
			record("failing", "credential_unreadable", source.EntryCount)
			return credentialErr
		}
		if header != "" {
			request.Header.Set("Authorization", header)
		}
	}

	response, err := sweeper.http.Do(request)
	if err != nil {
		record("failing", "unreachable", source.EntryCount)
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		record("failing", "http_error", source.EntryCount)
		return errors.New("blocklist source returned an error status")
	}

	entries, skipped, err := blocklist.Parse(source.SubjectKind, io.LimitReader(response.Body, maxBlocklistBody))
	if err != nil {
		record("failing", "unparsable", source.EntryCount)
		return err
	}
	// The replace is one transaction, so an entry the publisher removed stops
	// matching in the same instant the new set starts.
	if err := sweeper.operations.ReplaceBlocklistEntries(ctx, source.ID, entries); err != nil {
		record("failing", "store_failed", source.EntryCount)
		return err
	}

	record("healthy", "", int32(len(entries)))
	sweeper.logger.Info("blocklist source refreshed",
		"source", source.Slug, "entries", len(entries), "skipped", skipped)
	return nil
}

// evaluateAnomalies runs every enabled rule and records what it finds.
//
// It is throttled separately because it aggregates over the orders table. A
// signal that persists across runs is deduplicated by window, so running more
// often produces no extra alerts — only extra work.
func (sweeper *Sweeper) evaluateAnomalies(ctx context.Context) {
	if sweeper.operations == nil {
		return
	}
	now := sweeper.clock()
	if !sweeper.lastAnomalyRun.IsZero() && now.Sub(sweeper.lastAnomalyRun) < sweeper.config.AnomalyInterval {
		return
	}
	sweeper.lastAnomalyRun = now

	signals, err := sweeper.operations.EvaluateAnomalies(ctx)
	if err != nil {
		sweeper.logger.Warn("anomaly evaluation failed", "error", err)
		return
	}
	if len(signals) > 0 {
		// The count only. A subject identifier here would put a customer
		// identifier in the operator's log.
		sweeper.logger.Info("anomaly signals raised", "count", len(signals))
	}
}
