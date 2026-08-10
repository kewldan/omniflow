package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	telegram "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/botapp"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/config"
	"github.com/omniflow/omniflow/internal/payments"
	"github.com/omniflow/omniflow/internal/paymentservice"
	"github.com/omniflow/omniflow/internal/platform"
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

	sender := botapp.NewSender(client, identities, logger)
	settings := commerceSettings(cfg)
	commerceService, closeCommerce, err := buildCommerce(ctx, logger, cfg, identities)
	if err != nil {
		logger.Error("bot commerce initialization failed", "error", err)
		os.Exit(1)
	}
	defer closeCommerce()
	if commerceService != nil {
		limiter, limiterErr := buildLimiter(cfg.ValkeyURL)
		if limiterErr != nil {
			logger.Error("valkey initialization failed", "error", limiterErr)
			os.Exit(1)
		}
		application.EnableCommerce(identities, commerceService, limiter, sender, settings)
		logger.Info("telegram commerce enabled", "currency", settings.Currency)
	}
	go botapp.NewNotifier(logger, sender, identities, remnawaveClient, commerceService, settings).Run(ctx)

	logger.Info("telegram bot started", "remnawave_api", "3.2.2")
	client.Start(ctx)
	logger.Info("telegram bot stopped")
}

func commerceSettings(cfg config.BotConfig) botapp.CommerceSettings {
	return botapp.CommerceSettings{
		Currency:               cfg.DefaultCurrency,
		PublicURL:              cfg.PublicURL,
		TermsURL:               cfg.TermsURL,
		RecoveryWindow:         cfg.RecoveryWindow,
		MinimumTrialAccountAge: cfg.MinimumTrialAccountAge,
		MarketingFrequencyCap:  cfg.MarketingFrequencyCap,
		MarketingWindow:        cfg.MarketingWindow,
	}
}

func buildLimiter(valkeyURL string) (*platform.RateLimiter, error) {
	client, err := platform.NewValkeyClient(valkeyURL)
	if err != nil {
		return nil, err
	}
	return platform.NewRateLimiter(client), nil
}

// buildCommerce wires the checkout surface. The bot creates orders and payment
// intents but never enqueues fulfillment itself: the worker owns provisioning,
// and settlement writes the durable job through the outbox and River records the
// API process already commits.
func buildCommerce(ctx context.Context, logger *slog.Logger, cfg config.BotConfig, store *botapp.PostgresStore) (*botapp.Commerce, func(), error) {
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, func() {}, err
	}
	orders := commercepg.New(pool, nil)
	starsAdapter, err := payments.NewTelegramStars(cfg.TelegramToken, paymentservice.NewStarsPayerResolver(pool))
	if err != nil {
		pool.Close()
		return nil, func() {}, err
	}
	providers := []payments.Provider{starsAdapter, payments.Manual{}}
	if cfg.CryptoBotToken != "" {
		provider, providerErr := payments.NewCryptoBot(cfg.CryptoBotToken, cfg.CryptoBotTestnet)
		if providerErr != nil {
			pool.Close()
			return nil, func() {}, providerErr
		}
		providers = append(providers, provider)
	}
	if cfg.YooKassaShopID != "" {
		provider, providerErr := payments.NewYooKassa(cfg.YooKassaShopID, cfg.YooKassaSecret)
		if providerErr != nil {
			pool.Close()
			return nil, func() {}, providerErr
		}
		providers = append(providers, provider)
	}
	paymentService := paymentservice.New(pool, orders, providers...)
	return botapp.NewCommerce(logger, store, orders, paymentService, commerceSettings(cfg)), pool.Close, nil
}

func configureCommands(ctx context.Context, logger *slog.Logger, client *telegram.Bot) {
	commands := []models.BotCommand{
		{Command: "start", Description: "Open your Omniflow account"},
		{Command: "menu", Description: "Show the main menu"},
		{Command: "plans", Description: "Browse plans and buy"},
		{Command: "orders", Description: "Order and payment history"},
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
		{Command: "plans", Description: "Тарифы и покупка"},
		{Command: "orders", Description: "История заказов и оплат"},
		{Command: "settings", Description: "Язык и уведомления"},
		{Command: "support", Description: "Написать в поддержку"},
		{Command: "cancel", Description: "Отменить текущее действие"},
	}
	if _, err := client.SetMyCommands(ctx, &telegram.SetMyCommandsParams{Commands: russian, LanguageCode: "ru"}); err != nil {
		logger.Warn("telegram Russian command setup failed", "error", err)
	}
}
