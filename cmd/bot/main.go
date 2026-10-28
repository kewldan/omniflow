package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	telegram "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/app"
	"github.com/omniflow/omniflow/internal/backup"
	"github.com/omniflow/omniflow/internal/botapp"
	"github.com/omniflow/omniflow/internal/channelworker"
	"github.com/omniflow/omniflow/internal/config"
	"github.com/omniflow/omniflow/internal/customerauthpg"
	"github.com/omniflow/omniflow/internal/fulfillment"
	"github.com/omniflow/omniflow/internal/goodsdelivery"
	apihttp "github.com/omniflow/omniflow/internal/httpapi"
	"github.com/omniflow/omniflow/internal/jobs"
	"github.com/omniflow/omniflow/internal/operator"
	"github.com/omniflow/omniflow/internal/platform"
	"github.com/omniflow/omniflow/internal/remnawave"
)

var version = "dev"

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
	// One sized pool serves the whole process: identities, commerce, and the
	// operator tools alike.
	pool, err := app.OpenPool(ctx, cfg.DatabaseURL, cfg.DatabaseMaxConns)
	if err != nil {
		logger.Error("connect database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()
	identities, err := botapp.NewPostgresStoreWithPool(pool)
	if err != nil {
		logger.Error("telegram identity store initialization failed", "error", err)
		os.Exit(1)
	}
	remnawaveClient, err := remnawave.NewClient(cfg.RemnawaveURL, cfg.RemnawaveToken)
	if err != nil {
		logger.Error("Remnawave client initialization failed", "error", err)
		os.Exit(1)
	}
	application := botapp.New(logger, identities, remnawaveClient, cfg.SupportURL)
	metrics := platform.NewMetrics("bot")
	options := []telegram.Option{
		telegram.WithDefaultHandler(application.HandleDefault),
		telegram.WithMiddlewares(botapp.ObservabilityMiddleware(metrics)),
	}
	if cfg.Webhook.Enabled {
		options = append(options, telegram.WithWebhookSecretToken(cfg.Webhook.SecretToken))
	}
	client, err := telegram.New(cfg.TelegramToken, options...)
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
	commerceService, fulfillmentService, err := buildCommerce(logger, cfg, pool, identities, metrics)
	if err != nil {
		logger.Error("bot commerce initialization failed", "error", err)
		os.Exit(1)
	}
	limiter, err := buildLimiter(cfg.ValkeyURL)
	if err != nil {
		logger.Error("valkey initialization failed", "error", err)
		os.Exit(1)
	}
	application.EnableCommerce(identities, commerceService, limiter, sender, settings)
	logger.Info("telegram commerce enabled", "currency", settings.Currency, "multi_subscription", cfg.Subscriptions.MultiEnabled)

	// Backups and restores are the only administrative actions Telegram
	// carries, and only for explicitly named operator accounts.
	backupService := backup.New(pool, logger, backup.Config{
		Enabled: cfg.Backup.Enabled, Directory: cfg.Backup.Directory, Interval: cfg.Backup.Interval,
		Retention: cfg.Backup.Retention, EncryptionKey: cfg.Backup.EncryptionKey,
		PgDumpPath: cfg.Backup.PgDumpPath, PgRestorePath: cfg.Backup.PgRestorePath,
		DatabaseURL: cfg.DatabaseURL,
	})
	application.EnableOperatorTools(backupService, cfg.Operator.OperatorIDs)

	// The web sign-in link is delivered through this chat, so the bot is what
	// issues it. It stays off unless the operator enabled the fallback, and the
	// /login command then reports the route as unavailable.
	if cfg.CustomerPanel.MagicLinkEnabled && len(cfg.DataEncryptionKey) == 32 {
		identity, identityErr := customerauthpg.New(pool, cfg.DataEncryptionKey, customerauthpg.Options{
			TelegramBotToken: cfg.TelegramToken,
			MagicLinkEnabled: true,
			PublicURL:        cfg.PublicURL,
		})
		if identityErr != nil {
			logger.Error("customer web sign-in could not be configured", "error", identityErr)
			os.Exit(1)
		}
		application.WithWebSignIn(webSignIn{service: identity})
	}

	// Channel membership is verified in two places, and both need the bot's
	// token. The worker re-checks everybody periodically, warns, and takes
	// access away after a grace period through the fulfillment pipeline; the
	// app checks live at checkout, before money moves.
	membership := botapp.NewTelegramMembership(client)
	application.UseMembershipVerifier(membership)
	go channelworker.New(pool, membership, logger, channelworker.Config{
		Enforcer: channelworker.NewFulfillmentEnforcer(pool, fulfillmentService),
		Notifier: application,
	}).Run(ctx)

	notifier := operator.New(pool, client, logger, operator.Config{
		ChatID: cfg.Operator.ChatID, NotificationCap: cfg.Operator.NotificationCap, Window: cfg.Operator.Window,
	})
	go notifier.Run(ctx)
	go botapp.NewNotifier(logger, sender, identities, remnawaveClient, commerceService, settings).Run(ctx)

	operationalServer := startOperationalServer(logger, cfg, pool, metrics)
	webhookServer := startWebhookServer(logger, cfg, client)

	logger.Info("telegram bot started", "remnawave_api", "3.2.2", "mode", updateMode(cfg))
	if cfg.Webhook.Enabled {
		if err = registerWebhook(ctx, logger, client, cfg); err != nil {
			logger.Error("telegram webhook registration failed", "error", err)
			os.Exit(1)
		}
		client.StartWebhook(ctx)
	} else {
		client.Start(ctx)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for _, server := range []*http.Server{webhookServer, operationalServer} {
		if server == nil {
			continue
		}
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Warn("bot HTTP server shutdown failed", "error", err)
		}
	}
	logger.Info("telegram bot stopped")
}

func updateMode(cfg config.BotConfig) string {
	if cfg.Webhook.Enabled {
		return "webhook"
	}
	return "long_polling"
}

// registerWebhook points Telegram at this installation and pins the secret token
// it must present. Every update is then rejected unless it carries the token.
func registerWebhook(ctx context.Context, logger *slog.Logger, client *telegram.Bot, cfg config.BotConfig) error {
	_, err := client.SetWebhook(ctx, &telegram.SetWebhookParams{
		URL:         cfg.Webhook.URL,
		SecretToken: cfg.Webhook.SecretToken,
		AllowedUpdates: []string{
			"message", "edited_message", "callback_query", "pre_checkout_query",
			"my_chat_member", "chat_member",
		},
	})
	if err != nil {
		return err
	}
	logger.Info("telegram webhook registered")
	return nil
}

// startWebhookServer serves the Telegram webhook endpoint. The library verifies
// the secret token before an update is accepted, and the body limit keeps a
// malformed or hostile request from consuming memory.
func startWebhookServer(logger *slog.Logger, cfg config.BotConfig, client *telegram.Bot) *http.Server {
	if !cfg.Webhook.Enabled {
		return nil
	}
	mux := http.NewServeMux()
	handler := client.WebhookHandler()
	mux.HandleFunc("POST /telegram/webhook", func(writer http.ResponseWriter, request *http.Request) {
		request.Body = http.MaxBytesReader(writer, request.Body, 1<<20)
		handler(writer, request)
	})
	server := &http.Server{
		Addr:              cfg.Webhook.ListenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
	go func() {
		logger.Info("telegram webhook endpoint listening", "address", cfg.Webhook.ListenAddr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("telegram webhook server stopped unexpectedly", "error", err)
		}
	}()
	return server
}

// startOperationalServer exposes the bot's liveness, readiness, and metrics
// endpoints. It shares the webhook listener only when they are not both
// configured on the same address.
func startOperationalServer(logger *slog.Logger, cfg config.BotConfig, pool *pgxpool.Pool, metrics *platform.Metrics) *http.Server {
	address := cfg.Webhook.MetricsAddr
	// Serving metrics on the webhook listener would publish installation
	// internals on a host that must be reachable from the internet.
	if address == "" || address == cfg.Webhook.ListenAddr {
		return nil
	}
	health := platform.NewHealth(2*time.Second, 3*time.Second)
	if pool != nil {
		health.Register("postgres", func(probeCtx context.Context) error { return pool.Ping(probeCtx) })
	}
	server := &http.Server{
		Addr:              address,
		Handler:           apihttp.NewRouter(logger, apihttp.RouterOptions{Health: health, Metrics: metrics, Version: version}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
	go func() {
		logger.Info("bot operational endpoints listening", "address", address)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("bot operational server stopped unexpectedly", "error", err)
		}
	}()
	return server
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
		DeliveryRate:           cfg.DeliveryRate,
		CartTTL:                cfg.CartTTL,
		MultiSubscription:      cfg.Subscriptions.MultiEnabled,
	}
}

func buildLimiter(valkeyURL string) (*platform.RateLimiter, error) {
	client, err := platform.NewValkeyClient(valkeyURL)
	if err != nil {
		return nil, err
	}
	return platform.NewRateLimiter(client), nil
}

// buildCommerce wires the checkout surface. Settlements that close inside this
// process — a Telegram Stars payment, an order the wallet covered, a trial, a
// claimed gift or access code — insert their fulfillment job in the same
// transaction, exactly as the api does, because both build the commerce core
// through app.NewCommerceCore. The River client here only inserts; the worker
// is the only process that runs jobs.
func buildCommerce(
	logger *slog.Logger, cfg config.BotConfig, pool *pgxpool.Pool,
	store *botapp.PostgresStore, metrics *platform.Metrics,
) (*botapp.Commerce, *fulfillment.Service, error) {
	riverClient, err := jobs.NewInsertOnlyClient(pool)
	if err != nil {
		return nil, nil, err
	}
	core, err := app.NewCommerceCore(cfg.Commerce(), pool, riverClient, app.CommerceOptions{
		Logger: logger, Metrics: metrics,
	})
	if err != nil {
		return nil, nil, err
	}
	commerceService, err := botapp.NewCommerce(logger, store, core.Store, core.Payments, commerceSettings(cfg))
	if err != nil {
		return nil, nil, err
	}
	// The shop needs the encryption key to unseal a gateway credential before it
	// can quote a price. Without one the catalog stays empty rather than the bot
	// refusing to start: an installation that sells no digital goods never needs
	// the key.
	if len(cfg.DataEncryptionKey) == 32 {
		registry, registryErr := goodsdelivery.NewRegistry(pool, cfg.DataEncryptionKey, cfg.DefaultCurrency)
		if registryErr != nil {
			return nil, nil, registryErr
		}
		commerceService.EnableShop(registry)
	} else {
		logger.Info("digital goods shop disabled", "reason", "APP_DATA_ENCRYPTION_KEY is not set")
	}
	// The channel worker suspends and restores through the fulfillment
	// pipeline, which inserts through the same client.
	return commerceService, core.Fulfillment, nil
}

// webSignIn adapts the identity service to the narrow interface the bot needs.
//
// The bot supplies no request context: an update arriving over long polling or a
// webhook has no client address of its own, and recording the Telegram server's
// address against a customer's security log would be worse than recording none.
type webSignIn struct{ service *customerauthpg.Service }

func (issuer webSignIn) IssueMagicLink(ctx context.Context, customerID string) (string, error) {
	return issuer.service.IssueMagicLink(ctx, customerID, customerauthpg.RequestContext{})
}

func configureCommands(ctx context.Context, logger *slog.Logger, client *telegram.Bot) {
	commands := []models.BotCommand{
		{Command: "start", Description: "Open your Omniflow account"},
		{Command: "menu", Description: "Show the main menu"},
		{Command: "plans", Description: "Browse plans and buy"},
		{Command: "orders", Description: "Order and payment history"},
		{Command: "wallet", Description: "Wallet balance and top-up"},
		{Command: "settings", Description: "Language and notifications"},
		{Command: "support", Description: "Contact support"},
		{Command: "login", Description: "Sign in on the website"},
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
		{Command: "wallet", Description: "Баланс и пополнение"},
		{Command: "settings", Description: "Язык и уведомления"},
		{Command: "support", Description: "Написать в поддержку"},
		{Command: "cancel", Description: "Отменить текущее действие"},
	}
	if _, err := client.SetMyCommands(ctx, &telegram.SetMyCommandsParams{Commands: russian, LanguageCode: "ru"}); err != nil {
		logger.Warn("telegram Russian command setup failed", "error", err)
	}
}
