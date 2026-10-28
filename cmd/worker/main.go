package main

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/accountsupport"
	"github.com/omniflow/omniflow/internal/app"
	"github.com/omniflow/omniflow/internal/backup"
	"github.com/omniflow/omniflow/internal/bulkrunner"
	"github.com/omniflow/omniflow/internal/campaigns"
	"github.com/omniflow/omniflow/internal/config"
	"github.com/omniflow/omniflow/internal/fulfillment"
	"github.com/omniflow/omniflow/internal/goodsdelivery"
	apihttp "github.com/omniflow/omniflow/internal/httpapi"
	"github.com/omniflow/omniflow/internal/jobs"
	"github.com/omniflow/omniflow/internal/panelpg"
	"github.com/omniflow/omniflow/internal/paymentservice"
	"github.com/omniflow/omniflow/internal/platform"
	"github.com/omniflow/omniflow/internal/remnawave"
	"github.com/omniflow/omniflow/internal/renewal"
	"github.com/omniflow/omniflow/internal/retention"
	"github.com/omniflow/omniflow/internal/sweeper"
	"github.com/riverqueue/river"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	// Disaster recovery must not depend on the rest of the stack being able to
	// start, so decryption is available as a plain filter over stdin.
	if len(os.Args) > 1 && os.Args[1] == "--decrypt-backup" {
		if err := decryptBackup(); err != nil {
			logger.Error("backup decryption failed", "error", err)
			os.Exit(1)
		}
		return
	}
	cfg, err := config.LoadWorker()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	pool, err := app.OpenWorkerPool(ctx, cfg.DatabaseURL, cfg.DatabaseMaxConns)
	if err != nil {
		logger.Error("connect database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()
	remnawaveClient, err := remnawave.NewClient(cfg.RemnawaveURL, cfg.RemnawaveToken)
	if err != nil {
		logger.Error("initialize Remnawave", "error", err)
		os.Exit(1)
	}
	var metrics *platform.Metrics
	if cfg.MetricsEnabled {
		metrics = platform.NewMetrics("worker")
	}

	// Jobs that the worker's own passes create — a renewal settling, a saved
	// cart swept, a reconcile scheduled — are inserted through an insert-only
	// client in the same transaction as the change, exactly as in the api and
	// the bot. The running client below is built after the passes it runs.
	inserter, err := jobs.NewInsertOnlyClient(pool)
	if err != nil {
		logger.Error("initialize River", "error", err)
		os.Exit(1)
	}
	// Automatic renewals charge the provider the customer already paid with, so
	// the worker builds the same adapter set the api does — through the same
	// constructor, which is what keeps the two from drifting again.
	core, err := app.NewCommerceCore(cfg.Commerce(), pool, inserter, app.CommerceOptions{
		Logger: logger, Metrics: metrics,
	})
	if err != nil {
		logger.Error("initialize commerce", "error", err)
		os.Exit(1)
	}

	workers := river.NewWorkers()
	river.AddWorker(workers, fulfillment.NewWorker(pool, remnawaveClient, metrics))
	// The digital-goods shop is optional. Without an encryption key there is no
	// way to unseal a provider credential, so the worker starts without it
	// rather than refusing to start at all — an installation that sells no
	// digital goods never needs one.
	var deliveryRegistry *goodsdelivery.Registry
	if len(cfg.DataEncryptionKey) == 32 {
		deliveryRegistry, err = goodsdelivery.NewRegistry(pool, cfg.DataEncryptionKey, cfg.DefaultCurrency)
		if err != nil {
			logger.Error("initialize digital goods", "error", err)
			os.Exit(1)
		}
		river.AddWorker(workers, goodsdelivery.NewWorker(pool, deliveryRegistry, logger))
	} else {
		logger.Info("digital goods delivery disabled", "reason", "APP_DATA_ENCRYPTION_KEY is not set")
	}

	tasks, backupService, err := periodicTasks(logger, cfg, pool, core, inserter, deliveryRegistry)
	if err != nil {
		logger.Error("initialize periodic tasks", "error", err)
		os.Exit(1)
	}
	periodicWorker, schedule, err := jobs.NewPeriodic(logger, tasks...)
	if err != nil {
		logger.Error("initialize periodic tasks", "error", err)
		os.Exit(1)
	}
	river.AddWorker(workers, periodicWorker)

	client, err := jobs.NewWorkerClient(pool, workers, schedule)
	if err != nil {
		logger.Error("initialize River", "error", err)
		os.Exit(1)
	}
	if err = client.Start(ctx); err != nil {
		logger.Error("start River", "error", err)
		os.Exit(1)
	}

	metricsServer := startOperationalServer(logger, cfg, pool, metrics)

	logger.Info("worker started", "queue_backend", "river", "periodic_tasks", len(tasks),
		"backups_enabled", backupService.Enabled(), "database_max_conns", cfg.DatabaseMaxConns)
	<-ctx.Done()
	stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if metricsServer != nil {
		if err := metricsServer.Shutdown(stopCtx); err != nil {
			logger.Warn("worker operational server shutdown failed", "error", err)
		}
	}
	// River drains in-flight jobs before returning, so a shutdown never
	// abandons a fulfillment run mid-flight. A pass still running when the
	// budget is spent — a long pg_dump — has its context cancelled rather than
	// being cut off by the exit, so it records itself as cancelled.
	if err := client.Stop(stopCtx); err != nil {
		logger.Warn("River did not drain in time; cancelling running jobs", "error", err)
		cancelCtx, cancelJobs := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelJobs()
		if err = client.StopAndCancel(cancelCtx); err != nil {
			logger.Error("stop River", "error", err)
		}
	}
	logger.Info("worker stopped")
}

// periodicTasks lists every whole-table pass the installation runs.
//
// Each one is a River periodic job: River's elected leader enqueues it, and it
// stays unique while queued or running, so it runs once per interval across
// the installation however many worker replicas are up, and a slow pass is
// never doubled by the next tick. They used to be ticker goroutines, which ran
// once per process — including the cart sweep, which debits wallets, in every
// api replica.
func periodicTasks(
	logger *slog.Logger, cfg config.WorkerConfig, pool *pgxpool.Pool, core *app.CommerceCore,
	inserter *jobs.Client, deliveryRegistry *goodsdelivery.Registry,
) ([]jobs.Task, *backup.Service, error) {
	// The operations service unseals blocklist and anomaly-source credentials,
	// so the passes that need it run only when the encryption key is present.
	var operations *panelpg.Service
	if len(cfg.DataEncryptionKey) == 32 {
		var err error
		if operations, err = panelpg.New(pool, cfg.DataEncryptionKey, panelpg.Options{}); err != nil {
			return nil, nil, err
		}
	}
	renewals := renewal.New(
		pool, core.Store, core.Payments, paymentservice.Index(core.Providers), logger, renewal.Config{},
	)
	scheduler := fulfillment.NewScheduler(pool, inserter)
	lifecycle := sweeper.New(pool, operations, logger, sweeper.Config{})
	// Campaign expansion and scheduled publication. Delivery itself stays with
	// the bot's notifier, which already owns consent, quiet hours, and Telegram
	// health.
	campaignRunner := campaigns.New(pool, logger, campaigns.Config{Interval: campaignInterval})
	// Bulk actions run here rather than in the api, because a batch of four
	// hundred subscriptions must not be tied to the lifetime of the request that
	// started it.
	bulk := bulkrunner.New(
		pool, operations, core.Fulfillment, core.Store, logger, bulkrunner.Config{Interval: bulkInterval},
	)
	retentionWorker := retention.New(pool, logger, retention.Config{
		Outbox: cfg.Retention.Outbox, Telemetry: cfg.Retention.Telemetry,
		Drift: cfg.Retention.Drift, Interval: cfg.Retention.Interval,
	})
	// Retention deletes the attachment row; only the support service knows which
	// file behind it nothing references any more. Attachment files are named by
	// their content digest, so one file can back several rows, and deleting bytes
	// on the strength of a single expired row would break another customer's
	// download.
	if attachments, err := accountsupport.New(pool, accountsupport.Options{
		Logger: logger, AttachmentDirectory: cfg.SupportAttachmentDir,
	}); err != nil {
		logger.Warn("attachment files will not be reclaimed", "error", err)
	} else {
		retentionWorker = retentionWorker.WithAttachments(attachments)
	}
	backupService := backup.New(pool, logger, backup.Config{
		Enabled: cfg.Backup.Enabled, Directory: cfg.Backup.Directory, Interval: cfg.Backup.Interval,
		Retention: cfg.Backup.Retention, EncryptionKey: cfg.Backup.EncryptionKey,
		PgDumpPath: cfg.Backup.PgDumpPath, PgRestorePath: cfg.Backup.PgRestorePath,
		DatabaseURL: cfg.DatabaseURL,
	})

	tasks := []jobs.Task{
		{Name: "commerce_maintenance", Interval: time.Minute, Run: core.Store.MaintenancePass},
		{Name: "payment_reconcile", Interval: time.Minute, Run: core.Payments.ReconcilePass},
		{Name: "renewal", Interval: time.Minute, Timeout: 5 * time.Minute, Run: pass(renewals.RunOnce)},
		{Name: "fulfillment_reconcile", Interval: fulfillment.ReconcileInterval, Run: scheduler.Schedule},
		{Name: "fulfillment_revive", Interval: fulfillment.ReviveInterval, Run: scheduler.Revive},
		{Name: "lifecycle_sweep", Interval: lifecycle.Interval(), Run: pass(lifecycle.SweepLifecycle)},
		{Name: "anomaly_evaluation", Interval: lifecycle.AnomalyInterval(), Run: pass(lifecycle.EvaluateAnomalies)},
		{Name: "campaign_expansion", Interval: campaignInterval, Timeout: 5 * time.Minute, Run: pass(campaignRunner.RunOnce)},
		{Name: "bulk_actions", Interval: bulkInterval, Timeout: 5 * time.Minute, Run: pass(bulk.RunOnce)},
		{Name: "retention", Interval: cfg.Retention.Interval, Run: func(ctx context.Context) error {
			retentionWorker.Sweep(ctx)
			return ctx.Err()
		}},
	}
	if deliveryRegistry != nil {
		// `goods_deliveries` is the source of truth about what is owed, not the
		// job queue: the pass only translates "this row is due" into "run the
		// worker", so a lost job costs a delay rather than a delivery.
		goods := goodsdelivery.NewScheduler(pool, inserter, logger)
		tasks = append(tasks, jobs.Task{
			Name: "goods_delivery_schedule", Interval: goodsdelivery.ScanInterval, Run: goods.Schedule,
		})
	}
	if backupService.Enabled() {
		// A dump takes as long as the database is large, so the pass has no
		// timeout. With several worker replicas the backup lands on whichever
		// one runs it, which is why the backup directory must be shared.
		tasks = append(tasks, jobs.Task{
			Name: "backup", Interval: cfg.Backup.Interval, Timeout: -1,
			Run: func(ctx context.Context) error {
				if _, err := backupService.Create(ctx, "scheduled", "scheduler"); err != nil && !errors.Is(err, backup.ErrBusy) {
					return err
				}
				return backupService.Prune(ctx)
			},
		})
	} else {
		logger.Info("scheduled backups are disabled")
	}
	return tasks, backupService, nil
}

// Intervals of the passes whose packages take theirs from configuration.
const (
	campaignInterval = time.Minute
	bulkInterval     = 15 * time.Second
)

// pass adapts a pass that logs its own failures to the task signature, so a
// cancelled run is still reported as one.
func pass(run func(context.Context)) func(context.Context) error {
	return func(ctx context.Context) error {
		run(ctx)
		return ctx.Err()
	}
}

// decryptBackup streams an encrypted backup from stdin to plain pg_dump output
// on stdout using APP_BACKUP_ENCRYPTION_KEY. It exists so a restore is possible
// with nothing but this binary, the key, and the file.
func decryptBackup() error {
	encoded := os.Getenv("APP_BACKUP_ENCRYPTION_KEY")
	if encoded == "" {
		return errors.New("APP_BACKUP_ENCRYPTION_KEY is required to decrypt a backup")
	}
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(key) != 32 {
		return errors.New("APP_BACKUP_ENCRYPTION_KEY must be base64-encoded 32 bytes")
	}
	return backup.Decrypt(os.Stdout, os.Stdin, key)
}

// startOperationalServer exposes the worker's own liveness, readiness, and
// metrics endpoints. It is what makes a worker outage visible to an operator
// without giving the worker a public API surface.
func startOperationalServer(logger *slog.Logger, cfg config.WorkerConfig, pool *pgxpool.Pool, metrics *platform.Metrics) *http.Server {
	if cfg.MetricsAddr == "" {
		return nil
	}
	health := platform.NewHealth(2*time.Second, 3*time.Second)
	health.Register("postgres", func(probeCtx context.Context) error { return pool.Ping(probeCtx) })
	server := &http.Server{
		Addr:              cfg.MetricsAddr,
		Handler:           apihttp.NewRouter(logger, apihttp.RouterOptions{Health: health, Metrics: metrics, Version: version}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
	go func() {
		logger.Info("worker operational endpoints listening", "address", cfg.MetricsAddr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("worker operational server stopped unexpectedly", "error", err)
		}
	}()
	return server
}
