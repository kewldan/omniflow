package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	logger.Info("worker started", "queue_backend", "river")

	// River workers are registered here as domain modules are implemented.
	<-ctx.Done()
	logger.Info("worker stopped")
}
