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
