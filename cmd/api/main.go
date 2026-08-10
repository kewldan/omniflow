package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/omniflow/omniflow/internal/config"
	apihttp "github.com/omniflow/omniflow/internal/httpapi"
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

	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           apihttp.NewRouter(logger, version, telemetryClient, cfg.TelemetryCollectorEnabled),
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
