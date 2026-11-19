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
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/omniflow/omniflow/internal/commerce"
)

// CisPay is the hosted-checkout adapter for the CisPay Merchant API
// (api.cispay.app). Money is integer kopeks on the wire; the shop settles in
// RUB only.
//
// A notification is signed with the API key, but Omniflow still re-reads the
// payment through the authenticated status endpoint before applying it: a
// notification may omit `is_sandbox`, and a crypto payment may report its
// settled amount in USD cents without the rouble source amount. Only the
// status endpoint answers both questions.
type CisPay struct {
	shopID  string
	apiKey  string
	method  string
	baseURL string
	http    *http.Client
	clock   func() time.Time
}

const cispayDefaultBaseURL = "https://api.cispay.app"

// cispayCardMinimum is CisPay's floor for a card payment, in kopeks.
const cispayCardMinimum = 5000

var cispayIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func buildCisPay(settings Settings) (Provider, error) {
	shopID := settings.String("APP_CISPAY_SHOP_ID")
	apiKey := settings.String("APP_CISPAY_API_KEY")
	if shopID == "" && apiKey == "" {
		return nil, ErrNotConfigured
	}
	if shopID == "" || apiKey == "" {
		return nil, errors.New("CisPay needs both APP_CISPAY_SHOP_ID and APP_CISPAY_API_KEY")
	}
	method := strings.ToUpper(settings.String("APP_CISPAY_PAYMENT_METHOD"))
	switch method {
	case "", "CARD", "SBP", "CRYPTO":
	default:
		return nil, fmt.Errorf("APP_CISPAY_PAYMENT_METHOD must be CARD, SBP or CRYPTO, got %q", method)
	}
	baseURL := strings.TrimSuffix(settings.String("APP_CISPAY_BASE_URL"), "/")
	if baseURL == "" {
		baseURL = cispayDefaultBaseURL
	}
	return &CisPay{
		shopID: shopID, apiKey: apiKey, method: method, baseURL: baseURL,
		http: tracedClient(30 * time.Second), clock: time.Now,
	}, nil
}

func (provider *CisPay) Name() string { return "cispay" }

// Capabilities declares no refunds and no recurring charges: the Merchant API
// offers neither a refund call nor a charge against a saved method. CisPay
// subscriptions are schedules CisPay itself runs, which is not what
// RecurringCharger means.
func (provider *CisPay) Capabilities() Capabilities {
	return Capabilities{Webhooks: true, Polling: true}
}

// SupportsCurrency reports RUB only: invoices are created in roubles.
func (provider *CisPay) SupportsCurrency(currency string) bool {
	return strings.EqualFold(strings.TrimSpace(currency), "RUB")
}

// WebhooksNeedPolling makes the payment service confirm every signed
// notification through GET /payments/status before money moves. That is the
// only answer that carries the sandbox flag and the rouble source amount of a
// crypto payment.
func (provider *CisPay) WebhooksNeedPolling() bool { return true }

type cispayPayment struct {
	ID               string       `json:"id"`
	OrderID          *string      `json:"order_id"`
	StoreID          *string      `json:"store_id"`
	Status           string       `json:"status"`
	Amount           json.Number  `json:"amount"`
	Currency         string       `json:"currency"`
	PaymentMethod    string       `json:"payment_method"`
	SourceAmount     *json.Number `json:"source_amount"`
	SourceCurrency   string       `json:"source_currency"`
	IsSandbox        *bool        `json:"is_sandbox"`
	BlockReason      *string      `json:"block_reason"`
	PaymentURL       string       `json:"payment_url"`
	ExpiresInSeconds *int64       `json:"expires_in_seconds"`
	Event            *string      `json:"event"`
	Object           string       `json:"object"`
	SubscriptionID   *string      `json:"subscription_id"`
}

