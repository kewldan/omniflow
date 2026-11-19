package payments

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
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

// RollyPay settles through RollyPay (rollypay.io).
//
// Payments are created with the API key in X-API-Key and a fresh X-Nonce on
// every call. Notifications are signed with HMAC-SHA256 over
// "<X-Timestamp>.<raw body>" with the signing secret, carried in X-Signature.
type RollyPay struct {
	apiKey        string
	signingSecret string
	currency      string
	exponent      int
	method        string
	returnURL     string
	baseURL       string
	http          *http.Client
	clock         func() time.Time
}

const rollypayAPIBaseURL = "https://rollypay.io/api/v1"

// rollypayMethods are the payment methods Bedolaga offers as RollyPay options.
var rollypayMethods = map[string]bool{"sbp": true, "card": true, "crypto": true}

func buildRollyPay(settings Settings) (Provider, error) {
	apiKey := settings.String("APP_ROLLYPAY_API_KEY")
	secret := settings.String("APP_ROLLYPAY_SIGNING_SECRET")
	switch {
	case apiKey == "" && secret == "":
		return nil, ErrNotConfigured
	case apiKey == "":
		return nil, errors.New("RollyPay is partially configured: APP_ROLLYPAY_API_KEY must also be set")
	case secret == "":
		return nil, errors.New("RollyPay is partially configured: APP_ROLLYPAY_SIGNING_SECRET must also be set")
	}
	currency := strings.ToUpper(settings.String("APP_ROLLYPAY_CURRENCY"))
	if currency == "" {
		currency = "RUB"
	}
	exponent, err := currencyExponent(currency)
	if err != nil {
		return nil, fmt.Errorf("RollyPay currency %q is not supported", currency)
	}
	method := strings.ToLower(settings.String("APP_ROLLYPAY_PAYMENT_METHOD"))
	if method != "" && !rollypayMethods[method] {
		return nil, errors.New("APP_ROLLYPAY_PAYMENT_METHOD must be sbp, card, or crypto")
	}
	return &RollyPay{
		apiKey: apiKey, signingSecret: secret, currency: currency, exponent: exponent,
		method: method, returnURL: settings.String("APP_ROLLYPAY_RETURN_URL"),
		baseURL: rollypayAPIBaseURL, http: tracedClient(30 * time.Second), clock: time.Now,
	}, nil
}

func (provider *RollyPay) Name() string { return "rollypay" }

func (provider *RollyPay) Capabilities() Capabilities {
	return Capabilities{Webhooks: true, Polling: true}
}

// SupportsCurrency reports the single currency this account is configured to take.
func (provider *RollyPay) SupportsCurrency(currency string) bool {
	return strings.ToUpper(strings.TrimSpace(currency)) == provider.currency
}

type rollypayPayment struct {
	EventType       string      `json:"event_type"`
	PaymentID       string      `json:"payment_id"`
	OrderID         string      `json:"order_id"`
	Status          string      `json:"status"`
	PayURL          string      `json:"pay_url"`
	ExpiresAt       string      `json:"expires_at"`
	Amount          json.Number `json:"amount"`
	Currency        string      `json:"currency"`
	PaymentCurrency string      `json:"payment_currency"`
}

func (provider *RollyPay) Create(ctx context.Context, request CreateRequest) (Intent, error) {
	if !provider.SupportsCurrency(request.Amount.Currency) {
		return Intent{}, commerce.ErrCurrencyMismatch
	}
	if request.Amount.Amount <= 0 {
		return Intent{}, errors.New("RollyPay amount must be positive")
	}
	body := map[string]string{
		"amount":           formatMinor(request.Amount.Amount, provider.exponent),
		"payment_currency": provider.currency,
		"order_id":         request.OrderID,
	}
	if provider.method != "" {
		body["payment_method"] = provider.method
	}
	if request.Description != "" {
		body["description"] = request.Description
	}
	redirect := request.ReturnURL
	if redirect == "" {
		redirect = provider.returnURL
	}
	if redirect != "" {
		body["redirect_url"] = redirect
	}
	headers, err := provider.headers()
	if err != nil {
		return Intent{}, err
	}
	var payment rollypayPayment
	if err := doJSON(ctx, provider.http, http.MethodPost, provider.baseURL+"/payments", headers, body, &payment); err != nil {
		return Intent{}, err
	}
	if payment.PaymentID == "" || payment.PayURL == "" {
		return Intent{}, fmt.Errorf("%w: RollyPay returned no payment link", ErrProviderResponse)
	}
	intent := Intent{ProviderReference: payment.PaymentID, Status: rollypayStatus(payment.EventType, payment.Status), CheckoutURL: payment.PayURL, Amount: request.Amount}
	if expires, err := time.Parse(time.RFC3339, payment.ExpiresAt); err == nil {
		expires = expires.UTC()
		intent.ExpiresAt = &expires
	}
	return intent, nil
}

