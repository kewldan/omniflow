package payments

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/omniflow/omniflow/internal/commerce"
)

const (
	casheraDefaultBaseURL = "https://api.cashera.cash/api/v1"
	// casheraDescriptionLimit is the description length Cashera accepts.
	casheraDescriptionLimit = 255
)

// casheraMethods are the payment-method codes Cashera's create call takes.
var casheraMethods = map[string]bool{"sbp": true, "card": true, "mastercard": true, "crypto": true, "cryptobot": true}

// Cashera is the Cashera (api.cashera.cash) server-to-server adapter.
//
// Requests authenticate with the X-Api-Key header and amounts are integer
// minor units. Cashera authenticates its webhooks with the merchant's own
// X-Api-Key and X-Secret, which are compared here in constant time.
type Cashera struct {
	apiKey        string
	apiSecret     string
	baseURL       string
	paymentMethod string
	callbackURL   string
	returnURL     string
	failURL       string
	http          *http.Client
	clock         func() time.Time
}

// buildCashera reads APP_CASHERA_* and constructs the adapter.
func buildCashera(settings Settings) (Provider, error) {
	apiKey := settings.String("APP_CASHERA_API_KEY")
	apiSecret := settings.String("APP_CASHERA_API_SECRET")
	if apiKey == "" && apiSecret == "" {
		return nil, ErrNotConfigured
	}
	if apiKey == "" || apiSecret == "" {
		return nil, errors.New("cashera: APP_CASHERA_API_KEY and APP_CASHERA_API_SECRET must both be set")
	}
	method := strings.ToLower(settings.String("APP_CASHERA_PAYMENT_METHOD"))
	if method != "" && !casheraMethods[method] {
		return nil, errors.New("cashera: APP_CASHERA_PAYMENT_METHOD must be one of sbp, card, mastercard, crypto, cryptobot")
	}
	baseURL := settings.String("APP_CASHERA_BASE_URL")
	if baseURL == "" {
		baseURL = casheraDefaultBaseURL
	}
	return &Cashera{
		apiKey:        apiKey,
		apiSecret:     apiSecret,
		baseURL:       strings.TrimSuffix(baseURL, "/"),
		paymentMethod: method,
		// Without a public URL no callback_url is sent and Cashera uses the
		// address saved in the merchant cabinet.
		callbackURL: settings.WebhookURL("cashera"),
		returnURL:   settings.String("APP_CASHERA_RETURN_URL"),
		failURL:     settings.String("APP_CASHERA_FAILED_URL"),
		http:        tracedClient(30 * time.Second),
		clock:       time.Now,
	}, nil
}

func (provider *Cashera) Name() string { return "cashera" }

func (provider *Cashera) Capabilities() Capabilities {
	return Capabilities{Webhooks: true, Polling: true}
}

// SupportsCurrency reports RUB only: Cashera accepts payments in RUB.
func (provider *Cashera) SupportsCurrency(currency string) bool {
	return strings.EqualFold(strings.TrimSpace(currency), "RUB")
}

// casheraMinor reads an integer minor-unit amount sent as a number or string.
type casheraMinor struct {
	set   bool
	value int64
}

func (amount *casheraMinor) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	text := string(trimmed)
	var quoted string
	if err := json.Unmarshal(trimmed, &quoted); err == nil {
		text = quoted
	}
	value, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
	if err != nil {
		return fmt.Errorf("cashera: amount %s is not an integer of minor units", data)
	}
	amount.set, amount.value = true, value
	return nil
}

type casheraTransaction struct {
	UUID          string       `json:"uuid"`
	ExternalID    string       `json:"external_id"`
	Status        string       `json:"status"`
	Amount        casheraMinor `json:"amount"`
	Currency      string       `json:"currency"`
	PaymentURL    string       `json:"payment_url"`
	PaymentMethod string       `json:"payment_method"`
	ExpiresAt     string       `json:"expires_at"`
}

func (transaction casheraTransaction) money() (commerce.Money, error) {
	if !transaction.Amount.set {
		return commerce.Money{}, fmt.Errorf("%w: cashera transaction without amount", ErrProviderResponse)
	}
	currency := strings.ToUpper(strings.TrimSpace(transaction.Currency))
	if _, err := currencyExponent(currency); err != nil {
		return commerce.Money{}, err
	}
	return commerce.Money{Amount: transaction.Amount.value, Currency: currency}, nil
}

