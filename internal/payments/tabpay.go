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
	"strconv"
	"strings"
	"time"

	"github.com/omniflow/omniflow/internal/commerce"
)

// TabPay is the adapter for TabPay (tabpay.org): SBP and 3-D Secure card
// payments behind a hosted payUrl, with integer kopeks everywhere.
//
// Requests authenticate with the shop key in X-Api-Key. Webhooks are signed
// with a separate signing secret: X-Signature-V2 is a hex HMAC-SHA256 of
// `{X-Timestamp}.{raw body}`, and the timestamp must be fresh in both
// directions so a captured delivery cannot be replayed later.
//
// A sandbox shop marks its payments `test`/`isTest`. Bedolaga never credits
// one, and this adapter never reports one as succeeded.
type TabPay struct {
	apiKey        string
	webhookSecret string
	method        string
	maxAge        time.Duration
	baseURL       string
	http          *http.Client
	clock         func() time.Time
}

const (
	tabpayDefaultBaseURL = "https://tabpay.org/api"
	// tabpayMinAmount and tabpayMaxAmount are the bounds the create API
	// enforces: 1 RUB and 100 million RUB.
	tabpayMinAmount = 100
	tabpayMaxAmount = 10_000_000_000
)

func buildTabPay(settings Settings) (Provider, error) {
	apiKey := settings.String("APP_TABPAY_API_KEY")
	secret := settings.String("APP_TABPAY_WEBHOOK_SECRET")
	if apiKey == "" && secret == "" {
		return nil, ErrNotConfigured
	}
	if apiKey == "" || secret == "" {
		return nil, errors.New("TabPay needs both APP_TABPAY_API_KEY and APP_TABPAY_WEBHOOK_SECRET")
	}
	method := strings.ToUpper(settings.String("APP_TABPAY_METHOD"))
	switch method {
	case "", "SBP", "CARD":
	default:
		return nil, fmt.Errorf("APP_TABPAY_METHOD must be SBP or CARD, got %q", method)
	}
	maxAge := settings.Int("APP_TABPAY_WEBHOOK_MAX_AGE_SECONDS", 300)
	if maxAge <= 0 {
		return nil, errors.New("APP_TABPAY_WEBHOOK_MAX_AGE_SECONDS must be positive")
	}
	baseURL := strings.TrimSuffix(settings.String("APP_TABPAY_BASE_URL"), "/")
	if baseURL == "" {
		baseURL = tabpayDefaultBaseURL
	}
	return &TabPay{
		apiKey: apiKey, webhookSecret: secret, method: method, maxAge: time.Duration(maxAge) * time.Second,
		baseURL: baseURL, http: tracedClient(30 * time.Second), clock: time.Now,
	}, nil
}

func (provider *TabPay) Name() string { return "tabpay" }

// Capabilities declares no refunds and no recurring charges: Bedolaga's
// client covers creating and reading payments only.
func (provider *TabPay) Capabilities() Capabilities {
	return Capabilities{Webhooks: true, Polling: true}
}

// SupportsCurrency reports RUB only: TabPay amounts are kopeks.
func (provider *TabPay) SupportsCurrency(currency string) bool {
	return strings.EqualFold(strings.TrimSpace(currency), "RUB")
}

type tabpayPayment struct {
	ID            string       `json:"id"`
	OrderID       string       `json:"orderId"`
	Status        string       `json:"status"`
	AmountKopecks *json.Number `json:"amountKopecks"`
	PayURL        string       `json:"payUrl"`
	IsTest        any          `json:"isTest"`
	Test          any          `json:"test"`
}

