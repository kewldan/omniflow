package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omniflow/omniflow/internal/catalogpg"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/config"
	"github.com/omniflow/omniflow/internal/customerpg"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/fulfillment"
	apihttp "github.com/omniflow/omniflow/internal/httpapi"
	"github.com/omniflow/omniflow/internal/importservice"
	"github.com/omniflow/omniflow/internal/jobs"
	"github.com/omniflow/omniflow/internal/maintenance"
	"github.com/omniflow/omniflow/internal/payments"
	"github.com/omniflow/omniflow/internal/paymentservice"
	"github.com/omniflow/omniflow/internal/platform"
	"github.com/omniflow/omniflow/internal/remnawave"
	"github.com/omniflow/omniflow/internal/telemetry"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	telemetryClient := telemetry.NewClient(logger, telemetry.Config{
		Enabled:          cfg.TelemetryEnabled,
		Endpoint:         cfg.TelemetryEndpoint,
		Version:          version,
		Service:          "api",
		InstallationID:   telemetry.ResolveInstallationID(ctx, cfg.DatabaseURL, logger),
		CollectorEnabled: cfg.TelemetryCollectorEnabled,
		DatabaseURL:      cfg.DatabaseURL,
	})
	defer telemetryClient.Close()
	telemetryClient.Start(ctx)

	var metrics *platform.Metrics
	if cfg.MetricsEnabled {
		metrics = platform.NewMetrics("api")
	}
	health := platform.NewHealth(2*time.Second, 3*time.Second)

	runtime, closeCommerce, err := buildCommerce(ctx, logger, cfg, health, metrics)
	if err != nil {
		logger.Error("initialize commerce", "error", err)
		os.Exit(1)
	}
	defer closeCommerce()

	server := &http.Server{
		Addr: cfg.HTTPAddr,
		Handler: apihttp.NewRouter(logger, apihttp.RouterOptions{
			Health: health, Metrics: metrics, Commerce: runtime.handlers,
			CollectorEnabled: cfg.TelemetryCollectorEnabled, Telemetry: telemetryClient, Version: version,
		}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       90 * time.Second,
	}

	go func() {
		logger.Info("api listening", "address", cfg.HTTPAddr, "telemetry_enabled", cfg.TelemetryEnabled, "metrics_enabled", cfg.MetricsEnabled)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("api stopped unexpectedly", "error", err)
			stop()
		}
	}()

	<-ctx.Done()
	// Stop accepting new work first, then let in-flight requests and the
	// background loops finish before the pools close.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("api shutdown failed", "error", err)
	}
	logger.Info("api stopped")
}

// runtimeServices is what main needs back from the commerce wiring.
type runtimeServices struct {
	handlers *apihttp.CommerceHandlers
}

