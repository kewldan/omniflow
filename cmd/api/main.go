package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/accountcheckout"
	"github.com/omniflow/omniflow/internal/accountpg"
	"github.com/omniflow/omniflow/internal/accountreferral"
	"github.com/omniflow/omniflow/internal/accountshop"
	"github.com/omniflow/omniflow/internal/accountsupport"
	"github.com/omniflow/omniflow/internal/adminauthpg"
	"github.com/omniflow/omniflow/internal/catalogpg"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/config"
	"github.com/omniflow/omniflow/internal/customerauthpg"
	"github.com/omniflow/omniflow/internal/customerpg"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/fulfillment"
	"github.com/omniflow/omniflow/internal/goodsdelivery"
	apihttp "github.com/omniflow/omniflow/internal/httpapi"
	"github.com/omniflow/omniflow/internal/importservice"
	"github.com/omniflow/omniflow/internal/jobs"
	"github.com/omniflow/omniflow/internal/maintenance"
	"github.com/omniflow/omniflow/internal/panelpg"
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
			Health: health, Metrics: metrics, Commerce: runtime.handlers, Admin: runtime.admin,
			Account:          runtime.account,
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
	admin    *apihttp.AdminHandlers
	account  *apihttp.AccountHandlers
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
	providers, err := paymentservice.ConfigureProviders(pool, paymentservice.ProviderOptions{
		TelegramToken:    cfg.TelegramToken,
		CryptoBotToken:   cfg.CryptoBotToken,
		CryptoBotTestnet: cfg.CryptoBotTestnet,
		YooKassaShopID:   cfg.YooKassaShopID,
		YooKassaSecret:   cfg.YooKassaSecret,
	})
	if err != nil {
		valkeyClient.Close()
		pool.Close()
		return runtimeServices{}, nil, err
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

	services := runtimeServices{handlers: handlers}
	if cfg.AdminPanel.Enabled {
		adminHandlers, adminErr := buildAdminPanel(
			ctx, logger, cfg, pool, platform.NewRateLimiter(valkeyClient), providers, health,
			fulfillment.NewService(pool, riverClient), remnawaveClient,
		)
		if adminErr != nil {
			valkeyClient.Close()
			pool.Close()
			return runtimeServices{}, nil, adminErr
		}
		services.admin = adminHandlers
	}
	if cfg.CustomerPanel.Enabled {
		accountHandlers, identity, accountErr := buildCustomerPanel(
			logger, cfg, pool, platform.NewRateLimiter(valkeyClient), remnawaveClient,
			commerceStore, paymentService,
		)
		if accountErr != nil {
			valkeyClient.Close()
			pool.Close()
			return runtimeServices{}, nil, accountErr
		}
		services.account = accountHandlers
		// The operator panel manages the customer sign-in providers, so it needs
		// the same service the customer surface authenticates against.
		if services.admin != nil {
			services.admin.WithCustomerAuth(identity)
		}
	}
	return services, func() { valkeyClient.Close(); pool.Close() }, nil
}

// buildAdminPanel wires the operator panel API.
//
// It reuses APP_DATA_ENCRYPTION_KEY, already validated as 32 bytes by the
// commerce precondition above, to seal TOTP secrets.
func buildAdminPanel(
	ctx context.Context, logger *slog.Logger, cfg config.Config, pool *pgxpool.Pool,
	limiter *platform.RateLimiter, providers []payments.Provider, health *platform.Health,
	fulfillmentService *fulfillment.Service, remnawaveClient *remnawave.Client,
) (*apihttp.AdminHandlers, error) {
	// The public URL is what a passkey is bound to. Without one the service
	// leaves passkeys off rather than minting credentials the browser will
	// later decline to offer, which is a failure with no error message.
	service, err := adminauthpg.New(pool, cfg.DataEncryptionKey, adminauthpg.Options{
		PublicURL: cfg.PublicURL, ServiceName: cfg.AdminPanel.Issuer,
	})
	if err != nil {
		return nil, err
	}
	operations, err := panelpg.New(pool, cfg.DataEncryptionKey, panelpg.Options{})
	if err != nil {
		return nil, err
	}
	seedCommerceSettings(ctx, logger, operations, cfg)
	proxies, err := apihttp.NewTrustedProxies(cfg.AdminPanel.TrustedProxies)
	if err != nil {
		return nil, err
	}
	if !cfg.AdminPanel.CookieSecure {
		// Worth a loud line in the log: a session cookie without Secure can be
		// captured off any plain-HTTP request to the same host.
		logger.Warn("admin session cookie is not marked Secure; use this only for local development")
	}
	issueSetupTokenIfNeeded(logger, service)
	return apihttp.NewAdminHandlers(apihttp.AdminOptions{
		Service: service, Limiter: limiter, Logger: logger, Proxies: proxies,
		Operations: operations, Providers: paymentservice.Index(providers), Health: health,
		Fulfillment: fulfillmentService, Remnawave: remnawaveClient,
		CookieSecure: cfg.AdminPanel.CookieSecure, Issuer: cfg.AdminPanel.Issuer,
		Version: version, UpdateFeedURL: cfg.UpdateFeedURL,
	}), nil
}

