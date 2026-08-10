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
