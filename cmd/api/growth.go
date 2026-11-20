package main

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/config"
	"github.com/omniflow/omniflow/internal/emailpg"
	"github.com/omniflow/omniflow/internal/engagement"
	"github.com/omniflow/omniflow/internal/fxrate"
	"github.com/omniflow/omniflow/internal/guestpurchase"
	apihttp "github.com/omniflow/omniflow/internal/httpapi"
	"github.com/omniflow/omniflow/internal/mailer"
	"github.com/omniflow/omniflow/internal/paymentservice"
	"github.com/omniflow/omniflow/internal/referralpayout"
	"github.com/omniflow/omniflow/internal/selfemployed"
)

// buildGrowth wires the Bedolaga-parity services the api serves: the e-mail
// queue (the panel's status and test, the one-click unsubscribe route), and
// the services added beside it.
//
// The api never delivers queued mail — the worker does — so the queue is
// built without a sender here; it only enqueues and reads.
func buildGrowth(
	logger *slog.Logger, cfg config.Config, pool *pgxpool.Pool,
	commerceStore *commercepg.Store, paymentService *paymentservice.Service,
) (apihttp.GrowthServices, error) {
	mail, err := config.LoadMail()
	if err != nil {
		return apihttp.GrowthServices{}, err
	}
	email, err := emailpg.New(pool, emailpg.Options{
		DataKey: cfg.DataEncryptionKey, PublicURL: cfg.PublicURL,
		ReceiptsEnabled:       mail.ReceiptsEnabled,
		MarketingFrequencyCap: mail.MarketingFrequencyCap, MarketingWindow: mail.MarketingWindow,
		Disposable: mailer.NewDisposableChecker(mail.BlockedDomains), Logger: logger,
	})
	if err != nil {
		return apihttp.GrowthServices{}, err
	}
	services := apihttp.GrowthServices{Email: email, EmailSending: mail.Configured()}
	if services.Withdrawals, err = referralpayout.New(pool, cfg.DataEncryptionKey, email, logger); err != nil {
		return apihttp.GrowthServices{}, err
	}
	if services.SelfEmployed, err = selfemployed.New(pool, selfemployed.Options{DataKey: cfg.DataEncryptionKey, Logger: logger}); err != nil {
		return apihttp.GrowthServices{}, err
	}
	services.Rates = fxrate.New(pool, fxrate.Options{})
	services.Engagement = engagement.New(pool, commerceStore)
	services.Languages = func(ctx context.Context) []string { return offeredLanguages(ctx, pool) }
	// Guest purchases deliver their activation code by e-mail, so they are
	// offered only when a transport is configured and a public address exists
	// for the activation link.
	if mail.Configured() && cfg.PublicURL != "" {
		if services.Guest, err = guestpurchase.New(pool, commerceStore, paymentService, email, guestpurchase.Options{
			DataKey: cfg.DataEncryptionKey, PublicURL: cfg.PublicURL, Currency: cfg.DefaultCurrency,
			Disposable: mailer.NewDisposableChecker(mail.BlockedDomains), Logger: logger,
		}); err != nil {
			return apihttp.GrowthServices{}, err
		}
		services.GuestPayments, services.GuestCurrency = paymentService, cfg.DefaultCurrency
	}
	return services, nil
}

// supportedLanguages is every language the web panel and the bot have a
// catalogue for.
var supportedLanguages = []string{"ru", "en", "uk", "zh", "fa"}

// offeredLanguages reads the operator's choice from the bot section of the
// installation settings. Nothing chosen offers Russian and English, as before
// the extra languages existed.
func offeredLanguages(ctx context.Context, pool *pgxpool.Pool) []string {
	var chosen []string
	_ = pool.QueryRow(ctx, `SELECT COALESCE(ARRAY(SELECT jsonb_array_elements_text(document->'languages')), '{}')
		FROM installation_settings WHERE section = 'bot'`).Scan(&chosen)
	offered := []string{}
	for _, code := range supportedLanguages {
		for _, picked := range chosen {
			if picked == code {
				offered = append(offered, code)
			}
		}
	}
	if len(offered) == 0 {
		return []string{"ru", "en"}
	}
	return offered
}
