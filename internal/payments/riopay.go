package payments

import (
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/omniflow/omniflow/internal/commerce"
)

// RioPay settles through RioPay (api.riopay.online, API v2.0.1).
//
// Orders are created with the API token in the X-Api-Token header and carry
// Omniflow's order ID as `externalId`. Notifications are the order object,
// signed with HMAC-SHA512 over the raw body in X-Signature, keyed with the
// webhook secret or, when none is configured, the API token.
type RioPay struct {
	token         string
	webhookSecret string
	currency      string
	exponent      int
	successURL    string
	baseURL       string
	http          *http.Client
	clock         func() time.Time
}

const riopayAPIBaseURL = "https://api.riopay.online"

func buildRioPay(settings Settings) (Provider, error) {
	token := settings.String("APP_RIOPAY_API_TOKEN")
	secret := settings.String("APP_RIOPAY_WEBHOOK_SECRET")
	if token == "" {
		if secret != "" {
			return nil, errors.New("RioPay is partially configured: APP_RIOPAY_API_TOKEN must also be set")
		}
		return nil, ErrNotConfigured
	}
	if secret == "" {
		secret = token
	}
	currency := strings.ToUpper(settings.String("APP_RIOPAY_CURRENCY"))
	if currency == "" {
		currency = "RUB"
	}
	exponent, err := currencyExponent(currency)
	if err != nil {
		return nil, fmt.Errorf("RioPay currency %q is not supported", currency)
	}
	return &RioPay{
		token: token, webhookSecret: secret, currency: currency, exponent: exponent,
		successURL: settings.String("APP_RIOPAY_SUCCESS_URL"), baseURL: riopayAPIBaseURL,
		http: tracedClient(30 * time.Second), clock: time.Now,
	}, nil
}

func (provider *RioPay) Name() string { return "riopay" }

func (provider *RioPay) Capabilities() Capabilities {
	return Capabilities{Webhooks: true, Polling: true}
}

// SupportsCurrency reports the single currency this account is configured to take.
func (provider *RioPay) SupportsCurrency(currency string) bool {
	return strings.ToUpper(strings.TrimSpace(currency)) == provider.currency
}

// riopayOrder is RioPay's OrderData, returned on create and lookup and posted
// as the notification body.
type riopayOrder struct {
	ID          string      `json:"id"`
	ExternalID  string      `json:"externalId"`
	Status      string      `json:"status"`
	PaymentLink string      `json:"paymentLink"`
	Amount      json.Number `json:"amount"`
	Currency    string      `json:"currency"`
}

func (provider *RioPay) Create(ctx context.Context, request CreateRequest) (Intent, error) {
	if !provider.SupportsCurrency(request.Amount.Currency) {
		return Intent{}, commerce.ErrCurrencyMismatch
	}
	if request.Amount.Amount <= 0 {
		return Intent{}, errors.New("RioPay amount must be positive")
	}
	purpose := request.Description
	if purpose == "" {
		purpose = request.OrderID
	}
	body := map[string]string{
		"amount":     formatMinor(request.Amount.Amount, provider.exponent),
		"externalId": request.OrderID,
		"purpose":    purpose,
	}
	successURL := request.ReturnURL
	if successURL == "" {
		successURL = provider.successURL
	}
	if successURL != "" {
		body["successUrl"] = successURL
	}
	var order riopayOrder
	if err := doJSON(ctx, provider.http, http.MethodPost, provider.baseURL+"/v1/orders", provider.headers(), body, &order); err != nil {
		return Intent{}, err
	}
	if order.ID == "" || order.PaymentLink == "" {
		return Intent{}, fmt.Errorf("%w: RioPay returned no order link", ErrProviderResponse)
	}
	intent, err := provider.intent(order)
	if err != nil {
		return Intent{}, err
	}
	if intent.Amount.Currency == "" {
		intent.Amount = request.Amount
	}
	return intent, nil
}

func (provider *RioPay) Poll(ctx context.Context, reference string) (Intent, error) {
	var order riopayOrder
	if err := doJSON(ctx, provider.http, http.MethodGet, provider.baseURL+"/v1/orders/"+url.PathEscape(reference), provider.headers(), nil, &order); err != nil {
		return Intent{}, err
	}
	if order.ID == "" {
		return Intent{}, fmt.Errorf("%w: RioPay returned no order", ErrProviderResponse)
	}
	return provider.intent(order)
}

func (provider *RioPay) Refund(context.Context, RefundRequest) (Refund, error) {
	return Refund{}, ErrUnsupported
}

func (provider *RioPay) VerifyWebhook(headers http.Header, body []byte) (WebhookEvent, error) {
	provided, err := hex.DecodeString(strings.TrimSpace(headers.Get("X-Signature")))
	if err != nil || len(provided) == 0 {
		return WebhookEvent{}, ErrInvalidSignature
	}
	mac := hmac.New(sha512.New, []byte(provider.webhookSecret))
	_, _ = mac.Write(body)
	if !hmac.Equal(provided, mac.Sum(nil)) {
		return WebhookEvent{}, ErrInvalidSignature
	}
	var order riopayOrder
	if err := json.Unmarshal(body, &order); err != nil || order.ID == "" || order.Status == "" {
		return WebhookEvent{}, ErrProviderResponse
	}
	intent, err := provider.intent(order)
	if err != nil {
		return WebhookEvent{}, err
	}
	return WebhookEvent{
		ID: order.Status + ":" + order.ID, Type: "order", ProviderReference: order.ID,
		MerchantReference: order.ExternalID, Status: intent.Status, Amount: intent.Amount,
		OccurredAt: provider.clock().UTC(),
	}, nil
}

// WebhookAck answers as Bedolaga did.
func (provider *RioPay) WebhookAck(string) (int, string, []byte) {
	return http.StatusOK, "application/json", []byte(`{"status":"ok"}`)
}

func (provider *RioPay) headers() http.Header {
	headers := make(http.Header)
	headers.Set("X-Api-Token", provider.token)
	return headers
}

func (provider *RioPay) intent(order riopayOrder) (Intent, error) {
	intent := Intent{ProviderReference: order.ID, Status: riopayStatus(order.Status), CheckoutURL: order.PaymentLink}
	if order.Amount == "" {
		if intent.Status == "succeeded" {
			return Intent{}, fmt.Errorf("%w: completed RioPay order carries no amount", ErrProviderResponse)
		}
		return intent, nil
	}
	currency := strings.ToUpper(order.Currency)
	if currency == "" {
		currency = provider.currency
	}
	exponent, err := currencyExponent(currency)
	if err != nil {
		return Intent{}, err
	}
	amount, err := parseMinor(order.Amount.String(), exponent)
	if err != nil {
		return Intent{}, err
	}
	intent.Amount = commerce.Money{Amount: amount, Currency: currency}
	return intent, nil
}

// riopayStatus maps RioPay order statuses; an unknown one stays pending, as in
// Bedolaga.
func riopayStatus(status string) string {
	switch status {
	case "COMPLETED":
		return "succeeded"
	case "CANCELED":
		return "cancelled"
	case "FAILED":
		return "failed"
	case "EXPIRED":
		return "expired"
	default:
		return "pending"
	}
}

var (
	_ Provider            = (*RioPay)(nil)
	_ CurrencySupport     = (*RioPay)(nil)
	_ WebhookAcknowledger = (*RioPay)(nil)
)
