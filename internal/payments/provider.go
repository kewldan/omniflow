package payments

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/omniflow/omniflow/internal/commerce"
)

var (
	ErrInvalidSignature = errors.New("invalid webhook signature")
	ErrProviderResponse = errors.New("invalid provider response")
	ErrUnsupported      = errors.New("provider capability is not supported")
)

type Capabilities struct {
	Refunds       bool `json:"refunds"`
	PartialRefund bool `json:"partialRefund"`
	Recurring     bool `json:"recurring"`
	Webhooks      bool `json:"webhooks"`
	Polling       bool `json:"polling"`
}

type CreateRequest struct {
	OrderID        string
	IdempotencyKey string
	Amount         commerce.Money
	Description    string
	ReturnURL      string
	Metadata       map[string]string
}

type Intent struct {
	ProviderReference string
	Status            string
	CheckoutURL       string
	Amount            commerce.Money
	ExpiresAt         *time.Time
	Metadata          map[string]string
}

type RefundRequest struct {
	PaymentReference string
	IdempotencyKey   string
	Amount           commerce.Money
	Reason           string
}

type Refund struct {
	ProviderReference string
	Status            string
	Amount            commerce.Money
}

type WebhookEvent struct {
	ID                string
	Type              string
	ProviderReference string
	MerchantReference string
	Status            string
	Amount            commerce.Money
	OccurredAt        time.Time
}

type Provider interface {
	Name() string
	Capabilities() Capabilities
	Create(context.Context, CreateRequest) (Intent, error)
	Poll(context.Context, string) (Intent, error)
	Refund(context.Context, RefundRequest) (Refund, error)
	VerifyWebhook(http.Header, []byte) (WebhookEvent, error)
}

// SavedChargeRequest asks a provider to charge a token it issued earlier.
//
// The token is opaque and provider-scoped. Omniflow never holds a card number,
// an expiry, or a verification value: the whole instrument stays with the
// acquirer, and this hands back the reference it gave us.
type SavedChargeRequest struct {
	OrderID        string
	IdempotencyKey string
	MethodToken    string
	Amount         commerce.Money
	Description    string
}

// RecurringCharger is implemented by adapters that can charge a stored method
// without the customer present.
//
// It is separate from Create because the two are different acts: Create opens a
// checkout the customer completes, while this moves money on consent given
// earlier. Keeping them apart means no code path can accidentally charge a
// saved method while trying to start a checkout.
type RecurringCharger interface {
	ChargeSaved(context.Context, SavedChargeRequest) (Intent, error)
}

type Recoverer interface {
	Recover(context.Context, string) (Intent, bool, error)
}

// CurrencySupport is implemented by adapters that settle only some currencies.
// An adapter that does not implement it accepts whatever the order carries.
type CurrencySupport interface {
	SupportsCurrency(string) bool
}

// SupportsCurrency reports whether an adapter can settle a currency. Provider
// selection uses it so a customer is never offered an incompatible adapter.
func SupportsCurrency(provider Provider, currency string) bool {
	if support, ok := provider.(CurrencySupport); ok {
		return support.SupportsCurrency(currency)
	}
	return true
}

// Options describes the configured adapters for customer-facing selection.
func Options(providers map[string]Provider, order map[string]int) []commerce.PaymentOption {
	options := make([]commerce.PaymentOption, 0, len(providers))
	for name, provider := range providers {
		option := commerce.PaymentOption{Provider: name, Enabled: true, Recurring: provider.Capabilities().Recurring, Order: order[name]}
		if support, ok := provider.(CurrencySupport); ok {
			option.Currencies = supportedCurrencies(support)
		}
		options = append(options, option)
	}
	return options
}

// settlementCurrencies is the closed set of currencies Omniflow prices plans in.
// It exists so provider selection can enumerate compatibility without asking an
// adapter to publish a list it may not have.
var settlementCurrencies = []string{"XTR", "RUB", "USD", "EUR", "GBP", "AED", "AMD", "AUD", "AZN", "BYN", "CAD", "CHF", "CNY", "GEL", "INR", "KZT", "MDL", "NOK", "NZD", "PLN", "SEK", "TRY", "UAH", "UZS", "JPY", "KRW"}

func supportedCurrencies(support CurrencySupport) []string {
	supported := make([]string, 0, len(settlementCurrencies))
	for _, currency := range settlementCurrencies {
		if support.SupportsCurrency(currency) {
			supported = append(supported, currency)
		}
	}
	return supported
}

type Manual struct{}

func (Manual) Name() string { return "manual" }
func (Manual) Capabilities() Capabilities {
	return Capabilities{Refunds: true, PartialRefund: true}
}
func (Manual) Create(_ context.Context, request CreateRequest) (Intent, error) {
	return Intent{ProviderReference: request.OrderID, Status: "requires_action", Amount: request.Amount}, nil
}
func (Manual) Poll(context.Context, string) (Intent, error) { return Intent{}, ErrUnsupported }
func (Manual) Refund(_ context.Context, request RefundRequest) (Refund, error) {
	return Refund{ProviderReference: request.IdempotencyKey, Status: "pending", Amount: request.Amount}, nil
}
func (Manual) VerifyWebhook(http.Header, []byte) (WebhookEvent, error) {
	return WebhookEvent{}, ErrUnsupported
}

// ConnectionProbe is implemented by adapters that can verify their credentials
// without moving money.
//
// The probe is a read the provider's own API already offers — listing recent
// payments, describing the authenticated application — chosen so that a failed
// probe means "these credentials do not work" and never "a customer was
// charged". An adapter that cannot answer that question without a side effect
// does not implement this, and the panel reports the credentials as untested
// rather than inventing a result.
type ConnectionProbe interface {
	Probe(context.Context) error
}

// Probe verifies an adapter's credentials where it can.
//
// ErrUnsupported separates "the credentials are wrong" from "this adapter has
// no way to tell", which the panel renders differently: the first is a
// misconfiguration an operator must fix, the second is a limitation.
func Probe(ctx context.Context, provider Provider) error {
	probe, ok := provider.(ConnectionProbe)
	if !ok {
		return ErrUnsupported
	}
	return probe.Probe(ctx)
}
