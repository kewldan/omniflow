// Package accountshop is the customer web panel's digital-goods surface.
//
// It sells the same catalogue the bot sells, through the same order, promotion,
// and delivery pipeline, and it inherits that pipeline's two hard rules. A price
// is a quote with an expiry, because the provider's cost moves and the number on
// screen has to be the number charged. And a recipient is confirmed in a step of
// its own, because a mistyped username is unrecoverable the moment a gateway has
// sent the goods.
//
// The gateway that fronts Fragment honours no idempotency key, so a lost answer
// is genuinely ambiguous. This package never resolves that ambiguity by retrying
// or refunding: the delivery parks in the operator review queue and the panel
// says so. A shop screen that offered a "try again" button there would be
// offering to pay twice.
package accountshop

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/goods"
	"github.com/omniflow/omniflow/internal/paymentservice"
)

// Providers resolves a product's provider slug to its adapter. It is the same
// contract the bot's shop takes, so the two surfaces quote against one registry.
type Providers interface {
	Provider(ctx context.Context, slug string) (goods.Provider, error)
}

// The refusals the shop answers with.
//
// They are sentinels rather than messages because the panel's next move differs
// for each one, and a screen that cannot tell them apart has to guess. A stale
// quote is re-quoted; a moved price is shown again for a fresh confirmation; an
// unusable handle is corrected by the customer; and a shop with no working
// provider is not offered at all.
var (
	// ErrUnavailable reports an installation that cannot sell digital goods:
	// no provider registry, or none that this product's gateway resolves
	// through. It is not "sold out" and the panel must not render it as such.
	ErrUnavailable = errors.New("the digital goods shop is not available")
	// ErrQuoteExpired reports a purchase confirmed against a price that is no
	// longer honourable. A quote with no expiry at all is the same condition:
	// in both cases nothing was promised, and the remedy is to quote again.
	ErrQuoteExpired = errors.New("the quoted price has expired")
	// ErrPriceChanged reports a provider rate that moved while the customer was
	// deciding. It is deliberately distinct from an expired quote: the number on
	// the screen is still inside its window but is no longer the number that
	// would be charged, and charging the new one silently is the failure this
	// whole flow exists to prevent.
	ErrPriceChanged = errors.New("the price changed since it was quoted")
	// ErrPriceUnavailable reports a product whose provider publishes no cost and
	// for which the operator configured no price. There is nothing to charge,
	// and inventing a number would be charging one nobody chose.
	ErrPriceUnavailable = errors.New("this product cannot be priced right now")
	// ErrRecipientInvalid reports a handle that cannot be a Telegram username.
	ErrRecipientInvalid = errors.New("the recipient username is not usable")
	// ErrRecipientNotReviewed reports a purchase whose recipient did not come
	// from the review step. Delivery is irreversible once a gateway has sent the
	// goods, so a handle is confirmed in its normalised form before it is used,
	// never on the strength of a single submission.
	ErrRecipientNotReviewed = errors.New("the recipient has not been reviewed")
)

// Settings are the installation-wide values the shop needs.
type Settings struct {
	// Currency is the settlement currency the catalogue is priced in.
	Currency string
	// PublicURL is the base a provider returns the customer to.
	PublicURL string
}

// Service is the customer shop adapter.
type Service struct {
	pool      *pgxpool.Pool
	orders    *commercepg.Store
	payments  *paymentservice.Service
	providers Providers
	settings  Settings
	logger    *slog.Logger
	clock     func() time.Time
}

// Options configures a Service.
type Options struct {
	Settings Settings
	Logger   *slog.Logger
	Clock    func() time.Time
}

// New builds the adapter.
//
// A nil provider registry leaves the shop unavailable rather than empty, which
// is the honest state for an installation that sells no digital goods: an empty
// catalogue would read as "sold out" instead of "not offered here".
func New(
	pool *pgxpool.Pool, orders *commercepg.Store, payments *paymentservice.Service,
	providers Providers, options Options,
) (*Service, error) {
	if pool == nil {
		return nil, errors.New("a database pool is required")
	}
	if orders == nil {
		return nil, errors.New("a commerce store is required")
	}
	service := &Service{
		pool: pool, orders: orders, payments: payments, providers: providers,
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

func (service *Service) now() time.Time { return service.clock().UTC() }

// Enabled reports whether this installation can sell digital goods at all.
func (service *Service) Enabled() bool { return service != nil && service.providers != nil }

// provider resolves one product's adapter.
//
// A slug that does not resolve — disabled, uncredentialed, or built into no
// implementation this binary carries — is reported as the shop being
// unavailable rather than as a fault. Nothing has been submitted, nothing has
// been charged, and the honest thing to tell a customer is that this cannot be
// bought here at the moment.
func (service *Service) provider(ctx context.Context, slug string) (goods.Provider, error) {
	if !service.Enabled() {
		return nil, ErrUnavailable
	}
	adapter, err := service.providers.Provider(ctx, slug)
	if err != nil {
		// The slug is logged; the credential the registry opened is not, and
		// never becomes part of an error a customer could see.
		service.logger.Warn("digital goods provider is unusable", "provider", slug, "error", err)
		return nil, ErrUnavailable
	}
	return adapter, nil
}