// Create posts /v1/payments. A 409 means either the order ID is already taken
// or the shop does not offer the requested method; TabPay is asked which, and
// an existing payment for the order is returned instead of an error.
func (provider *TabPay) Create(ctx context.Context, request CreateRequest) (Intent, error) {
	if !provider.SupportsCurrency(request.Amount.Currency) || request.Amount.Amount < tabpayMinAmount || request.Amount.Amount > tabpayMaxAmount {
		return Intent{}, fmt.Errorf("%w: TabPay accepts 1 to 100 000 000 RUB", ErrUnsupported)
	}
	body := map[string]any{
		"orderId":       tabpayTruncate(request.OrderID, 64),
		"amountKopecks": request.Amount.Amount,
	}
	if request.Description != "" {
		body["description"] = tabpayTruncate(request.Description, 255)
	}
	if email := strings.TrimSpace(request.Metadata["customer_email"]); email != "" {
		body["email"] = email
	}
	if request.ReturnURL != "" {
		body["successUrl"] = request.ReturnURL
		body["failUrl"] = request.ReturnURL
	}
	if provider.method != "" {
		body["method"] = provider.method
	}
	var payment tabpayPayment
	err := provider.do(ctx, http.MethodPost, "/v1/payments", nil, body, &payment)
	var status *StatusError
	if errors.As(err, &status) && status.StatusCode == http.StatusConflict {
		existing, found, lookupErr := provider.Recover(ctx, request.OrderID)
		if lookupErr == nil && found {
			return existing, nil
		}
		return Intent{}, err
	}
	if err != nil {
		return Intent{}, err
	}
	if payment.ID == "" || payment.PayURL == "" {
		return Intent{}, fmt.Errorf("%w: TabPay returned no payment ID or payUrl", ErrProviderResponse)
	}
	intent := tabpayIntent(payment)
	if intent.Amount.Currency == "" {
		intent.Amount = request.Amount
	}
	return intent, nil
}

// Poll reads GET /v1/payments/{id}.
func (provider *TabPay) Poll(ctx context.Context, reference string) (Intent, error) {
	var payment tabpayPayment
	if err := provider.do(ctx, http.MethodGet, "/v1/payments/"+url.PathEscape(reference), nil, nil, &payment); err != nil {
		return Intent{}, err
	}
	if payment.ID != reference {
		return Intent{}, fmt.Errorf("%w: TabPay answered for another payment", ErrProviderResponse)
	}
	return tabpayIntent(payment), nil
}

// Recover looks the order up with GET /v1/payments?orderId=…; TabPay answers
// 404 when no payment carries it. This is what keeps a create whose answer
// was lost from becoming a second invoice for the same purchase.
func (provider *TabPay) Recover(ctx context.Context, orderID string) (Intent, bool, error) {
	var payment tabpayPayment
	err := provider.do(ctx, http.MethodGet, "/v1/payments", url.Values{"orderId": {tabpayTruncate(orderID, 64)}}, nil, &payment)
	var status *StatusError
	if errors.As(err, &status) && status.StatusCode == http.StatusNotFound {
		return Intent{}, false, nil
	}
	if err != nil {
		return Intent{}, false, err
	}
	if payment.ID == "" {
		return Intent{}, false, nil
	}
	return tabpayIntent(payment), true, nil
}

func (provider *TabPay) Refund(context.Context, RefundRequest) (Refund, error) {
	return Refund{}, ErrUnsupported
}

