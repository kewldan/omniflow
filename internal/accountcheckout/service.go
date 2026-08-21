// Package accountcheckout is the customer commerce surface, shared by the web
// panel and the Telegram bot.
//
// It answers what the checkout screens ask — which plans may I buy, what would
// this cost me, what did I already order — and performs the mutations that
// follow: opening a checkout, editing it, turning it into an order, and starting
// a payment against that order.
//
// Nothing here decides a price, a discount, or an eligibility. Those rules live
// in internal/commerce and internal/commercepg. This package projects the same
// records onto a shape a screen can render, so a quote shown in a browser and a
// quote shown in a chat come from one PreviewOrder call against one plan
// version. That is not a coincidence to be maintained by review: internal/botapp
// delegates to this package rather than keeping a second implementation, because
// two implementations of a purchase eventually price the same order differently
// and only one of the two customers finds out.
//
// The checkout session itself is shared between the surfaces. A customer has at
// most one open checkout, so starting one in the browser supersedes one left
// open in the bot rather than running beside it. Two half-finished purchases for
// one person is a state neither surface could explain.
package accountcheckout

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/paymentservice"
)

// Settings are the installation-wide values the checkout needs. They mirror the
// bot's CommerceSettings so both surfaces price and link the same way.
type Settings struct {
	// Currency is the settlement currency plans are compared in.
	Currency string
	// PublicURL is the base a provider returns the customer to.
	PublicURL string
	// TermsURL is shown beside the confirmation control.
	TermsURL string
	// RecoveryWindow is how long an expired subscription keeps its one-tap
	// recovery offer.
	RecoveryWindow time.Duration
	// MinimumTrialAccountAge refuses trials from accounts created moments ago.
	MinimumTrialAccountAge time.Duration
	// MultiSubscription mirrors the installation switch, so a panel with one
	// possible subscription never renders a target picker.
	MultiSubscription bool
}

// Service is the customer checkout adapter.
type Service struct {
	store    *Store
	orders   *commercepg.Store
	payments *paymentservice.Service
	settings Settings
	logger   *slog.Logger
	clock    func() time.Time

	// botUsername resolves the bot's @name for the Telegram Stars handoff.
	// Nil leaves a Stars payment with no link; see stars.go.
	botUsername func(context.Context) string
}

// Options configures a Service.
type Options struct {
	Settings Settings
	Logger   *slog.Logger
	Clock    func() time.Time
}

// New builds the adapter.
//
// A nil payment service is allowed and leaves the panel able to price and
// compare plans while reporting that nothing can settle them yet, which is what
// an installation with no configured provider actually is.
func New(
	pool *pgxpool.Pool, orders *commercepg.Store,
	payments *paymentservice.Service, options Options,
) (*Service, error) {
	if pool == nil {
		return nil, errors.New("a database pool is required")
	}
	if orders == nil {
		return nil, errors.New("a commerce store is required")
	}
	service := &Service{
		store: NewStore(pool), orders: orders, payments: payments,
		settings: options.Settings, logger: options.Logger, clock: options.Clock,
	}
	if service.logger == nil {
		service.logger = slog.Default()
	}
	if service.clock == nil {
		service.clock = time.Now
	}
	return service, nil
}

// Settings exposes the configured values the transport needs to render terms
// and provider handoff links.
func (service *Service) Settings() Settings { return service.settings }

// Store exposes the persistence half, for callers that need the shared checkout
// session and order projection without the pricing orchestration around them.
func (service *Service) Store() *Store { return service.store }
