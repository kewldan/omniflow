package paymentservice

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/payments"
)

// ProviderOptions are the credentials an installation configured.
//
// An empty credential leaves its adapter unregistered rather than registering a
// broken one: an installation that does not sell through YooKassa should not
// have a YooKassa adapter that fails on first use.
type ProviderOptions struct {
	TelegramToken    string
	CryptoBotToken   string
	CryptoBotTestnet bool
	YooKassaShopID   string
	YooKassaSecret   string
	// Env reads the credentials of every adapter in payments.Builders. Nil
	// means the process environment.
	Env payments.Env
	// PublicURL is APP_PUBLIC_URL; adapters that send a callback URL per
	// payment derive it from here.
	PublicURL string
}

// ConfigureProviders builds the adapter set from an installation's credentials.
//
// It lives here rather than in `internal/payments` because the Stars adapter
// needs to resolve a payer from the database to refund, and the payments
// package deliberately has no database in it. Both the API and the worker call
// this, so the two processes cannot end up with different provider sets — which
// would show up as a renewal the worker cannot charge through a provider the
// customer paid with.
func ConfigureProviders(
	pool *pgxpool.Pool, options ProviderOptions,
) ([]payments.Provider, error) {
	// A zero-value Stars adapter still creates invoices and parses settlements;
	// it only loses the ability to refund. That is why it is always registered.
	stars := payments.Provider(&payments.TelegramStars{})
	if options.TelegramToken != "" {
		configured, err := payments.NewTelegramStars(options.TelegramToken, NewStarsPayerResolver(pool))
		if err != nil {
			return nil, err
		}
		stars = configured
	}
	providers := []payments.Provider{payments.Manual{}, stars}
	if options.CryptoBotToken != "" {
		provider, err := payments.NewCryptoBot(options.CryptoBotToken, options.CryptoBotTestnet)
		if err != nil {
			return nil, err
		}
		assets, lifetime, err := payments.CryptoBotOptionsFromEnv(payments.Settings{Env: options.Env})
		if err != nil {
			return nil, fmt.Errorf("configure cryptobot: %w", err)
		}
		providers = append(providers, provider.WithInvoiceOptions(assets, lifetime))
	}
	if options.YooKassaShopID != "" || options.YooKassaSecret != "" {
		provider, err := payments.NewYooKassa(options.YooKassaShopID, options.YooKassaSecret)
		if err != nil {
			return nil, err
		}
		receipt, err := payments.YooKassaReceiptFromEnv(payments.Settings{Env: options.Env})
		if err != nil {
			return nil, fmt.Errorf("configure yookassa: %w", err)
		}
		env := payments.Settings{Env: options.Env}
		providers = append(providers, provider.WithReceipt(receipt).WithModes(
			env.Bool("APP_YOOKASSA_SBP_ENABLED", false), env.Bool("APP_YOOKASSA_TEST_MODE", false),
		))
	}
	settings := payments.Settings{Env: options.Env, PublicURL: options.PublicURL}
	for _, builder := range payments.Builders {
		provider, err := builder.Build(settings)
		if errors.Is(err, payments.ErrNotConfigured) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("configure %s: %w", builder.Slug, err)
		}
		providers = append(providers, provider)
	}
	return providers, nil
}

// Index keys adapters by name for the callers that need lookup rather than
// iteration.
func Index(providers []payments.Provider) map[string]payments.Provider {
	index := make(map[string]payments.Provider, len(providers))
	for _, provider := range providers {
		index[provider.Name()] = provider
	}
	return index
}
