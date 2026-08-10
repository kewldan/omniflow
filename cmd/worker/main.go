package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/config"
	"github.com/omniflow/omniflow/internal/fulfillment"
	"github.com/omniflow/omniflow/internal/jobs"
	"github.com/omniflow/omniflow/internal/remnawave"
	"github.com/riverqueue/river"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.LoadWorker()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
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
	workers := river.NewWorkers()
	river.AddWorker(workers, fulfillment.NewWorker(pool, remnawaveClient))
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
	logger.Info("worker started", "queue_backend", "river")
	<-ctx.Done()
	stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := client.Stop(stopCtx); err != nil {
		logger.Error("stop River", "error", err)
	}
	logger.Info("worker stopped")
}
