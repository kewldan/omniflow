package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	token := os.Getenv("OMNIFLOW_TELEGRAM_TOKEN")
	if token == "" {
		logger.Info("telegram bot is disabled because OMNIFLOW_TELEGRAM_TOKEN is empty")
		<-ctx.Done()
		return
	}

	client, err := bot.New(token, bot.WithDefaultHandler(func(ctx context.Context, client *bot.Bot, update *models.Update) {
		if update.Message == nil {
			return
		}
		_, _ = client.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: update.Message.Chat.ID,
			Text:   "Omniflow is starting up. Self-service flows are coming soon.",
		})
	}))
	if err != nil {
		logger.Error("telegram client initialization failed", "error", err)
		os.Exit(1)
	}

	logger.Info("telegram bot started")
	client.Start(ctx)
}