func buildCommerce(ctx context.Context, logger *slog.Logger, cfg config.Config, health *platform.Health, metrics *platform.Metrics) (runtimeServices, func(), error) {
	if cfg.DatabaseURL == "" {
		return runtimeServices{}, func() {}, nil
	}
	if cfg.OperatorToken == "" || len(cfg.DataEncryptionKey) != 32 || cfg.RemnawaveURL == "" || cfg.RemnawaveToken == "" || cfg.ValkeyURL == "" {
		return runtimeServices{}, nil, errors.New("commerce requires APP_OPERATOR_TOKEN, APP_DATA_ENCRYPTION_KEY, APP_REMNAWAVE_URL, APP_REMNAWAVE_TOKEN, and APP_VALKEY_URL")
	}
	pool, err := platform.TracedPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return runtimeServices{}, nil, err
	}
	valkeyClient, err := platform.NewValkeyClient(cfg.ValkeyURL)
	if err != nil {
		pool.Close()
		return runtimeServices{}, nil, err
	}
	riverClient, err := jobs.NewClient(pool)
	if err != nil {
		valkeyClient.Close()
		pool.Close()
		return runtimeServices{}, nil, err
	}
	enqueue := func(ctx context.Context, tx pgx.Tx, operationID string) error {
		_, err := riverClient.InsertTx(ctx, tx, fulfillment.JobArgs{OperationID: operationID}, fulfillment.InsertOpts())
		return err
	}
	commerceStore := commercepg.New(pool, enqueue, commercepg.Options{
		Subscriptions: cfg.Subscriptions.Policy(),
		TopUp:         cfg.TopUp.Limits(),
		Logger:        logger,
	})
	go commerceStore.RunMaintenance(ctx, logger)
	// The API process settles Stars only through the bot's authenticated update
	// stream, so it registers the adapter with a refund-capable configuration
	// only when a bot token is present.
	starsAdapter := payments.Provider(&payments.TelegramStars{})
	if cfg.TelegramToken != "" {
		configured, providerErr := payments.NewTelegramStars(cfg.TelegramToken, paymentservice.NewStarsPayerResolver(pool))
		if providerErr != nil {
			valkeyClient.Close()
			pool.Close()
			return runtimeServices{}, nil, providerErr
		}
		starsAdapter = configured
	}
	providers := []payments.Provider{payments.Manual{}, starsAdapter}
	if cfg.CryptoBotToken != "" {
		provider, providerErr := payments.NewCryptoBot(cfg.CryptoBotToken, cfg.CryptoBotTestnet)
		if providerErr != nil {
			valkeyClient.Close()
			pool.Close()
			return runtimeServices{}, nil, providerErr
		}
		providers = append(providers, provider)
	}
	if cfg.YooKassaShopID != "" || cfg.YooKassaSecret != "" {
		provider, providerErr := payments.NewYooKassa(cfg.YooKassaShopID, cfg.YooKassaSecret)
		if providerErr != nil {
			valkeyClient.Close()
			pool.Close()
			return runtimeServices{}, nil, providerErr
		}
		providers = append(providers, provider)
	}
	remnawaveClient, err := remnawave.NewClient(cfg.RemnawaveURL, cfg.RemnawaveToken)
	if err != nil {
		valkeyClient.Close()
		pool.Close()
		return runtimeServices{}, nil, err
	}
	customerService, err := customerpg.New(pool, cfg.DataEncryptionKey, 30*24*time.Hour)
	if err != nil {
		valkeyClient.Close()
		pool.Close()
		return runtimeServices{}, nil, err
	}
	paymentService := paymentservice.New(pool, commerceStore, providers...).WithMetrics(metrics)
	go paymentService.RunReconciler(ctx)

	// Readiness names every dependency a request can touch. The probes are
	// cheap, cached, and never echo a connection string into the response.
	health.Register("postgres", func(probeCtx context.Context) error { return pool.Ping(probeCtx) })
	health.Register("valkey", func(probeCtx context.Context) error {
		return valkeyClient.Do(probeCtx, valkeyClient.B().Ping().Build()).Error()
	})
	health.Register("remnawave", remnawaveClient.Ping)

	// The API process owns automatic maintenance detection because it is the
	// process that already probes every dependency for readiness.
	maintenanceController := maintenance.NewController(commerceStore, health, metrics, logger, maintenance.Config{
		AutoDetect: cfg.Maintenance.AutoDetect, ProbeInterval: cfg.Maintenance.ProbeInterval,
		FailureStreak: cfg.Maintenance.FailureStreak, RecoveryStreak: cfg.Maintenance.RecoveryStreak,
	})
	go maintenanceController.Run(ctx)

	handlers := apihttp.NewCommerceHandlers(dbgen.New(pool), catalogpg.New(pool), commerceStore, paymentService, customerService, importservice.New(pool, remnawaveClient), fulfillment.NewService(pool, riverClient), platform.NewRateLimiter(valkeyClient), cfg.OperatorToken)
	handlers.WithOperations(pool, riverClient, commerceStore)
	return runtimeServices{handlers: handlers}, func() { valkeyClient.Close(); pool.Close() }, nil
}
