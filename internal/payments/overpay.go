package payments

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/omniflow/omniflow/internal/commerce"
)

// Overpay settles hosted-checkout orders through the Overpay API.
//
// Requests use HTTP Basic authentication and, when the merchant was issued
// one, a TLS client certificate. Overpay authenticates its notifications to
// the merchant with mutual TLS terminated in front of the application, which
// Omniflow cannot observe, so a notification is a hint: the payment service
// re-reads the order through the authenticated API before anything settles.
type Overpay struct {
	username  string
	password  string
	projectID string
	currency  string
	lifetime  int
	methods   []string
	baseURL   string
	http      *http.Client
	clock     func() time.Time
}

const overpayDefaultURL = "https://api.overpay.io"

func buildOverpay(settings Settings) (Provider, error) {
	username := settings.String("APP_OVERPAY_USERNAME")
	password := settings.String("APP_OVERPAY_PASSWORD")
	projectID := settings.String("APP_OVERPAY_PROJECT_ID")
	if username == "" && password == "" && projectID == "" {
		return nil, ErrNotConfigured
	}
	if username == "" || password == "" || projectID == "" {
		return nil, errors.New("Overpay needs APP_OVERPAY_USERNAME, APP_OVERPAY_PASSWORD, and APP_OVERPAY_PROJECT_ID together")
	}
	currency := strings.ToUpper(settings.String("APP_OVERPAY_CURRENCY"))
	if currency == "" {
		currency = "RUB"
	}
	if _, err := currencyExponent(currency); err != nil {
		return nil, fmt.Errorf("APP_OVERPAY_CURRENCY %q is not a currency Omniflow can price in", currency)
	}
	baseURL := strings.TrimSuffix(settings.String("APP_OVERPAY_API_URL"), "/")
	if baseURL == "" {
		baseURL = overpayDefaultURL
	}
	methodsSetting := settings.String("APP_OVERPAY_PAYMENT_METHODS")
	if methodsSetting == "" {
		methodsSetting = "card,fps"
	}
	var methods []string
	for method := range strings.SplitSeq(methodsSetting, ",") {
		if method = strings.TrimSpace(method); method != "" {
			methods = append(methods, method)
		}
	}
	client, err := overpayClient(settings.String("APP_OVERPAY_CLIENT_CERT"), settings.String("APP_OVERPAY_CLIENT_KEY"))
	if err != nil {
		return nil, err
	}
	return &Overpay{
		username: username, password: password, projectID: projectID, currency: currency,
		lifetime: max(settings.Int("APP_OVERPAY_LIFETIME_MINUTES", 1440), 1), methods: methods,
		baseURL: baseURL, http: client, clock: time.Now,
	}, nil
}

// overpayClient is the traced client, carrying the merchant's TLS client
// certificate when one is configured. Both halves are PEM; a value written on
// one line with literal `\n` sequences, as `.env` files often hold it, is
// accepted too.
func overpayClient(certificatePEM, keyPEM string) (*http.Client, error) {
	if certificatePEM == "" && keyPEM == "" {
		return tracedClient(30 * time.Second), nil
	}
	if certificatePEM == "" || keyPEM == "" {
		return nil, errors.New("Overpay needs both APP_OVERPAY_CLIENT_CERT and APP_OVERPAY_CLIENT_KEY for mutual TLS")
	}
	certificate, err := tls.X509KeyPair([]byte(overpayPEM(certificatePEM)), []byte(overpayPEM(keyPEM)))
	if err != nil {
		// The parse error can quote key material, so it is not wrapped.
		return nil, errors.New("APP_OVERPAY_CLIENT_CERT and APP_OVERPAY_CLIENT_KEY are not a matching unencrypted PEM certificate and key")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}}
	return &http.Client{Timeout: 30 * time.Second, Transport: otelhttp.NewTransport(transport)}, nil
}

func overpayPEM(value string) string {
	if !strings.Contains(value, "\n") {
		return strings.ReplaceAll(value, `\n`, "\n")
	}
	return value
}

func (provider *Overpay) Name() string { return "overpay" }

// Capabilities declares polling and poll-verified notifications. Refunds are
// not declared: Overpay's refund call takes no idempotency key, and Omniflow
// re-asks a refund whose answer was lost, which could refund twice.
func (provider *Overpay) Capabilities() Capabilities {
	return Capabilities{Webhooks: true, Polling: true}
}

// SupportsCurrency accepts the currency the project is configured to settle.
func (provider *Overpay) SupportsCurrency(currency string) bool {
	return strings.ToUpper(strings.TrimSpace(currency)) == provider.currency
}

// WebhooksNeedPolling marks Overpay notifications as untrusted hints.
func (provider *Overpay) WebhooksNeedPolling() bool { return true }

type overpayOrder struct {
	ID                    json.RawMessage `json:"id"`
	Status                string          `json:"status"`
	Amount                json.RawMessage `json:"amount"`
	Currency              string          `json:"currency"`
	MerchantTransactionID string          `json:"merchantTransactionId"`
	ResultURL             string          `json:"resultUrl"`
}

