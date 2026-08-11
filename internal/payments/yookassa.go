package payments

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/omniflow/omniflow/internal/commerce"
)

type YooKassa struct {
	shopID  string
	secret  string
	baseURL string
	http    *http.Client
	clock   func() time.Time
}

func NewYooKassa(shopID, secret string) (*YooKassa, error) {
	if strings.TrimSpace(shopID) == "" || strings.TrimSpace(secret) == "" {
		return nil, errors.New("YooKassa shop ID and secret are required")
	}
	return &YooKassa{shopID: shopID, secret: secret, baseURL: "https://api.yookassa.ru/v3", http: tracedClient(15 * time.Second), clock: time.Now}, nil
}

func (provider *YooKassa) Name() string { return "yookassa" }
func (provider *YooKassa) Capabilities() Capabilities {
	return Capabilities{Refunds: true, PartialRefund: true, Recurring: true, Webhooks: true, Polling: true}
}

// SupportsCurrency reports the settlement currencies a YooKassa shop can accept.
func (provider *YooKassa) SupportsCurrency(currency string) bool {
	switch strings.ToUpper(strings.TrimSpace(currency)) {
	case "RUB", "USD", "EUR":
		return true
	default:
		return false
	}
}

type yooMoney struct {
	Value    string `json:"value"`
	Currency string `json:"currency"`
}

type yooPayment struct {
	ID           string    `json:"id"`
	Status       string    `json:"status"`
	Amount       yooMoney  `json:"amount"`
	CreatedAt    time.Time `json:"created_at"`
	Confirmation struct {
		URL string `json:"confirmation_url"`
	} `json:"confirmation"`
}

func (provider *YooKassa) Create(ctx context.Context, request CreateRequest) (Intent, error) {
	exponent, err := currencyExponent(request.Amount.Currency)
	if err != nil {
		return Intent{}, err
	}
	body := map[string]any{
		"amount":       yooMoney{Value: formatMinor(request.Amount.Amount, exponent), Currency: request.Amount.Currency},
		"capture":      true,
		"confirmation": map[string]string{"type": "redirect", "return_url": request.ReturnURL},
		"description":  request.Description,
		"metadata":     map[string]string{"order_id": request.OrderID},
	}
	var payment yooPayment
	if err := provider.do(ctx, http.MethodPost, "/payments", request.IdempotencyKey, body, &payment); err != nil {
		return Intent{}, err
	}
	return provider.intent(payment)
}

func (provider *YooKassa) Poll(ctx context.Context, reference string) (Intent, error) {
	var payment yooPayment
	if err := provider.do(ctx, http.MethodGet, "/payments/"+reference, "", nil, &payment); err != nil {
		return Intent{}, err
	}
	return provider.intent(payment)
}

func (provider *YooKassa) Refund(ctx context.Context, request RefundRequest) (Refund, error) {
	exponent, err := currencyExponent(request.Amount.Currency)
	if err != nil {
		return Refund{}, err
	}
	body := map[string]any{
		"payment_id":  request.PaymentReference,
		"amount":      yooMoney{Value: formatMinor(request.Amount.Amount, exponent), Currency: request.Amount.Currency},
		"description": request.Reason,
	}
	var response struct {
		ID     string   `json:"id"`
		Status string   `json:"status"`
		Amount yooMoney `json:"amount"`
	}
	if err := provider.do(ctx, http.MethodPost, "/refunds", request.IdempotencyKey, body, &response); err != nil {
		return Refund{}, err
	}
	exponent, err = currencyExponent(response.Amount.Currency)
	if err != nil {
		return Refund{}, err
	}
	amount, err := parseMinor(response.Amount.Value, exponent)
	if err != nil {
		return Refund{}, err
	}
	return Refund{ProviderReference: response.ID, Status: normalizeYooStatus(response.Status), Amount: commerce.Money{Amount: amount, Currency: response.Amount.Currency}}, nil
}