func (provider *CisPay) Create(ctx context.Context, request CreateRequest) (Intent, error) {
	if !provider.SupportsCurrency(request.Amount.Currency) {
		return Intent{}, fmt.Errorf("%w: CisPay settles RUB only", ErrUnsupported)
	}
	if request.Amount.Amount <= 0 || request.Amount.Amount > 2_147_483_647 {
		return Intent{}, fmt.Errorf("%w: CisPay amount is out of range", ErrUnsupported)
	}
	if provider.method == "CARD" && request.Amount.Amount < cispayCardMinimum {
		return Intent{}, fmt.Errorf("%w: CisPay card payments start at 50.00 RUB", ErrUnsupported)
	}
	body := map[string]any{
		"amount":   request.Amount.Amount,
		"currency": "RUB",
		"order_id": request.OrderID,
	}
	if provider.method != "" {
		body["payment_method"] = provider.method
	}
	// The hosted chooser and SBP require a customer identifier. Omniflow does
	// not hand a customer ID to adapters, so the order ID stands in, as
	// Bedolaga does for a buyer with no account.
	customer := strings.TrimSpace(request.Metadata["customer_id"])
	if customer == "" {
		customer = request.OrderID
	}
	body["customer_id"] = cispayTruncate(customer, 255)
	if request.Description != "" {
		body["description"] = cispayTruncate(request.Description, 512)
	}
	if request.ReturnURL != "" && len(request.ReturnURL) <= 1024 {
		body["redirect_success_url"] = request.ReturnURL
		body["redirect_fail_url"] = request.ReturnURL
	}
	var payment cispayPayment
	if err := provider.do(ctx, http.MethodPost, "/payments", nil, body, &payment); err != nil {
		return Intent{}, err
	}
	if payment.OrderID != nil && *payment.OrderID != request.OrderID {
		return Intent{}, fmt.Errorf("%w: CisPay answered for another order", ErrProviderResponse)
	}
	if payment.StoreID != nil && *payment.StoreID != provider.shopID {
		return Intent{}, fmt.Errorf("%w: CisPay answered for another store", ErrProviderResponse)
	}
	checkout, err := url.Parse(payment.PaymentURL)
	if err != nil || checkout.Scheme != "https" || checkout.Host == "" || checkout.User != nil {
		return Intent{}, fmt.Errorf("%w: CisPay returned no usable payment URL", ErrProviderResponse)
	}
	if payment.Currency == "" {
		payment.Currency = "RUB"
	}
	return provider.intent(payment)
}

// Poll reads GET /payments/status by CisPay's payment ID. The status schema
// defaults `is_sandbox` to false, so an absent flag here is a real payment.
func (provider *CisPay) Poll(ctx context.Context, reference string) (Intent, error) {
	if !cispayIDPattern.MatchString(reference) {
		return Intent{}, fmt.Errorf("%w: invalid CisPay payment ID", ErrProviderResponse)
	}
	var payment cispayPayment
	query := url.Values{"id": {reference}}
	if err := provider.do(ctx, http.MethodGet, "/payments/status", query, nil, &payment); err != nil {
		return Intent{}, err
	}
	if payment.ID != reference {
		return Intent{}, fmt.Errorf("%w: CisPay status is for another payment", ErrProviderResponse)
	}
	if payment.Currency == "" {
		payment.Currency = "RUB"
	}
	if payment.IsSandbox == nil {
		sandbox := false
		payment.IsSandbox = &sandbox
	}
	return provider.intent(payment)
}

func (provider *CisPay) Refund(context.Context, RefundRequest) (Refund, error) {
	return Refund{}, ErrUnsupported
}

// VerifyWebhook checks X-Signature, a hex HMAC-SHA256 of the exact body keyed
// with the API key, and then parses the payment notification.
//
// Subscription lifecycle and subscription charge notifications are refused:
// Omniflow never opens a CisPay subscription, so one arriving here does not
// belong to an order it can settle.
func (provider *CisPay) VerifyWebhook(headers http.Header, body []byte) (WebhookEvent, error) {
	provided, err := hex.DecodeString(strings.TrimSpace(headers.Get("X-Signature")))
	if err != nil || len(provided) != sha256.Size {
		return WebhookEvent{}, ErrInvalidSignature
	}
	mac := hmac.New(sha256.New, []byte(provider.apiKey))
	_, _ = mac.Write(body)
	if !hmac.Equal(provided, mac.Sum(nil)) {
		return WebhookEvent{}, ErrInvalidSignature
	}
	var payment cispayPayment
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&payment); err != nil {
		return WebhookEvent{}, fmt.Errorf("%w: malformed CisPay notification", ErrProviderResponse)
	}
	if payment.Event != nil || payment.SubscriptionID != nil || payment.Object == "subscription" || headers.Get("X-CISPay-Event") != "" {
		return WebhookEvent{}, fmt.Errorf("%w: CisPay subscription notifications", ErrUnsupported)
	}
	if !cispayIDPattern.MatchString(payment.ID) || payment.OrderID == nil || *payment.OrderID == "" {
		return WebhookEvent{}, fmt.Errorf("%w: CisPay notification has no payment or order ID", ErrProviderResponse)
	}
	if payment.StoreID == nil || *payment.StoreID != provider.shopID {
		return WebhookEvent{}, fmt.Errorf("%w: CisPay notification is for another store", ErrProviderResponse)
	}
	if payment.Currency == "" {
		payment.Currency = "RUB"
	}
	intent, err := provider.intent(payment)
	if err != nil {
		return WebhookEvent{}, err
	}
	return WebhookEvent{
		ID:                strings.ToLower(payment.Status) + ":" + payment.ID,
		Type:              "payment." + strings.ToLower(payment.Status),
		ProviderReference: payment.ID,
		MerchantReference: *payment.OrderID,
		Status:            intent.Status,
		Amount:            intent.Amount,
		OccurredAt:        provider.clock().UTC(),
	}, nil
}

