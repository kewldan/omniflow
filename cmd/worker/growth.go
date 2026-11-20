package main

import (
	"errors"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/config"
	"github.com/omniflow/omniflow/internal/emailpg"
	"github.com/omniflow/omniflow/internal/jobs"
	"github.com/omniflow/omniflow/internal/mailer"
	"github.com/omniflow/omniflow/internal/selfemployed"
)

// emailInterval is how often the mail pass runs. A receipt or a sign-in
// follow-up waits at most this long before its first attempt.
const emailInterval = 20 * time.Second

// growthTasks lists the periodic passes of the Bedolaga-parity services.
//
// Each is optional and starts only when what it needs is configured, so a
// worker with no e-mail transport simply has no mail pass rather than one
// that fails every twenty seconds.
func growthTasks(logger *slog.Logger, cfg config.WorkerConfig, pool *pgxpool.Pool) ([]jobs.Task, error) {
	var tasks []jobs.Task
	mail, err := config.LoadMail()
	if err != nil {
		return nil, err
	}
	sender, err := mailer.New(mailer.Config{
		Transport: mail.Transport, From: mail.From, ResendAPIKey: mail.ResendAPIKey,
		SMTPHost: mail.SMTPHost, SMTPPort: mail.SMTPPort, SMTPUsername: mail.SMTPUsername,
		SMTPPassword: mail.SMTPPassword, SMTPSecurity: mail.SMTPSecurity,
	})
	switch {
	case errors.Is(err, mailer.ErrNotConfigured):
		logger.Info("e-mail delivery disabled", "reason", "no e-mail transport is configured")
	case err != nil:
		return nil, err
	case len(cfg.DataEncryptionKey) != 32:
		logger.Warn("e-mail delivery disabled", "reason", "APP_DATA_ENCRYPTION_KEY is not set")
	default:
		email, emailErr := emailpg.New(pool, emailpg.Options{
			Sender: sender, DataKey: cfg.DataEncryptionKey, PublicURL: os.Getenv("APP_PUBLIC_URL"),
			ReceiptsEnabled:       mail.ReceiptsEnabled,
			MarketingFrequencyCap: mail.MarketingFrequencyCap, MarketingWindow: mail.MarketingWindow,
			Disposable: mailer.NewDisposableChecker(mail.BlockedDomains), Logger: logger,
		})
		if emailErr != nil {
			return nil, emailErr
		}
		tasks = append(tasks, jobs.Task{Name: "email_delivery", Interval: emailInterval, Timeout: 2 * time.Minute, Run: email.Pass})
	}
	// Self-employed receipts. The pass does nothing until an operator switches
	// the integration on in the panel, so it is registered whenever the
	// password can be unsealed.
	if len(cfg.DataEncryptionKey) == 32 {
		receipts, err := selfemployed.New(pool, selfemployed.Options{DataKey: cfg.DataEncryptionKey, Logger: logger})
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, jobs.Task{Name: "self_employed_receipts", Interval: time.Minute, Timeout: 5 * time.Minute, Run: receipts.Pass})
	}
	return tasks, nil
}
