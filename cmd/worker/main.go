package main

import (
	"context"
	"encoding/base64"
	"errors"
	"github.com/omniflow/omniflow/internal/dormancy"
	"github.com/omniflow/omniflow/internal/recurring"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/accountsupport"
	"github.com/omniflow/omniflow/internal/app"
	"github.com/omniflow/omniflow/internal/backup"
	"github.com/omniflow/omniflow/internal/blockedbot"
	"github.com/omniflow/omniflow/internal/bulkrunner"
	"github.com/omniflow/omniflow/internal/campaigns"
	"github.com/omniflow/omniflow/internal/config"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/egress"
	"github.com/omniflow/omniflow/internal/errorlog"
	"github.com/omniflow/omniflow/internal/fulfillment"
	"github.com/omniflow/omniflow/internal/goodsdelivery"
	apihttp "github.com/omniflow/omniflow/internal/httpapi"
	"github.com/omniflow/omniflow/internal/importservice"
	"github.com/omniflow/omniflow/internal/installsettings"
	"github.com/omniflow/omniflow/internal/jobs"
	"github.com/omniflow/omniflow/internal/operator"
	"github.com/omniflow/omniflow/internal/panelpg"
	"github.com/omniflow/omniflow/internal/paymentservice"
	"github.com/omniflow/omniflow/internal/platform"
	"github.com/omniflow/omniflow/internal/remnawave"
	"github.com/omniflow/omniflow/internal/remnawavesync"
	"github.com/omniflow/omniflow/internal/renewal"
	"github.com/omniflow/omniflow/internal/retention"
	"github.com/omniflow/omniflow/internal/sweeper"
	"github.com/omniflow/omniflow/internal/updatecheck"
	"github.com/riverqueue/river"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	errorLog := errorlog.New(slog.NewJSONHandler(os.Stderr, nil), "worker")
	logger := slog.New(errorLog)
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
	errorLog.Attach(ctx, pool)
	settings := installsettings.New(pool, logger)
	remnawaveClient, err := remnawave.NewClient(cfg.RemnawaveURL, cfg.RemnawaveToken, remnawave.OptionsFromEnv(os.Getenv))
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
	naming, err := fulfillment.NamingFromEnv(os.Getenv)
	if err != nil {
		logger.Error("Remnawave naming templates are invalid", "error", err)
		os.Exit(1)
	}
	// The operator alert threshold is the notifications section's, read per
	// run, so an operator who wants to hear sooner or later need not restart.
	fulfillmentWorker := fulfillment.NewWorker(pool, remnawaveClient, metrics).WithAlertAttempts(
		func(ctx context.Context) int {
			return settings.Section(ctx, "notifications").Positive("fulfillmentAlertAttempts", 0)
		})
	fulfillmentWorker.SetNaming(naming)
	river.AddWorker(workers, fulfillmentWorker)
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
		deliveryRegistry.WithFragmentDirect(
			goodsdelivery.DirectOptionsFrom(cfg.Fragment, cfg.DefaultCurrency, logger))
		river.AddWorker(workers, goodsdelivery.NewWorker(pool, deliveryRegistry, logger).WithOptions(
			goodsdelivery.Options{Rates: deliveryRegistry.Rates(), LowWalletStars: cfg.Fragment.WalletLowStars}))
	} else {
		logger.Info("digital goods delivery disabled", "reason", "APP_DATA_ENCRYPTION_KEY is not set")
	}

	tasks, backupService, err := periodicTasks(logger, cfg, pool, core, inserter, deliveryRegistry, settings)
	if err != nil {
		logger.Error("initialize periodic tasks", "error", err)
		os.Exit(1)
	}
	monitoring, err := monitoringTasks(logger, cfg, pool, remnawaveClient, gate(pool))
	if err != nil {
		logger.Error("initialize usage monitoring", "error", err)
		os.Exit(1)
	}
	tasks = append(tasks, monitoring...)
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
		"backups_available", backupService.Enabled(), "database_max_conns", cfg.DatabaseMaxConns,
		"version", version)
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
	inserter *jobs.Client, deliveryRegistry *goodsdelivery.Registry, settings *installsettings.Reader,
) ([]jobs.Task, *backup.Service, error) {
	gate := jobs.NewGate(pool)
	// The operations service unseals blocklist and anomaly-source credentials,
	// so the passes that need it run only when the encryption key is present.
	var operations *panelpg.Service
	if len(cfg.DataEncryptionKey) == 32 {
		var err error
		if operations, err = panelpg.New(pool, cfg.DataEncryptionKey, panelpg.Options{}); err != nil {
			return nil, nil, err
		}
	}
	dunningSchedule, err := recurring.ParseSchedule(
		os.Getenv("APP_RENEWAL_RETRY_HOURS"), os.Getenv("APP_RENEWAL_NOTIFY_FROM_ATTEMPT"),
	)
	if err != nil {
		return nil, nil, err
	}
	renewals := renewal.New(
		pool, core.Store, core.Payments, paymentservice.Index(core.Providers), logger, renewal.Config{
			LowBalanceWarning: lowBalanceWarning(), Schedule: dunningSchedule,
		},
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
	// The worker can take a backup whenever it holds the key; whether the
	// schedule runs is the backup section's switch, falling back to
	// APP_BACKUP_ENABLED, and is decided on every tick below.
	backupService := backup.New(pool, logger, backup.Config{
		Enabled: len(cfg.Backup.EncryptionKey) == 32, Directory: cfg.Backup.Directory, Interval: cfg.Backup.Interval,
		Retention: cfg.Backup.Retention, EncryptionKey: cfg.Backup.EncryptionKey,
		PgDumpPath: cfg.Backup.PgDumpPath, PgRestorePath: cfg.Backup.PgRestorePath,
		DatabaseURL: cfg.DatabaseURL,
	})

	tasks := []jobs.Task{
		{Name: "commerce_maintenance", Interval: time.Minute, Run: core.Store.MaintenancePass},
		{Name: "payment_reconcile", Interval: time.Minute, Run: core.Payments.ReconcilePass},
		{Name: "renewal", Interval: time.Minute, Timeout: 5 * time.Minute, Run: pass(renewals.RunOnce)},
		// The reconcile cadence is the Remnawave section's, read on a one-minute
		// tick, so changing it in the panel applies within a minute.
		{Name: "fulfillment_reconcile", Interval: time.Minute, Timeout: 10 * time.Minute, Run: func(ctx context.Context) error {
			interval := settings.Section(ctx, "remnawave").Duration(
				"reconcileIntervalMinutes", time.Minute, fulfillment.ReconcileInterval)
			due, err := gate.Claim(ctx, "fulfillment_reconcile", interval)
			if err != nil || !due {
				return err
			}
			return scheduler.Schedule(ctx)
		}},
		{Name: "fulfillment_revive", Interval: fulfillment.ReviveInterval, Run: scheduler.Revive},
		{Name: "lifecycle_sweep", Interval: lifecycle.Interval(), Run: pass(lifecycle.SweepLifecycle)},
		{Name: "anomaly_evaluation", Interval: lifecycle.AnomalyInterval(), Run: pass(lifecycle.EvaluateAnomalies)},
		{Name: "campaign_expansion", Interval: campaignInterval, Timeout: 5 * time.Minute, Run: pass(campaignRunner.RunOnce)},
		{Name: "bulk_actions", Interval: bulkInterval, Timeout: 5 * time.Minute, Run: pass(bulk.RunOnce)},
		// The named API tokens' request logs keep ninety days too.
		{Name: "api_token_log_prune", Interval: 6 * time.Hour, Run: func(ctx context.Context) error {
			_, err := dbgen.New(pool).PruneAPITokenRequests(ctx)
			return err
		}},
		// The customer action log keeps ninety days.
		{Name: "customer_activity_prune", Interval: 6 * time.Hour, Run: func(ctx context.Context) error {
			_, err := dbgen.New(pool).PruneCustomerActivity(ctx)
			return err
		}},
		{Name: "retention", Interval: cfg.Retention.Interval, Run: func(ctx context.Context) error {
			retentionWorker.Sweep(ctx)
			return ctx.Err()
		}},
	}
	// Customers who blocked the bot lose their subscription while they stay
	// blocked, when the operator switched that on in the Marketing settings.
	if core.Fulfillment != nil {
		blocked := blockedbot.New(pool, blockedbot.FulfillmentEnqueuer{Service: core.Fulfillment}, logger)
		tasks = append(tasks, jobs.Task{Name: "blocked_bot_subscriptions", Interval: 15 * time.Minute, Run: blocked.Run})
	}
	if operations != nil {
		// Automatic loyalty placement by spend, and the bulk recalculation an
		// operator or a newly published programme asks for.
		tasks = append(tasks, jobs.Task{
			Name: "loyalty_evaluation", Interval: loyaltyInterval, Timeout: 10 * time.Minute,
			Run: func(ctx context.Context) error { return operations.LoyaltySweep(ctx, time.Now().UTC()) },
		})
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
	if operations != nil {
		// Outgoing webhooks: outbox events fanned out to subscribed endpoints
		// and sent signed, through the egress policy every outbound call uses.
		webhookClient := egress.Policy{}.Client(10 * time.Second)
		tasks = append(tasks, jobs.Task{Name: "webhooks", Interval: 15 * time.Second, Run: func(ctx context.Context) error {
			return operations.DispatchWebhooks(ctx, webhookClient)
		}})
	}
	// Customers quiet for longer than the owner allows are marked dormant,
	// and deleted only if the owner asked for that too. The check is cheap
	// and runs hourly; a real pass is claimed at most once a day.
	dormantPass := dormancy.New(pool, settings, logger)
	dormantClaims := gate
	tasks = append(tasks, jobs.Task{Name: "dormant_customers", Interval: time.Hour, Timeout: 30 * time.Minute,
		Run: func(ctx context.Context) error {
			if !dormancy.SettingsFrom(settings.Section(ctx, dormancy.Section)).Enabled {
				return nil
			}
			due, claimErr := dormantClaims.Claim(ctx, "dormant_customers", 24*time.Hour)
			if claimErr != nil || !due {
				return claimErr
			}
			return dormantPass.Run(ctx)
		}})
	reports := operator.NewReports(pool, settings, logger)
	tasks = append(tasks,
		jobs.Task{Name: "operator_reports", Interval: 5 * time.Minute, Run: reports.RunReports},
		jobs.Task{Name: "support_sla", Interval: time.Minute, Run: reports.RunSupportSLA},
	)
	if cfg.UpdateFeedURL != "" {
		checker := updatecheck.New(updatecheck.Options{FeedURL: cfg.UpdateFeedURL, Current: version})
		tasks = append(tasks, jobs.Task{Name: "release_check", Interval: updatecheck.DefaultInterval, Run: func(ctx context.Context) error {
			if status := checker.Status(ctx); status.UpdateAvailable && status.Latest != "" {
				return operator.AnnounceRelease(ctx, pool, version, status.Latest)
			}
			return nil
		}})
	}
	growth, err := growthTasks(logger, cfg, pool)
	if err != nil {
		return nil, nil, err
	}
	tasks = append(tasks, growth...)
	if backupService.Enabled() {
		// A dump takes as long as the database is large, so the pass has no
		// timeout. With several worker replicas the backup lands on whichever
		// one runs it, which is why the backup directory must be shared. The
		// schedule — on or off, every N hours or at a time of day, and how long
		// a backup is kept — is the backup section's, read on every tick.
		scheduled := scheduledBackup{
			service: backupService, settings: settings, gate: gate, logger: logger,
			fallback: cfg.Backup, telegram: cfg.TelegramToken, transport: cfg.TelegramTransport,
		}
		tasks = append(tasks, jobs.Task{Name: "backup", Interval: 5 * time.Minute, Timeout: -1, Run: scheduled.Run})
		// Backups, restores, and deletions an operator asked for in the panel.
		tasks = append(tasks, jobs.Task{Name: "backup_requests", Interval: 15 * time.Second, Timeout: -1,
			Run: func(ctx context.Context) error {
				for range 5 {
					done, err := backupService.ProcessRequest(ctx)
					if err != nil {
						logger.Warn("backup request failed", "error", err)
					}
					if !done {
						return nil
					}
				}
				return nil
			}})
	} else {
		logger.Info("backups are unavailable", "reason", "APP_BACKUP_ENCRYPTION_KEY is not set")
	}
	return tasks, backupService, nil
}

// Intervals of the passes whose packages take theirs from configuration.
const (
	campaignInterval = time.Minute
	bulkInterval     = 15 * time.Second
	// loyaltyInterval is how soon a payment can move a customer to a new tier.
	loyaltyInterval = 5 * time.Minute
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

func gate(pool *pgxpool.Pool) *jobs.Gate { return jobs.NewGate(pool) }

// monitoringTasks are the read-only usage passes over Remnawave: unusual
// traffic growth and devices over their limit. They report to operators and
// change nothing in the panel.
func monitoringTasks(
	logger *slog.Logger, cfg config.WorkerConfig, pool *pgxpool.Pool,
	panel *remnawave.Client, claims *jobs.Gate,
) ([]jobs.Task, error) {
	settings, err := remnawavesync.MonitorSettingsFromEnv(os.Getenv)
	if err != nil {
		return nil, err
	}
	var store remnawavesync.SnapshotStore
	if cfg.ValkeyURL != "" {
		client, clientErr := platform.NewValkeyClient(cfg.ValkeyURL)
		if clientErr != nil {
			return nil, clientErr
		}
		store = remnawavesync.ValkeySnapshots{Client: client}
	} else if settings.FastInterval > 0 || settings.DailyThresholdBytes > 0 {
		logger.Warn("traffic monitoring is off: it keeps its readings in Valkey and APP_VALKEY_URL is not set")
	}
	monitor := remnawavesync.NewMonitor(pool, panel, store, settings, logger)
	var tasks []jobs.Task
	// The scheduled import turns users created directly in the panel into
	// customers, through the same staging and conflict rules as a manual one.
	if raw := strings.TrimSpace(os.Getenv("APP_REMNAWAVE_AUTO_IMPORT_HOURS")); raw != "" {
		hours, parseErr := strconv.Atoi(raw)
		if parseErr != nil || hours < 1 || hours > 24*7 {
			return nil, errors.New("APP_REMNAWAVE_AUTO_IMPORT_HOURS must be 1 to 168")
		}
		importer := importservice.New(pool, panel)
		tasks = append(tasks, jobs.Task{Name: "remnawave_auto_import", Interval: time.Hour, Timeout: 50 * time.Minute,
			Run: func(ctx context.Context) error {
				due, claimErr := claims.Claim(ctx, "remnawave_auto_import", time.Duration(hours)*time.Hour)
				if claimErr != nil || !due {
					return claimErr
				}
				run, runErr := importer.RunAll(ctx)
				if runErr == nil {
					logger.Info("scheduled Remnawave import finished", "status", run.Status,
						"total", run.TotalCount, "valid", run.ValidCount, "conflicts", run.ConflictCount)
				}
				return runErr
			}})
	}
	if settings.FastInterval > 0 && store != nil {
		tasks = append(tasks, jobs.Task{Name: "traffic_monitor_fast", Interval: settings.FastInterval, Timeout: 10 * time.Minute,
			Run: func(ctx context.Context) error {
				return monitor.TrafficPass(ctx, "fast", settings.FastThresholdBytes, 3*settings.FastInterval)
			}})
	}
	if settings.DailyThresholdBytes > 0 && store != nil {
		tasks = append(tasks, jobs.Task{Name: "traffic_monitor_daily", Interval: time.Hour, Timeout: 30 * time.Minute,
			Run: func(ctx context.Context) error {
				due, claimErr := claims.Claim(ctx, "traffic_monitor_daily", 24*time.Hour)
				if claimErr != nil || !due {
					return claimErr
				}
				return monitor.TrafficPass(ctx, "daily", settings.DailyThresholdBytes, 49*time.Hour)
			}})
	}
	if settings.DeviceReport {
		tasks = append(tasks, jobs.Task{Name: "device_overflow_report", Interval: time.Hour, Timeout: 30 * time.Minute,
			Run: func(ctx context.Context) error {
				due, claimErr := claims.Claim(ctx, "device_overflow_report", 24*time.Hour)
				if claimErr != nil || !due {
					return claimErr
				}
				return monitor.DeviceOverflowPass(ctx, time.Now())
			}})
	}
	return tasks, nil
}

// lowBalanceWarning reads APP_RENEWAL_LOW_BALANCE_WARNING_HOURS: how long
// before a wallet-funded renewal the customer is told the balance falls short.
// Unset is the default of 48 hours; 0 turns the warning off.
func lowBalanceWarning() time.Duration {
	raw := strings.TrimSpace(os.Getenv("APP_RENEWAL_LOW_BALANCE_WARNING_HOURS"))
	if raw == "" {
		return 0
	}
	hours, err := strconv.Atoi(raw)
	if err != nil || hours < 0 {
		return 0
	}
	if hours == 0 {
		return -1
	}
	return time.Duration(hours) * time.Hour
}