// VerifyWebhook checks X-Timestamp freshness and X-Signature-V2 over the raw
// body, then parses {id, orderId, status, amountKopecks, test}. A status this
// adapter does not know is acknowledged and ignored, since TabPay may add
// statuses.
func (provider *TabPay) VerifyWebhook(headers http.Header, body []byte) (WebhookEvent, error) {
	timestamp := strings.TrimSpace(headers.Get("X-Timestamp"))
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || seconds < 0 || strings.TrimLeft(timestamp, "0123456789") != "" {
		return WebhookEvent{}, ErrInvalidSignature
	}
	age := provider.clock().Sub(time.Unix(seconds, 0))
	if age < 0 {
		age = -age
	}
	if age > provider.maxAge {
		return WebhookEvent{}, ErrInvalidSignature
	}
	provided, err := hex.DecodeString(strings.ToLower(strings.TrimSpace(headers.Get("X-Signature-V2"))))
	if err != nil || len(provided) != sha256.Size {
		return WebhookEvent{}, ErrInvalidSignature
	}
	mac := hmac.New(sha256.New, []byte(provider.webhookSecret))
	_, _ = mac.Write([]byte(timestamp + "."))
	_, _ = mac.Write(body)
	if !hmac.Equal(provided, mac.Sum(nil)) {
		return WebhookEvent{}, ErrInvalidSignature
	}
	var payment tabpayPayment
	if err := json.Unmarshal(body, &payment); err != nil {
		return WebhookEvent{}, fmt.Errorf("%w: malformed TabPay webhook", ErrProviderResponse)
	}
	status := strings.ToUpper(strings.TrimSpace(payment.Status))
	if payment.ID == "" || payment.OrderID == "" || status == "" {
		return WebhookEvent{}, fmt.Errorf("%w: TabPay webhook has no payment, order or status", ErrProviderResponse)
	}
	intent := tabpayIntent(payment)
	if status == "SUCCESS" && intent.Amount.Currency == "" && !tabpayTestFlag(payment.Test) {
		return WebhookEvent{}, fmt.Errorf("%w: TabPay success without amountKopecks", ErrProviderResponse)
	}
	return WebhookEvent{
		ID:                strings.ToLower(status) + ":" + payment.ID,
		Type:              "payment." + strings.ToLower(status),
		ProviderReference: payment.ID,
		MerchantReference: payment.OrderID,
		Status:            intent.Status,
		Amount:            intent.Amount,
		OccurredAt:        time.Unix(seconds, 0).UTC(),
	}, nil
}

// Probe looks up an order ID that cannot exist. TabPay authenticates the key
// before searching, so a 404 means the key works; nothing is created.
func (provider *TabPay) Probe(ctx context.Context) error {
	nonce := make([]byte, 8)
	_, _ = rand.Read(nonce)
	_, _, err := provider.Recover(ctx, "omniflow-probe-"+hex.EncodeToString(nonce))
	return err
}

func (provider *TabPay) do(ctx context.Context, method, path string, query url.Values, body, target any) error {
	headers := make(http.Header)
	headers.Set("X-Api-Key", provider.apiKey)
	endpoint := provider.baseURL + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	return doJSON(ctx, provider.http, method, endpoint, headers, body, target)
}

func tabpayIntent(payment tabpayPayment) Intent {
	intent := Intent{
		ProviderReference: payment.ID,
		Status:            tabpayStatus(payment.Status, tabpayTestFlag(payment.IsTest) || tabpayTestFlag(payment.Test)),
		CheckoutURL:       payment.PayURL,
	}
	if payment.AmountKopecks != nil {
		if amount, err := strconv.ParseInt(payment.AmountKopecks.String(), 10, 64); err == nil && amount > 0 {
			intent.Amount = commerce.Money{Amount: amount, Currency: "RUB"}
		}
	}
	if intent.Status == "succeeded" && intent.Amount.Currency == "" {
		// A success whose amount cannot be read is not one Omniflow can verify.
		intent.Status = "processing"
	}
	return intent
}

// tabpayStatus normalises TabPay's statuses. A test payment never succeeds.
// FAILED and EXPIRED are not final at TabPay — an SBP QR outlives the
// timeout, and a late payment arrives as a separate SUCCESS — which Omniflow
// settles as a late payment.
func tabpayStatus(status string, test bool) string {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "CREATED":
		return "pending"
	case "PENDING":
		return "processing"
	case "SUCCESS":
		if test {
			return "failed"
		}
		return "succeeded"
	case "FAILED":
		return "failed"
	case "EXPIRED":
		return "expired"
	case "REFUNDED", "CANCELED":
		return "cancelled"
	default:
		return "pending"
	}
}

// tabpayTestFlag reads `test`/`isTest`. Only an explicit yes marks a sandbox
// payment; anything unrecognised is treated as live, because wrongly marking
// a real payment as a test would leave a paying customer uncredited.
func tabpayTestFlag(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "true", "1", "yes", "y":
			return true
		}
	case float64:
		return typed == 1
	}
	return false
}

func tabpayTruncate(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

var _ Provider = (*TabPay)(nil)