func (provider *YooKassa) VerifyWebhook(_ http.Header, body []byte) (WebhookEvent, error) {
	// YooKassa does not sign Basic-auth webhooks. Authenticity is established by
	// immediately fetching the referenced object over the authenticated API before
	// applying state; this parser only extracts the untrusted notification envelope.
	var notification struct {
		Type   string     `json:"type"`
		Event  string     `json:"event"`
		Object yooPayment `json:"object"`
	}
	if err := json.Unmarshal(body, &notification); err != nil || notification.Type != "notification" || notification.Object.ID == "" {
		return WebhookEvent{}, ErrProviderResponse
	}
	exponent, err := currencyExponent(notification.Object.Amount.Currency)
	if err != nil {
		return WebhookEvent{}, err
	}
	amount, err := parseMinor(notification.Object.Amount.Value, exponent)
	if err != nil {
		return WebhookEvent{}, err
	}
	digest := sha256.Sum256(body)
	return WebhookEvent{ID: notification.Event + ":" + notification.Object.ID + ":" + hex.EncodeToString(digest[:8]), Type: notification.Event, ProviderReference: notification.Object.ID, Status: normalizeYooStatus(notification.Object.Status), Amount: commerce.Money{Amount: amount, Currency: notification.Object.Amount.Currency}, OccurredAt: provider.clock().UTC()}, nil
}

func (provider *YooKassa) do(ctx context.Context, method, path, idempotencyKey string, body, target any) error {
	headers := make(http.Header)
	headers.Set("Authorization", "Basic "+basicAuth(provider.shopID, provider.secret))
	if idempotencyKey != "" {
		headers.Set("Idempotence-Key", idempotencyKey)
	}
	return doJSON(ctx, provider.http, method, provider.baseURL+path, headers, body, target)
}

func (provider *YooKassa) intent(payment yooPayment) (Intent, error) {
	exponent, err := currencyExponent(payment.Amount.Currency)
	if err != nil {
		return Intent{}, err
	}
	amount, err := parseMinor(payment.Amount.Value, exponent)
	if err != nil {
		return Intent{}, err
	}
	return Intent{ProviderReference: payment.ID, Status: normalizeYooStatus(payment.Status), CheckoutURL: payment.Confirmation.URL, Amount: commerce.Money{Amount: amount, Currency: payment.Amount.Currency}}, nil
}

func normalizeYooStatus(status string) string {
	switch status {
	case "succeeded":
		return "succeeded"
	case "canceled":
		return "cancelled"
	case "waiting_for_capture":
		return "processing"
	default:
		return "pending"
	}
}

func basicAuth(username, password string) string {
	request, _ := http.NewRequest(http.MethodGet, "https://example.invalid", nil)
	request.SetBasicAuth(username, password)
	return strings.TrimPrefix(request.Header.Get("Authorization"), "Basic ")
}

var _ Provider = (*YooKassa)(nil)
var _ = fmt.Sprintf

// Probe verifies the shop credentials by listing a single payment.
//
// The list endpoint is authenticated and read-only, so a pass means the shop
// identifier and secret are accepted by YooKassa and a fail means they are not.
// It never creates anything: an operator testing a connection must not be able
// to produce a payment.
func (provider *YooKassa) Probe(ctx context.Context) error {
	var response struct {
		Type string `json:"type"`
	}
	return provider.do(ctx, http.MethodGet, "/payments?limit=1", "", nil, &response)
}

// ChargeSaved charges a payment method the customer bound earlier.
//
// `capture: true` settles in one step rather than authorising and capturing
// separately: there is nobody present to confirm, so a two-phase charge would
// only create a window in which money is held and never taken.
//
// The idempotency key is the caller's, and YooKassa honours it for 24 hours,
// which is what makes a retried worker safe: the same key returns the same
// payment rather than making a second one.
func (provider *YooKassa) ChargeSaved(ctx context.Context, request SavedChargeRequest) (Intent, error) {
	exponent, err := currencyExponent(request.Amount.Currency)
	if err != nil {
		return Intent{}, err
	}
	body := map[string]any{
		"amount": yooMoney{
			Value:    formatMinor(request.Amount.Amount, exponent),
			Currency: request.Amount.Currency,
		},
		"capture":           true,
		"description":       request.Description,
		"payment_method_id": request.MethodToken,
		"metadata":          map[string]string{"order_id": request.OrderID},
	}
	var payment yooPayment
	if err := provider.do(
		ctx, http.MethodPost, "/payments", request.IdempotencyKey, body, &payment,
	); err != nil {
		return Intent{}, err
	}
	return provider.intent(payment)
}