func (provider *Overpay) Create(ctx context.Context, request CreateRequest) (Intent, error) {
	if !provider.SupportsCurrency(request.Amount.Currency) {
		return Intent{}, commerce.ErrCurrencyMismatch
	}
	exponent, err := currencyExponent(provider.currency)
	if err != nil {
		return Intent{}, err
	}
	body := map[string]any{
		"amount":                formatMinor(request.Amount.Amount, exponent),
		"currency":              provider.currency,
		"livetimeMinutes":       provider.lifetime,
		"projectId":             provider.projectID,
		"merchantTransactionId": request.OrderID,
	}
	if request.Description != "" {
		body["description"] = request.Description
	}
	if request.ReturnURL != "" {
		body["returnUrl"] = request.ReturnURL
	}
	if len(provider.methods) > 0 {
		body["paymentMethods"] = provider.methods
	}
	var order overpayOrder
	if err := provider.do(ctx, http.MethodPost, "/orders/", body, &order); err != nil {
		return Intent{}, err
	}
	id := overpayID(order.ID)
	if id == "" || order.ResultURL == "" {
		return Intent{}, fmt.Errorf("%w: order was created without a checkout link", ErrProviderResponse)
	}
	expires := provider.clock().UTC().Add(time.Duration(provider.lifetime) * time.Minute)
	return Intent{ProviderReference: id, Status: "pending", CheckoutURL: order.ResultURL, Amount: request.Amount, ExpiresAt: &expires}, nil
}

// Poll reads `GET /orders/{id}`. Overpay answers either with the order itself
// or with an `orders` list holding it.
//
// A charged order whose amount cannot be read is an error rather than a
// success: the poll is what authenticates a notification, and an amount it
// cannot confirm must not be settled.
func (provider *Overpay) Poll(ctx context.Context, reference string) (Intent, error) {
	var response struct {
		overpayOrder
		Orders []overpayOrder `json:"orders"`
	}
	if err := provider.do(ctx, http.MethodGet, "/orders/"+url.PathEscape(reference), nil, &response); err != nil {
		return Intent{}, err
	}
	order := response.overpayOrder
	if order.Status == "" && len(response.Orders) > 0 {
		order = response.Orders[0]
	}
	if order.Status == "" {
		return Intent{}, fmt.Errorf("%w: order has no status", ErrProviderResponse)
	}
	if id := overpayID(order.ID); id != "" && id != reference {
		return Intent{}, fmt.Errorf("%w: order mismatch", ErrProviderResponse)
	}
	intent := Intent{ProviderReference: reference, Status: normalizeOverpayStatus(order.Status), CheckoutURL: order.ResultURL}
	amount, err := overpayMoney(order, provider.currency)
	if err != nil && intent.Status == "succeeded" {
		return Intent{}, err
	}
	intent.Amount = amount
	return intent, nil
}

func (provider *Overpay) Refund(context.Context, RefundRequest) (Refund, error) {
	return Refund{}, ErrUnsupported
}

// VerifyWebhook parses the notification envelope. Nothing in it is trusted:
// the status and amount applied come from Poll.
func (provider *Overpay) VerifyWebhook(_ http.Header, body []byte) (WebhookEvent, error) {
	var notification overpayOrder
	if err := json.Unmarshal(body, &notification); err != nil {
		return WebhookEvent{}, ErrProviderResponse
	}
	id := overpayID(notification.ID)
	if id == "" || notification.MerchantTransactionID == "" || notification.Status == "" {
		return WebhookEvent{}, ErrProviderResponse
	}
	return WebhookEvent{
		ID: notification.Status + ":" + id, Type: notification.Status,
		ProviderReference: id, MerchantReference: notification.MerchantTransactionID,
		Status: normalizeOverpayStatus(notification.Status), OccurredAt: provider.clock().UTC(),
	}, nil
}

func (provider *Overpay) do(ctx context.Context, method, path string, body, target any) error {
	headers := make(http.Header)
	headers.Set("Authorization", "Basic "+basicAuth(provider.username, provider.password))
	return doJSON(ctx, provider.http, method, provider.baseURL+path, headers, body, target)
}

// overpayID reads an identifier Overpay may send as a string or a number.
func overpayID(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	return string(raw)
}

func overpayMoney(order overpayOrder, fallbackCurrency string) (commerce.Money, error) {
	text := overpayID(order.Amount)
	if text == "" {
		return commerce.Money{}, fmt.Errorf("%w: order carries no amount", ErrProviderResponse)
	}
	currency := strings.ToUpper(strings.TrimSpace(order.Currency))
	if currency == "" {
		currency = fallbackCurrency
	}
	exponent, err := currencyExponent(currency)
	if err != nil {
		return commerce.Money{}, err
	}
	minor, err := parseMinor(text, exponent)
	if err != nil {
		return commerce.Money{}, err
	}
	return commerce.Money{Amount: minor, Currency: currency}, nil
}

// normalizeOverpayStatus maps Overpay's order statuses. Only `charged` is
// money received; `authorized` is a hold and `credited` is a payout.
func normalizeOverpayStatus(status string) string {
	switch status {
	case "charged":
		return "succeeded"
	case "authorized", "prepared", "prepared_for_holder_metadata_collecting", "processing", "credited":
		return "processing"
	case "declined", "rejected", "error", "representment":
		return "failed"
	case "chargeback":
		return StatusChargeback
	case "reversed", "refunded":
		return StatusRefunded
	default:
		return "pending"
	}
}

var (
	_ Provider             = (*Overpay)(nil)
	_ CurrencySupport      = (*Overpay)(nil)
	_ PollVerifiedWebhooks = (*Overpay)(nil)
)