func (provider *Cashera) Create(ctx context.Context, request CreateRequest) (Intent, error) {
	if !provider.SupportsCurrency(request.Amount.Currency) {
		return Intent{}, fmt.Errorf("cashera: currency %s is not supported", request.Amount.Currency)
	}
	if request.Amount.Amount <= 0 {
		return Intent{}, errors.New("cashera: amount must be positive")
	}
	description := request.Description
	if description == "" {
		description = "Order " + request.OrderID
	}
	// external_id is Cashera's idempotency key: a repeat with the same body
	// returns the same transaction, so a retried create cannot double-charge.
	body := map[string]any{
		"amount":      request.Amount.Amount,
		"currency":    "RUB",
		"external_id": request.OrderID,
		"description": casheraTruncate(description, casheraDescriptionLimit),
	}
	if provider.paymentMethod != "" {
		body["payment_method"] = provider.paymentMethod
	}
	if provider.callbackURL != "" {
		body["callback_url"] = provider.callbackURL
	}
	returnURL := request.ReturnURL
	if returnURL == "" {
		returnURL = provider.returnURL
	}
	if returnURL != "" {
		body["success_url"] = returnURL
	}
	failURL := provider.failURL
	if failURL == "" {
		failURL = returnURL
	}
	if failURL != "" {
		body["fail_url"] = failURL
	}
	var transaction casheraTransaction
	if err := provider.do(ctx, http.MethodPost, "/integration/transactions", body, &transaction); err != nil {
		return Intent{}, err
	}
	if transaction.UUID == "" {
		return Intent{}, fmt.Errorf("%w: cashera transaction without uuid", ErrProviderResponse)
	}
	intent := Intent{
		ProviderReference: transaction.UUID,
		Status:            casheraStatus(transaction.Status),
		CheckoutURL:       casheraNormalizeURL(transaction.PaymentURL),
		Amount:            request.Amount,
	}
	if expires, err := time.Parse(time.RFC3339, transaction.ExpiresAt); err == nil {
		expires = expires.UTC()
		intent.ExpiresAt = &expires
	}
	return intent, nil
}

func (provider *Cashera) Poll(ctx context.Context, reference string) (Intent, error) {
	var transaction casheraTransaction
	if err := provider.do(ctx, http.MethodGet, "/integration/transactions/"+url.PathEscape(reference), nil, &transaction); err != nil {
		return Intent{}, err
	}
	if transaction.Status == "" {
		return Intent{}, fmt.Errorf("%w: cashera transaction without status", ErrProviderResponse)
	}
	intent := Intent{ProviderReference: reference, Status: casheraStatus(transaction.Status), CheckoutURL: casheraNormalizeURL(transaction.PaymentURL)}
	money, err := transaction.money()
	if err != nil {
		if intent.Status == "succeeded" {
			return Intent{}, err
		}
	} else {
		intent.Amount = money
	}
	return intent, nil
}

func (provider *Cashera) Refund(context.Context, RefundRequest) (Refund, error) {
	return Refund{}, ErrUnsupported
}

// VerifyWebhook authenticates a notification by its X-Api-Key and X-Secret
// headers before reading the body.
//
// Only transaction.status_updated moves money. Every other event —
// webhook.test from the cabinet, payouts, subscriptions — is acknowledged as
// an event that changes nothing, as the reference integration does.
func (provider *Cashera) VerifyWebhook(headers http.Header, body []byte) (WebhookEvent, error) {
	keyOK := hmac.Equal([]byte(headers.Get("X-Api-Key")), []byte(provider.apiKey))
	secretOK := hmac.Equal([]byte(headers.Get("X-Secret")), []byte(provider.apiSecret))
	if !keyOK || !secretOK {
		return WebhookEvent{}, ErrInvalidSignature
	}
	var notification struct {
		Event       string              `json:"event"`
		Transaction *casheraTransaction `json:"transaction"`
	}
	if err := json.Unmarshal(body, &notification); err != nil || notification.Event == "" {
		return WebhookEvent{}, ErrProviderResponse
	}
	now := provider.clock().UTC()
	if notification.Event != "transaction.status_updated" || notification.Transaction == nil {
		digest := sha256.Sum256(body)
		return WebhookEvent{ID: notification.Event + ":" + hex.EncodeToString(digest[:8]), Type: notification.Event, Status: "pending", OccurredAt: now}, nil
	}
	transaction := notification.Transaction
	if transaction.UUID == "" && transaction.ExternalID == "" {
		return WebhookEvent{}, ErrProviderResponse
	}
	rawStatus := strings.ToLower(strings.TrimSpace(transaction.Status))
	status := casheraStatus(rawStatus)
	money, err := transaction.money()
	if err != nil && status == "succeeded" {
		// A payment reported paid without a confirmed amount is not credited;
		// the delivery is refused so it is retried or reconciled.
		return WebhookEvent{}, err
	}
	return WebhookEvent{
		ID:                notification.Event + ":" + transaction.UUID + ":" + transaction.ExternalID + ":" + rawStatus,
		Type:              notification.Event,
		ProviderReference: transaction.UUID,
		MerchantReference: transaction.ExternalID,
		Status:            status,
		Amount:            money,
		OccurredAt:        now,
	}, nil
}

func (provider *Cashera) do(ctx context.Context, method, path string, body, target any) error {
	headers := make(http.Header)
	headers.Set("X-Api-Key", provider.apiKey)
	return doJSON(ctx, provider.http, method, provider.baseURL+path, headers, body, target)
}

func casheraStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "paid":
		return "succeeded"
	case "failed":
		return "failed"
	case "expired":
		return "expired"
	case "refunded":
		// Reversals of a settled payment: the payment service records the
		// refund and takes back what the payment bought.
		return StatusRefunded
	case "chargeback":
		return StatusChargeback
	default:
		// pending and anything unrecognised.
		return "pending"
	}
}

// casheraNormalizeURL adds the scheme Cashera's examples leave off
// (pay.cashera.cash/...), so the link opens as an absolute URL.
func casheraNormalizeURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.HasPrefix(raw, "https://") || strings.HasPrefix(raw, "http://") {
		return raw
	}
	return "https://" + strings.TrimLeft(raw, "/")
}

func casheraTruncate(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	return string([]rune(value)[:limit])
}

var (
	_ Provider        = (*Cashera)(nil)
	_ CurrencySupport = (*Cashera)(nil)
)