func (provider *RollyPay) Poll(ctx context.Context, reference string) (Intent, error) {
	headers, err := provider.headers()
	if err != nil {
		return Intent{}, err
	}
	var payment rollypayPayment
	if err := doJSON(ctx, provider.http, http.MethodGet, provider.baseURL+"/payments/"+url.PathEscape(reference), headers, nil, &payment); err != nil {
		return Intent{}, err
	}
	if payment.Status == "" {
		return Intent{}, fmt.Errorf("%w: RollyPay returned no payment status", ErrProviderResponse)
	}
	status := rollypayStatus("", payment.Status)
	amount, err := provider.amount(payment, status)
	if err != nil {
		return Intent{}, err
	}
	return Intent{ProviderReference: reference, Status: status, CheckoutURL: payment.PayURL, Amount: amount}, nil
}

func (provider *RollyPay) Refund(context.Context, RefundRequest) (Refund, error) {
	return Refund{}, ErrUnsupported
}

func (provider *RollyPay) VerifyWebhook(headers http.Header, body []byte) (WebhookEvent, error) {
	timestamp := strings.TrimSpace(headers.Get("X-Timestamp"))
	provided, err := hex.DecodeString(strings.TrimSpace(headers.Get("X-Signature")))
	if err != nil || len(provided) == 0 || timestamp == "" {
		return WebhookEvent{}, ErrInvalidSignature
	}
	mac := hmac.New(sha256.New, []byte(provider.signingSecret))
	_, _ = mac.Write([]byte(timestamp + "."))
	_, _ = mac.Write(body)
	if !hmac.Equal(provided, mac.Sum(nil)) {
		return WebhookEvent{}, ErrInvalidSignature
	}
	var payment rollypayPayment
	if err := json.Unmarshal(body, &payment); err != nil || payment.PaymentID == "" || payment.Status == "" {
		return WebhookEvent{}, ErrProviderResponse
	}
	status := rollypayStatus(payment.EventType, payment.Status)
	amount, err := provider.amount(payment, status)
	if err != nil {
		return WebhookEvent{}, err
	}
	kind := payment.EventType
	if kind == "" {
		kind = "payment"
	}
	return WebhookEvent{
		ID: kind + ":" + payment.PaymentID + ":" + payment.Status, Type: kind,
		ProviderReference: payment.PaymentID, MerchantReference: payment.OrderID,
		Status: status, Amount: amount, OccurredAt: provider.clock().UTC(),
	}, nil
}

// WebhookAck answers as Bedolaga did; RollyPay retries anything but a 200.
func (provider *RollyPay) WebhookAck(string) (int, string, []byte) {
	return http.StatusOK, "application/json", []byte(`{"status":true}`)
}

func (provider *RollyPay) headers() (http.Header, error) {
	nonce, err := rollypayNonce()
	if err != nil {
		return nil, err
	}
	headers := make(http.Header)
	headers.Set("X-API-Key", provider.apiKey)
	headers.Set("X-Nonce", nonce)
	return headers, nil
}

func (provider *RollyPay) amount(payment rollypayPayment, status string) (commerce.Money, error) {
	if payment.Amount == "" {
		if status == "succeeded" {
			return commerce.Money{}, fmt.Errorf("%w: paid RollyPay payment carries no amount", ErrProviderResponse)
		}
		return commerce.Money{}, nil
	}
	currency := strings.ToUpper(payment.Currency)
	if currency == "" {
		currency = strings.ToUpper(payment.PaymentCurrency)
	}
	if currency == "" {
		currency = provider.currency
	}
	exponent, err := currencyExponent(currency)
	if err != nil {
		return commerce.Money{}, err
	}
	amount, err := parseMinor(payment.Amount.String(), exponent)
	if err != nil {
		return commerce.Money{}, err
	}
	return commerce.Money{Amount: amount, Currency: currency}, nil
}

// rollypayStatus maps a RollyPay status. A `payment.paid` event settles
// regardless of the status field, as in Bedolaga; a chargeback is not a
// settlement and is reported as failed.
func rollypayStatus(eventType, status string) string {
	if eventType == "payment.paid" {
		return "succeeded"
	}
	switch status {
	case "paid":
		return "succeeded"
	case "processing":
		return "processing"
	case "expired":
		return "expired"
	case "canceled":
		return "cancelled"
	case "chargeback":
		return StatusChargeback
	default:
		return "pending"
	}
}

// rollypayNonce is a random UUIDv4, the nonce form Bedolaga sends.
func rollypayNonce() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate RollyPay nonce: %w", err)
	}
	raw[6] = raw[6]&0x0f | 0x40
	raw[8] = raw[8]&0x3f | 0x80
	encoded := hex.EncodeToString(raw[:])
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:], nil
}

var (
	_ Provider            = (*RollyPay)(nil)
	_ CurrencySupport     = (*RollyPay)(nil)
	_ WebhookAcknowledger = (*RollyPay)(nil)
)
