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
	"github.com/omniflow/omniflow/internal/backup"
	"github.com/omniflow/omniflow/internal/config"
	"github.com/omniflow/omniflow/internal/fulfillment"
	"github.com/omniflow/omniflow/internal/goodsdelivery"
	apihttp "github.com/omniflow/omniflow/internal/httpapi"
	"github.com/omniflow/omniflow/internal/jobs"
	"github.com/omniflow/omniflow/internal/panelpg"
	"github.com/omniflow/omniflow/internal/platform"
	"github.com/omniflow/omniflow/internal/remnawave"
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
	pool, err := platform.TracedPool(ctx, cfg.DatabaseURL)
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
	workers := river.NewWorkers()
	river.AddWorker(workers, fulfillment.NewWorker(pool, remnawaveClient, metrics))

	// The digital-goods shop is optional. Without an encryption key there is no
	// way to unseal a provider credential, so the worker starts without it
	// rather than refusing to start at all — an installation that sells no
	// digital goods never needs one.
	if len(cfg.DataEncryptionKey) == 32 {
		registry, registryErr := goodsdelivery.NewRegistry(pool, cfg.DataEncryptionKey, cfg.DefaultCurrency)
		if registryErr != nil {
			logger.Error("initialize digital goods", "error", registryErr)
			os.Exit(1)
		}
		river.AddWorker(workers, goodsdelivery.NewWorker(pool, registry, logger))
	} else {
		logger.Info("digital goods delivery disabled", "reason", "APP_DATA_ENCRYPTION_KEY is not set")
	}

	client, err := jobs.NewClientWithWorkers(pool, workers)
	if err != nil {
		logger.Error("initialize River", "error", err)
		os.Exit(1)
	}
	if err = client.Start(ctx); err != nil {
		logger.Error("start River", "error", err)
		os.Exit(1)
	}
	go fulfillment.NewScheduler(pool, client).Run(ctx)
	// Lifecycle sweeps: gift and offer expiry always, plus blocklist refresh and
	// anomaly evaluation when the encryption key makes a source credential
	// readable.
	var operations *panelpg.Service
	if len(cfg.DataEncryptionKey) == 32 {
		operations, err = panelpg.New(pool, cfg.DataEncryptionKey, panelpg.Options{})
		if err != nil {
			logger.Error("initialize operations service", "error", err)
			os.Exit(1)
		}
	}
	go sweeper.New(pool, operations, logger, sweeper.Config{}).Run(ctx)

	go retention.New(pool, logger, retention.Config{
		Outbox: cfg.Retention.Outbox, Telemetry: cfg.Retention.Telemetry,
		Drift: cfg.Retention.Drift, Interval: cfg.Retention.Interval,
	}).Run(ctx)

	backupService := backup.New(pool, logger, backup.Config{
		Enabled: cfg.Backup.Enabled, Directory: cfg.Backup.Directory, Interval: cfg.Backup.Interval,
		Retention: cfg.Backup.Retention, EncryptionKey: cfg.Backup.EncryptionKey,
		PgDumpPath: cfg.Backup.PgDumpPath, PgRestorePath: cfg.Backup.PgRestorePath,
		DatabaseURL: cfg.DatabaseURL,
	})
	go backupService.Run(ctx)

	metricsServer := startOperationalServer(logger, cfg, pool, metrics)

	logger.Info("worker started", "queue_backend", "river", "backups_enabled", backupService.Enabled())
	<-ctx.Done()
	stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if metricsServer != nil {
		if err := metricsServer.Shutdown(stopCtx); err != nil {
			logger.Warn("worker operational server shutdown failed", "error", err)
		}
	}
	// River drains in-flight jobs before returning, so a shutdown never
	// abandons a fulfillment run mid-flight.
	if err := client.Stop(stopCtx); err != nil {
		logger.Error("stop River", "error", err)
	}
	logger.Info("worker stopped")
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
