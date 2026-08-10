package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	telegram "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/omniflow/omniflow/internal/botapp"
	"github.com/omniflow/omniflow/internal/config"
	"github.com/omniflow/omniflow/internal/remnawave"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	if os.Getenv("APP_TELEGRAM_TOKEN") == "" {
		logger.Info("telegram bot disabled because APP_TELEGRAM_TOKEN is empty")
		<-ctx.Done()
		return
	}
	cfg, err := config.LoadBot()
	if err != nil {
		logger.Error("invalid bot configuration", "error", err)
		os.Exit(1)
	}
	identities, err := botapp.NewPostgresStore(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("telegram identity store initialization failed", "error", err)
		os.Exit(1)
	}
	defer identities.Close()
	remnawaveClient, err := remnawave.NewClient(cfg.RemnawaveURL, cfg.RemnawaveToken)
	if err != nil {
		logger.Error("Remnawave client initialization failed", "error", err)
		os.Exit(1)
	}
	application := botapp.New(logger, identities, remnawaveClient, cfg.SupportURL)
	client, err := telegram.New(cfg.TelegramToken, telegram.WithDefaultHandler(application.HandleDefault))
	if err != nil {
		logger.Error("telegram client initialization failed", "error", err)
		os.Exit(1)
	}
	application.Register(client)
	if me, getMeErr := client.GetMe(ctx); getMeErr != nil {
		logger.Warn("telegram bot identity lookup failed; referral sharing is unavailable", "error", getMeErr)
	} else {
		application.SetBotUsername(me.Username)
	}
	configureCommands(ctx, logger, client)
	go botapp.RunNotifications(ctx, logger, client, identities, remnawaveClient)

	logger.Info("telegram bot started", "remnawave_api", "3.2.2")
	client.Start(ctx)
	logger.Info("telegram bot stopped")
}

func configureCommands(ctx context.Context, logger *slog.Logger, client *telegram.Bot) {
	commands := []models.BotCommand{
		{Command: "start", Description: "Open your Omniflow account"},
		{Command: "menu", Description: "Show the main menu"},
		{Command: "settings", Description: "Language and notifications"},
		{Command: "support", Description: "Contact support"},
		{Command: "cancel", Description: "Cancel the current action"},
	}
	if _, err := client.SetMyCommands(ctx, &telegram.SetMyCommandsParams{Commands: commands}); err != nil {
		logger.Warn("telegram command setup failed", "error", err)
	}
	russian := []models.BotCommand{
		{Command: "start", Description: "Открыть аккаунт Omniflow"},
		{Command: "menu", Description: "Показать главное меню"},
		{Command: "settings", Description: "Язык и уведомления"},
		{Command: "support", Description: "Написать в поддержку"},
		{Command: "cancel", Description: "Отменить текущее действие"},
	}
	if _, err := client.SetMyCommands(ctx, &telegram.SetMyCommandsParams{Commands: russian, LanguageCode: "ru"}); err != nil {
		logger.Warn("telegram Russian command setup failed", "error", err)
	}
}
