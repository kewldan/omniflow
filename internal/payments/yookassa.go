package payments

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
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
	receipt YooKassaReceipt
	// sbp offers SBP as its own button beside the default checkout page.
	sbp bool
	// testMode accepts payments YooKassa marks `test: true`. Off, such a
	// payment never settles: test-shop money must not buy real access.
	testMode bool
}

// WithModes sets the SBP button (APP_YOOKASSA_SBP_ENABLED) and test mode
// (APP_YOOKASSA_TEST_MODE).
func (provider *YooKassa) WithModes(sbp, testMode bool) *YooKassa {
	provider.sbp, provider.testMode = sbp, testMode
	return provider
}

// Methods offers the default checkout and, when enabled, SBP.
func (provider *YooKassa) Methods() []string {
	if provider.sbp {
		return []string{"", "sbp"}
	}
	return nil
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
	Test         bool      `json:"test"`
	Confirmation struct {
		URL string `json:"confirmation_url"`
	} `json:"confirmation"`
	PaymentMethod *yooPaymentMethod `json:"payment_method"`
	// RefundedAmount is what has been returned on this payment so far,
	// including refunds made from the YooKassa dashboard.
	RefundedAmount *yooMoney `json:"refunded_amount"`
	// PaymentID is set on a refund object: the payment it returned money
	// from. A refund notification is routed to that payment.
	PaymentID string `json:"payment_id"`
	// Metadata is what the creator of the payment attached: `order_id` for an
	// Omniflow payment, the previous bot's own keys for one it created.
	Metadata map[string]any `json:"metadata"`
}

// stringMetadata flattens YooKassa metadata, whose values are strings in
// practice but arrive as JSON values.
func stringMetadata(raw map[string]any) map[string]string {
	if len(raw) == 0 {
		return nil
	}
	out := make(map[string]string, len(raw))
	for key, value := range raw {
		switch typed := value.(type) {
		case string:
			out[key] = typed
		case float64:
			out[key] = strconv.FormatFloat(typed, 'f', -1, 64)
		case bool:
			out[key] = strconv.FormatBool(typed)
		}
	}
	return out
}

// yooPaymentMethod is the instrument a payment used. `saved` is true only when
// the payment was created with save_payment_method and the customer's bank
// allowed the binding; `id` is then the token a later charge presents.
type yooPaymentMethod struct {
	Type  string `json:"type"`
	ID    string `json:"id"`
	Saved bool   `json:"saved"`
	Title string `json:"title"`
	Card  *struct {
		Last4    string `json:"last4"`
		CardType string `json:"card_type"`
	} `json:"card"`
}

// label is the masked description stored beside a saved method.
func (method yooPaymentMethod) label() string {
	if method.Card != nil && method.Card.Last4 != "" {
		label := strings.TrimSpace(method.Card.CardType + " *" + method.Card.Last4)
		return truncateLabel(label)
	}
	if method.Title != "" {
		return truncateLabel(method.Title)
	}
	return truncateLabel(method.Type)
}

// truncateLabel bounds a provider-supplied label to the column's limit.
func truncateLabel(label string) string {
	runes := []rune(strings.TrimSpace(label))
	if len(runes) > 64 {
		runes = runes[:64]
	}
	return string(runes)
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
	if request.Method == "sbp" && provider.sbp {
		body["payment_method_data"] = map[string]string{"type": "sbp"}
	}
	if request.SavePaymentMethod {
		// Only on the customer's explicit consent. YooKassa then returns
		// payment_method.saved=true once the bank allowed the binding.
		body["save_payment_method"] = true
	}
	if receipt := provider.receiptBody(request.Description, formatMinor(request.Amount.Amount, exponent), request.Amount.Currency, request.Metadata); receipt != nil {
		body["receipt"] = receipt
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
	reference := notification.Object.ID
	if strings.HasPrefix(notification.Event, "refund.") && notification.Object.PaymentID != "" {
		// A refund made from the YooKassa dashboard: route it to the
		// payment. The poll that follows reads the payment's refunded total.
		reference = notification.Object.PaymentID
	}
	return WebhookEvent{ID: notification.Event + ":" + notification.Object.ID + ":" + hex.EncodeToString(digest[:8]), Type: notification.Event, ProviderReference: reference, Status: normalizeYooStatus(notification.Object.Status), Amount: commerce.Money{Amount: amount, Currency: notification.Object.Amount.Currency}, OccurredAt: provider.clock().UTC(), Metadata: stringMetadata(notification.Object.Metadata)}, nil
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
	intent := Intent{ProviderReference: payment.ID, Status: normalizeYooStatus(payment.Status), CheckoutURL: payment.Confirmation.URL, Amount: commerce.Money{Amount: amount, Currency: payment.Amount.Currency}, Metadata: stringMetadata(payment.Metadata)}
	if payment.Test && !provider.testMode && intent.Status == "succeeded" {
		// A payment made against a test shop moved no money. Outside test
		// mode it is reported failed so it can never buy anything.
		intent.Status = "failed"
	}
	if method := payment.PaymentMethod; method != nil && method.Saved && method.ID != "" && intent.Status == "succeeded" {
		intent.BoundMethod = &BoundMethod{Token: method.ID, Label: method.label()}
	}
	if refunded := payment.RefundedAmount; refunded != nil && intent.Status == "succeeded" && refunded.Currency == payment.Amount.Currency {
		if minor, refundErr := parseMinor(refunded.Value, exponent); refundErr == nil && minor > 0 {
			intent.RefundedMinor = minor
			if minor >= amount {
				intent.Status = StatusRefunded
			}
		}
	}
	return intent, nil
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

// UsesCustomerContact asks for the payer's verified e-mail when fiscal
// receipts are on: the receipt goes to the customer rather than to the
// installation-wide fallback address.
func (provider *YooKassa) UsesCustomerContact() bool { return provider.receipt.Enabled }

// WebhooksNeedPolling marks YooKassa notifications as unsigned hints: the
// payment service re-fetches the payment before applying one.
func (provider *YooKassa) WebhooksNeedPolling() bool { return true }

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
	if receipt := provider.receiptBody(request.Description, formatMinor(request.Amount.Amount, exponent), request.Amount.Currency, request.Metadata); receipt != nil {
		body["receipt"] = receipt
	}
	var payment yooPayment
	if err := provider.do(
		ctx, http.MethodPost, "/payments", request.IdempotencyKey, body, &payment,
	); err != nil {
		return Intent{}, err
	}
	return provider.intent(payment)
}