// Probe reads GET /store/capabilities, which describes the shop and moves
// nothing. It fails when the key belongs to another shop or the shop is
// inactive, since either way no checkout would open.
func (provider *CisPay) Probe(ctx context.Context) error {
	var capabilities struct {
		StoreID  string `json:"store_id"`
		IsActive bool   `json:"is_active"`
	}
	if err := provider.do(ctx, http.MethodGet, "/store/capabilities", nil, nil, &capabilities); err != nil {
		return err
	}
	if capabilities.StoreID != provider.shopID {
		return fmt.Errorf("%w: the API key belongs to another CisPay store", ErrProviderResponse)
	}
	if !capabilities.IsActive {
		return fmt.Errorf("%w: the CisPay store is not active", ErrProviderResponse)
	}
	return nil
}

func (provider *CisPay) do(ctx context.Context, method, path string, query url.Values, body, target any) error {
	headers := make(http.Header)
	headers.Set("X-Shop-ID", provider.shopID)
	headers.Set("X-Api-Key", provider.apiKey)
	endpoint := provider.baseURL + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	return doJSON(ctx, provider.http, method, endpoint, headers, body, target)
}

// intent maps a CisPay payment object. A crypto payment reports `amount` in
// USD cents; what the customer owes is the rouble `source_amount`, so that is
// the amount Omniflow settles against when it is present.
func (provider *CisPay) intent(payment cispayPayment) (Intent, error) {
	status, err := cispayStatus(payment)
	if err != nil {
		return Intent{}, err
	}
	raw, currency := payment.Amount, strings.ToUpper(payment.Currency)
	if payment.PaymentMethod == "CRYPTO" && payment.SourceAmount != nil {
		raw, currency = *payment.SourceAmount, strings.ToUpper(payment.SourceCurrency)
	}
	amount, err := strconv.ParseInt(raw.String(), 10, 64)
	if err != nil || amount <= 0 {
		return Intent{}, fmt.Errorf("%w: invalid CisPay amount", ErrProviderResponse)
	}
	intent := Intent{
		ProviderReference: payment.ID,
		Status:            status,
		CheckoutURL:       payment.PaymentURL,
		Amount:            commerce.Money{Amount: amount, Currency: currency},
	}
	if payment.ExpiresInSeconds != nil && *payment.ExpiresInSeconds >= 0 && *payment.ExpiresInSeconds <= 31_536_000 {
		expires := provider.clock().UTC().Add(time.Duration(*payment.ExpiresInSeconds) * time.Second)
		intent.ExpiresAt = &expires
	}
	return intent, nil
}

// cispayStatus normalises a CisPay status. A sandbox payment or one carrying a
// block reason never moved real money, so it is reported failed rather than
// succeeded. REFUNDED is reported cancelled: the money went back, and an
// operator reconciles any credit already granted.
func cispayStatus(payment cispayPayment) (string, error) {
	switch payment.Status {
	case "PENDING":
		return "pending", nil
	case "PAID":
		if (payment.IsSandbox != nil && *payment.IsSandbox) || (payment.BlockReason != nil && *payment.BlockReason != "") {
			return "failed", nil
		}
		return "succeeded", nil
	case "FAILED":
		return "failed", nil
	case "EXPIRED":
		return "expired", nil
	case "REFUNDED":
		return "cancelled", nil
	default:
		return "", fmt.Errorf("%w: unknown CisPay status", ErrProviderResponse)
	}
}

func cispayTruncate(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

var _ Provider = (*CisPay)(nil)