// buildCustomerPanel wires the customer web API.
//
// It reuses APP_DATA_ENCRYPTION_KEY, already validated as 32 bytes above, to
// seal OIDC client secrets and the sign-in flow cookie. The Telegram bot token
// is what verifies a login-widget or Mini App payload; without one the panel
// still runs and simply does not offer that route.
func buildCustomerPanel(
	logger *slog.Logger, cfg config.Config, pool *pgxpool.Pool,
	limiter *platform.RateLimiter, remnawaveClient *remnawave.Client,
	commerceStore *commercepg.Store, paymentService *paymentservice.Service,
) (*apihttp.AccountHandlers, *customerauthpg.Service, error) {
	identity, err := customerauthpg.New(pool, cfg.DataEncryptionKey, customerauthpg.Options{
		TelegramBotToken: cfg.TelegramToken,
		MagicLinkEnabled: cfg.CustomerPanel.MagicLinkEnabled,
		PublicURL:        cfg.PublicURL,
	})
	if err != nil {
		return nil, nil, err
	}
	// The account read model records device removals and link rotations in the
	// same log the identity adapter writes sign-ins to, so the customer reads one
	// history rather than two partial ones.
	account, err := accountpg.New(pool, remnawaveClient, accountpg.Options{
		Logger: logger,
		Security: accountpg.SecurityRecorderFunc(func(
			ctx context.Context, customerID, event string,
			request accountpg.SecurityRequest, metadata map[string]any,
		) error {
			return identity.RecordSecurityEvent(ctx, customerID, event, customerauthpg.RequestContext{
				IP: parseAddress(request.IP), UserAgent: request.UserAgent, RequestID: request.RequestID,
			}, metadata)
		}),
	})
	if err != nil {
		return nil, nil, err
	}
	proxies, err := apihttp.NewTrustedProxies(cfg.AdminPanel.TrustedProxies)
	if err != nil {
		return nil, nil, err
	}
	if !cfg.CustomerPanel.CookieSecure {
		logger.Warn("customer session cookie is not marked Secure; use this only for local development")
	}
	if cfg.CustomerPanel.MagicLinkEnabled && cfg.PublicURL == "" {
		return nil, nil, errors.New("APP_PUBLIC_URL is required when the customer magic-link fallback is enabled")
	}

	checkout, err := accountcheckout.New(pool, commerceStore, paymentService, accountcheckout.Options{
		Logger: logger,
		Settings: accountcheckout.Settings{
			Currency: cfg.DefaultCurrency, PublicURL: cfg.PublicURL, TermsURL: cfg.TermsURL,
			RecoveryWindow: cfg.RecoveryWindow, MinimumTrialAccountAge: cfg.MinimumTrialAccountAge,
			MultiSubscription: cfg.Subscriptions.MultiEnabled,
		},
	})
	if err != nil {
		return nil, nil, err
	}
	support, err := accountsupport.New(pool, accountsupport.Options{
		Logger: logger, AttachmentDirectory: cfg.SupportAttachmentDir,
	})
	if err != nil {
		return nil, nil, err
	}
	// The contact key is the installation's own data key, already validated as 32
	// bytes by the commerce precondition. It has to be the same one customerpg
	// uses: a contact added from the panel must collide with one added through
	// the operator API under UNIQUE (kind, value_fingerprint), and that only
	// happens when both derive the fingerprint from the same key.
	referral, err := accountreferral.New(pool, accountreferral.Options{
		PublicURL: cfg.PublicURL, EncryptionKey: cfg.DataEncryptionKey, Logger: logger,
	})
	if err != nil {
		return nil, nil, err
	}
	// The shop needs the encryption key to unseal a gateway credential before it
	// can quote a price. The key is already validated as 32 bytes by the commerce
	// precondition, so the registry is always available here; the bot's
	// conditional exists because the bot may run without a key at all.
	goodsRegistry, err := goodsdelivery.NewRegistry(pool, cfg.DataEncryptionKey, cfg.DefaultCurrency)
	if err != nil {
		return nil, nil, err
	}
	shop, err := accountshop.New(pool, commerceStore, paymentService, goodsRegistry, accountshop.Options{
		Logger:   logger,
		Settings: accountshop.Settings{Currency: cfg.DefaultCurrency, PublicURL: cfg.PublicURL},
	})
	if err != nil {
		return nil, nil, err
	}

	return apihttp.NewAccountHandlers(apihttp.AccountOptions{
		Auth: identity, Account: account, Limiter: limiter, Logger: logger, Proxies: proxies,
		Checkout: checkout, Shop: shop, Support: support, Referral: referral,
		CookieSecure: cfg.CustomerPanel.CookieSecure,
	}), identity, nil
}

