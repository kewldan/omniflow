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
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/catalogpg"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/config"
	"github.com/omniflow/omniflow/internal/customerpg"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/fulfillment"
	apihttp "github.com/omniflow/omniflow/internal/httpapi"
	"github.com/omniflow/omniflow/internal/importservice"
	"github.com/omniflow/omniflow/internal/jobs"
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

	commerceHandlers, closeCommerce, err := buildCommerce(ctx, cfg)
	if err != nil {
		logger.Error("initialize commerce", "error", err)
		os.Exit(1)
	}
	defer closeCommerce()

	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           apihttp.NewRouter(logger, version, telemetryClient, cfg.TelemetryCollectorEnabled, commerceHandlers),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       90 * time.Second,
	}

	go func() {
		logger.Info("api listening", "address", cfg.HTTPAddr, "telemetry_enabled", cfg.TelemetryEnabled)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("api stopped unexpectedly", "error", err)
			stop()
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("api shutdown failed", "error", err)
	}
}

func buildCommerce(ctx context.Context, cfg config.Config) (*apihttp.CommerceHandlers, func(), error) {
	if cfg.DatabaseURL == "" {
		return nil, func() {}, nil
	}
	if cfg.OperatorToken == "" || len(cfg.DataEncryptionKey) != 32 || cfg.RemnawaveURL == "" || cfg.RemnawaveToken == "" || cfg.ValkeyURL == "" {
		return nil, nil, errors.New("commerce requires APP_OPERATOR_TOKEN, APP_DATA_ENCRYPTION_KEY, APP_REMNAWAVE_URL, APP_REMNAWAVE_TOKEN, and APP_VALKEY_URL")
	}
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, nil, err
	}
	valkeyClient, err := platform.NewValkeyClient(cfg.ValkeyURL)
	if err != nil {
		pool.Close()
		return nil, nil, err
	}
	riverClient, err := jobs.NewClient(pool)
	if err != nil {
		valkeyClient.Close()
		pool.Close()
		return nil, nil, err
	}
	enqueue := func(ctx context.Context, tx pgx.Tx, operationID string) error {
		_, err := riverClient.InsertTx(ctx, tx, fulfillment.JobArgs{OperationID: operationID}, fulfillment.InsertOpts())
		return err
	}
	commerceStore := commercepg.New(pool, enqueue)
	go commerceStore.RunMaintenance(ctx)
	providers := []payments.Provider{payments.Manual{}, payments.TelegramStars{}}
	if cfg.CryptoBotToken != "" {
		provider, providerErr := payments.NewCryptoBot(cfg.CryptoBotToken, cfg.CryptoBotTestnet)
		if providerErr != nil {
			valkeyClient.Close()
			pool.Close()
			return nil, nil, providerErr
		}
		providers = append(providers, provider)
	}
	if cfg.YooKassaShopID != "" || cfg.YooKassaSecret != "" {
		provider, providerErr := payments.NewYooKassa(cfg.YooKassaShopID, cfg.YooKassaSecret)
		if providerErr != nil {
			valkeyClient.Close()
			pool.Close()
			return nil, nil, providerErr
		}
		providers = append(providers, provider)
	}
	remnawaveClient, err := remnawave.NewClient(cfg.RemnawaveURL, cfg.RemnawaveToken)
	if err != nil {
		valkeyClient.Close()
		pool.Close()
		return nil, nil, err
	}
	customerService, err := customerpg.New(pool, cfg.DataEncryptionKey, 30*24*time.Hour)
	if err != nil {
		valkeyClient.Close()
		pool.Close()
		return nil, nil, err
	}
	paymentService := paymentservice.New(pool, commerceStore, providers...)
	go paymentService.RunReconciler(ctx)
	handlers := apihttp.NewCommerceHandlers(dbgen.New(pool), catalogpg.New(pool), commerceStore, paymentService, customerService, importservice.New(pool, remnawaveClient), fulfillment.NewService(pool, riverClient), platform.NewRateLimiter(valkeyClient), cfg.OperatorToken)
	return handlers, func() { valkeyClient.Close(); pool.Close() }, nil
}
