// Package app is the composition root the three processes share.
//
// Each entrypoint still builds its own process — the api its HTTP surfaces, the
// bot its Telegram client, the worker its job runners — but the part they have
// in common, the commerce core that takes money and queues provisioning, is
// built here once. It used to be written out three times in three mains, and
// the copies drifted: the worker enforced no top-up limits, the bot assembled
// its payment adapters by hand and recorded no payment metrics, and nothing
// stopped a caller from building the store with no way to queue fulfillment.
// The integration harness builds the core through the same constructor, so a
// test exercises the wiring production runs rather than a hand-made variant.
//
// Construction stays explicit: plain constructors, required dependencies
// checked and reported as errors, no container and no reflection.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/config"
	"github.com/omniflow/omniflow/internal/fulfillment"
	"github.com/omniflow/omniflow/internal/jobs"
	"github.com/omniflow/omniflow/internal/payments"
	"github.com/omniflow/omniflow/internal/paymentservice"
	"github.com/omniflow/omniflow/internal/platform"
)

// OpenPool opens a process's traced PostgreSQL pool with an explicit size and
// checks that the database answers, so a wrong URL fails at start-up rather
// than on the first request.
func OpenPool(ctx context.Context, databaseURL string, maxConns int32) (*pgxpool.Pool, error) {
	if databaseURL == "" {
		return nil, errors.New("APP_DATABASE_URL is required")
	}
	if maxConns < 1 {
		return nil, errors.New("the database pool needs at least one connection")
	}
	pool, err := platform.TracedPool(ctx, databaseURL, maxConns)
	if err != nil {
		return nil, err
	}
	if err = pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}

// OpenWorkerPool opens the worker's pool and refuses a size that cannot carry
// the worker's own job concurrency. A pool smaller than the number of jobs River
// may run at once makes jobs queue for connections behind each other and
// behind River's own bookkeeping, which looks like a hung worker.
func OpenWorkerPool(ctx context.Context, databaseURL string, maxConns int32) (*pgxpool.Pool, error) {
	if minimum := int32(jobs.MaxWorkers()) + 4; maxConns < minimum {
		return nil, fmt.Errorf(
			"APP_DATABASE_MAX_CONNS is %d for the worker; it runs up to %d jobs at once and needs at least %d",
			maxConns, jobs.MaxWorkers(), minimum,
		)
	}
	return OpenPool(ctx, databaseURL, maxConns)
}

// FulfillmentEnqueuer is the hook commercepg calls inside a settling
// transaction: it inserts the River job that provisions the operation in that
// same transaction, so the job exists if and only if the settlement commits.
func FulfillmentEnqueuer(client *jobs.Client) (commercepg.EnqueueFulfillment, error) {
	if client == nil {
		return nil, errors.New("a River client is required to enqueue fulfillment")
	}
	return func(ctx context.Context, tx pgx.Tx, operationID string) error {
		_, err := client.InsertTx(ctx, tx, fulfillment.JobArgs{OperationID: operationID}, fulfillment.InsertOpts())
		return err
	}, nil
}

// CommerceCore is the part of every process that settles money: the order
// store, the configured payment adapters, the payment service over them, and
// the fulfillment service that queues provisioning.
type CommerceCore struct {
	Store       *commercepg.Store
	Providers   []payments.Provider
	Payments    *paymentservice.Service
	Fulfillment *fulfillment.Service
	Jobs        *jobs.Client
}

// CommerceOptions are the optional collaborators of the commerce core.
type CommerceOptions struct {
	Logger *slog.Logger
	// Metrics records payment and webhook outcomes. Nil is allowed only in a
	// process whose operator turned metrics off.
	Metrics *platform.Metrics
}

// NewCommerceCore builds the commerce core every process shares.
//
// The pool and the River client are required. The client may be insert-only
// (the api and the bot) or the worker's full client; either way settlements
// insert their fulfillment job in the paying transaction.
func NewCommerceCore(
	cfg config.CommerceConfig, pool *pgxpool.Pool, jobsClient *jobs.Client, options CommerceOptions,
) (*CommerceCore, error) {
	if pool == nil {
		return nil, errors.New("commerce: a database pool is required")
	}
	enqueue, err := FulfillmentEnqueuer(jobsClient)
	if err != nil {
		return nil, fmt.Errorf("commerce: %w", err)
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	store, err := commercepg.New(pool, enqueue, commercepg.Options{
		Subscriptions: cfg.Subscriptions.Policy(),
		TopUp:         cfg.TopUp.Limits(),
		Logger:        logger,
	})
	if err != nil {
		return nil, err
	}
	providers, err := paymentservice.ConfigureProviders(pool, paymentservice.ProviderOptions{
		TelegramToken:    cfg.Payments.TelegramToken,
		CryptoBotToken:   cfg.Payments.CryptoBotToken,
		CryptoBotTestnet: cfg.Payments.CryptoBotTestnet,
		YooKassaShopID:   cfg.Payments.YooKassaShopID,
		YooKassaSecret:   cfg.Payments.YooKassaSecret,
	})
	if err != nil {
		return nil, fmt.Errorf("configure payment providers: %w", err)
	}
	return &CommerceCore{
		Store:       store,
		Providers:   providers,
		Payments:    paymentservice.New(pool, store, providers...).WithMetrics(options.Metrics),
		Fulfillment: fulfillment.NewService(pool, jobsClient),
		Jobs:        jobsClient,
	}, nil
}
