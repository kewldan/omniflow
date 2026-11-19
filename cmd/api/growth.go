package main

import (
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/config"
	"github.com/omniflow/omniflow/internal/emailpg"
	apihttp "github.com/omniflow/omniflow/internal/httpapi"
	"github.com/omniflow/omniflow/internal/mailer"
	"github.com/omniflow/omniflow/internal/paymentservice"
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
	_, _ = commerceStore, paymentService
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
	return apihttp.GrowthServices{Email: email, EmailSending: mail.Configured()}, nil
}
