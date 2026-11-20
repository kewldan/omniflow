package payments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/omniflow/omniflow/internal/commerce"
)

// StarsCustomerResolver maps a Telegram payment charge identifier back to the
// Telegram user that paid it. refundStarPayment requires both, and Omniflow
// stores the payer only as non-secret receipt metadata on the payment intent.
type StarsCustomerResolver func(ctx context.Context, chargeID string) (int64, error)

// TelegramStars settles in Telegram Stars (XTR) through the authenticated Bot
// API. Updates never arrive on the public provider-webhook route: they are read
// from the bot's own authenticated update stream, so Capabilities reports no
// webhook support and paymentservice exposes a separate settlement entry point.
type TelegramStars struct {
	token    string
	resolve  StarsCustomerResolver
	http     *http.Client
	endpoint string
	// convertible, when set, reports whether Stars may pay an order priced
	// in another currency at the operator's Stars rate. The Star count is
	// fixed per order outside the adapter (fxrate.QuoteOrder), so the intent
	// keeps the order's own currency and amount.
	convertible func(currency string) bool
}

// WithConversion lets the adapter be offered for orders in a currency the
// check accepts.
func (provider *TelegramStars) WithConversion(convertible func(currency string) bool) *TelegramStars {
	provider.convertible = convertible
	return provider
}

// NewTelegramStars builds an adapter that can also refund. A zero-value
// TelegramStars remains valid for invoice creation and settlement parsing when
// the operator has not granted the adapter a bot token.
func NewTelegramStars(token string, resolve StarsCustomerResolver) (*TelegramStars, error) {
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("Telegram bot token is required for the Stars adapter")
	}
	return &TelegramStars{token: token, resolve: resolve, http: tracedClient(15 * time.Second), endpoint: "https://api.telegram.org"}, nil
}

func (provider *TelegramStars) Name() string { return "telegram_stars" }

func (provider *TelegramStars) Capabilities() Capabilities {
	// Stars refunds are full-amount only and require the bot token plus a payer
	// lookup. Polling and webhooks stay off: settlement is authenticated by the
	// Bot API update stream, not by an inbound HTTP request.
	return Capabilities{Refunds: provider != nil && provider.token != "" && provider.resolve != nil}
}

// SupportsCurrency reports that Stars settle in XTR, and in a currency the
// operator converts at a Stars rate.
func (provider *TelegramStars) SupportsCurrency(currency string) bool {
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if currency == "XTR" {
		return true
	}
	return provider != nil && provider.convertible != nil && provider.convertible(currency)
}

func (provider *TelegramStars) Create(_ context.Context, request CreateRequest) (Intent, error) {
	if request.Amount.Amount <= 0 || !provider.SupportsCurrency(request.Amount.Currency) {
		return Intent{}, commerce.ErrCurrencyMismatch
	}
	if request.Amount.Currency != "XTR" {
		// The invoice is sent by the bot at the order's fixed Stars quote.
		return Intent{Status: "requires_action", Amount: request.Amount, Metadata: map[string]string{
			"currency": request.Amount.Currency, "payload": request.OrderID, "converted": "true",
		}}, nil
	}
	return Intent{
		ProviderReference: "",
		Status:            "requires_action",
		Amount:            request.Amount,
		Metadata: map[string]string{
			"currency": "XTR", "payload": request.OrderID,
			"stars": strconv.FormatInt(request.Amount.Amount, 10),
		},
	}, nil
}

func (provider *TelegramStars) Poll(context.Context, string) (Intent, error) {
	return Intent{}, ErrUnsupported
}

func (provider *TelegramStars) Refund(ctx context.Context, request RefundRequest) (Refund, error) {
	if provider == nil || provider.token == "" || provider.resolve == nil {
		return Refund{}, ErrUnsupported
	}
	// refundStarPayment returns the whole charge whatever currency the order
	// was priced in, so a converted payment refunds the same way.
	if request.Amount.Currency != "XTR" && !provider.SupportsCurrency(request.Amount.Currency) {
		return Refund{}, commerce.ErrCurrencyMismatch
	}
	telegramUserID, err := provider.resolve(ctx, request.PaymentReference)
	if err != nil {
		return Refund{}, err
	}
	if telegramUserID <= 0 {
		return Refund{}, errors.New("Telegram Stars refund requires the paying Telegram account")
	}
	var response struct {
		OK          bool   `json:"ok"`
		Result      bool   `json:"result"`
		Description string `json:"description"`
	}
	body := map[string]any{"user_id": telegramUserID, "telegram_payment_charge_id": request.PaymentReference}
	if err := doJSON(ctx, provider.http, http.MethodPost, provider.endpoint+"/bot"+provider.token+"/refundStarPayment", make(http.Header), body, &response); err != nil {
		return Refund{}, err
	}
	if !response.OK || !response.Result {
		return Refund{}, ErrProviderResponse
	}
	// refundStarPayment always returns the whole charge, so a partial request
	// must never be reported as settled.
	return Refund{ProviderReference: request.PaymentReference, Status: "succeeded", Amount: request.Amount}, nil
}

func (provider *TelegramStars) VerifyWebhook(_ http.Header, body []byte) (WebhookEvent, error) {
	// Telegram payment updates arrive through the authenticated Bot API update
	// stream. Only the minimal successful_payment projection is accepted here.
	var update struct {
		UpdateID int64 `json:"update_id"`
		Message  struct {
			Date              int64 `json:"date"`
			SuccessfulPayment *struct {
				Currency         string `json:"currency"`
				TotalAmount      int64  `json:"total_amount"`
				InvoicePayload   string `json:"invoice_payload"`
				TelegramChargeID string `json:"telegram_payment_charge_id"`
			} `json:"successful_payment"`
		} `json:"message"`
	}
	if err := json.Unmarshal(body, &update); err != nil || update.Message.SuccessfulPayment == nil {
		return WebhookEvent{}, ErrProviderResponse
	}
	payment := update.Message.SuccessfulPayment
	if payment.Currency != "XTR" || payment.TelegramChargeID == "" || payment.InvoicePayload == "" {
		return WebhookEvent{}, errors.New("invalid Telegram Stars payment")
	}
	return WebhookEvent{ID: strconv.FormatInt(update.UpdateID, 10), Type: "payment.succeeded", ProviderReference: payment.TelegramChargeID, MerchantReference: payment.InvoicePayload, Status: "succeeded", Amount: commerce.Money{Amount: payment.TotalAmount, Currency: "XTR"}, OccurredAt: time.Unix(update.Message.Date, 0).UTC()}, nil
}

var _ Provider = (*TelegramStars)(nil)

// Probe verifies the bot token with getMe.
//
// A Stars adapter without a token is still valid for invoice creation and
// settlement parsing, so it reports the probe as unsupported rather than
// failing: there is nothing to authenticate.
func (provider *TelegramStars) Probe(ctx context.Context) error {
	if provider == nil || provider.token == "" {
		return ErrUnsupported
	}
	var response struct {
		Ok          bool   `json:"ok"`
		Description string `json:"description"`
	}
	endpoint := provider.endpoint + "/bot" + provider.token + "/getMe"
	if err := doJSON(ctx, provider.http, http.MethodGet, endpoint, nil, nil, &response); err != nil {
		return err
	}
	if !response.Ok {
		return fmt.Errorf("%w: %s", ErrProviderResponse, response.Description)
	}
	return nil
}