// parseAddress converts a rendered client address back for storage, yielding nil
// for anything unparseable so a malformed value is recorded as "not observed"
// rather than as a placeholder somebody might read as real.
func parseAddress(value string) *netip.Addr {
	address, err := netip.ParseAddr(value)
	if err != nil {
		return nil
	}
	return &address
}

// providerIndex keys the configured adapters by name so the panel can publish
// what each one declares about storing a payment method.

// seedCommerceSettings writes the wallet and subscription policy into the
// database the first time the panel starts.
//
// Both were environment variables until v0.7. Seeding from the environment
// rather than from the schema defaults means an installation upgrading from
// v0.5 keeps the limits its operator configured; afterwards the row is
// authoritative and only the panel changes it.
//
// A failure here never blocks startup: the panel simply shows no settings yet,
// and the next start tries again.
func seedCommerceSettings(
	ctx context.Context, logger *slog.Logger, operations *panelpg.Service, cfg config.Config,
) {
	seedCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if _, err := operations.EnsureCommerceSettings(seedCtx, panelpg.CommerceSettings{
		TopUp: panelpg.TopUpSettings{
			Enabled:          cfg.TopUp.Enabled,
			Currency:         cfg.DefaultCurrency,
			PresetsMinor:     cfg.TopUp.Presets,
			MinimumMinor:     cfg.TopUp.MinimumMinor,
			MaximumMinor:     cfg.TopUp.MaximumMinor,
			WindowSeconds:    int64(cfg.TopUp.Window.Seconds()),
			WindowLimitMinor: cfg.TopUp.WindowLimitMinor,
		},
		Subscriptions: panelpg.SubscriptionSettings{
			MultiEnabled:   cfg.Subscriptions.MultiEnabled,
			MaxPerCustomer: int32(cfg.Subscriptions.MaxPerCustomer),
		},
	}); err != nil {
		logger.Warn("commerce settings could not be seeded; the panel will show none yet", "error", err)
	}
}

// issueSetupTokenIfNeeded prints a one-time bootstrap token when an
// installation has no operator account yet.
//
// The token goes to the log rather than to an HTTP response, because at this
// point nothing can authenticate and an endpoint that handed it out would give
// it to anyone who asked. It is issued only while no operator exists, so a
// restart of an established installation prints nothing.
//
// A failure here never blocks startup: the API is still useful without the
// panel, and the operator can restart to try again.
func issueSetupTokenIfNeeded(logger *slog.Logger, service *adminauthpg.Service) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	state, err := service.BootstrapStatus(ctx)
	if err != nil {
		logger.Warn("could not determine admin setup state", "error", err)
		return
	}
	if !state.Required {
		return
	}

	token, err := service.IssueSetupToken(ctx)
	if err != nil {
		if errors.Is(err, adminauthpg.ErrBootstrapClosed) {
			return
		}
		logger.Warn("could not issue an admin setup token", "error", err)
		return
	}
	logger.Warn(
		"no administrator exists yet; redeem this one-time setup token at /admin/setup",
		"token", token,
		"expires_in", adminauthpg.SetupTokenTTL.String(),
	)
}
