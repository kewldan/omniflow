package payments

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/omniflow/omniflow/internal/commerce"
)

// CloudPayments settles card payments through the CloudPayments REST API.
//
// A checkout is an order created with `/orders/create`, whose `InvoiceId` is
// Omniflow's order ID. Every later lookup — notifications, `/payments/find`,
// refunds — is keyed by that invoice ID, so it doubles as the provider
// reference: a notification maps back to the intent without a second
// identifier, and polling needs nothing the order did not already carry.
type CloudPayments struct {
	publicID string
	secret   string
	currency string
	testMode bool
	baseURL  string
	http     *http.Client
	clock    func() time.Time
}

const cloudPaymentsDefaultURL = "https://api.cloudpayments.ru"

func buildCloudPayments(settings Settings) (Provider, error) {
	publicID := settings.String("APP_CLOUDPAYMENTS_PUBLIC_ID")
	secret := settings.String("APP_CLOUDPAYMENTS_API_SECRET")
	if publicID == "" && secret == "" {
		return nil, ErrNotConfigured
	}
	if publicID == "" || secret == "" {
		return nil, errors.New("CloudPayments needs both APP_CLOUDPAYMENTS_PUBLIC_ID and APP_CLOUDPAYMENTS_API_SECRET")
	}
	currency := strings.ToUpper(settings.String("APP_CLOUDPAYMENTS_CURRENCY"))
	if currency == "" {
		currency = "RUB"
	}
	if _, err := currencyExponent(currency); err != nil {
		return nil, fmt.Errorf("APP_CLOUDPAYMENTS_CURRENCY %q is not a currency Omniflow can price in", currency)
	}
	baseURL := strings.TrimSuffix(settings.String("APP_CLOUDPAYMENTS_API_URL"), "/")
	if baseURL == "" {
		baseURL = cloudPaymentsDefaultURL
	}
	return &CloudPayments{
		publicID: publicID, secret: secret, currency: currency,
		testMode: settings.Bool("APP_CLOUDPAYMENTS_TEST_MODE", false),
		baseURL:  baseURL, http: tracedClient(30 * time.Second), clock: time.Now,
	}, nil
}

func (provider *CloudPayments) Name() string { return "cloudpayments" }

// Capabilities declares refunds (full and partial, by transaction) and
// polling by invoice ID. Saved-card charging is not declared: CloudPayments
// binds a token to the `AccountId` it was issued for, which a saved-method
// charge in Omniflow does not carry.
func (provider *CloudPayments) Capabilities() Capabilities {
	return Capabilities{Refunds: true, PartialRefund: true, Webhooks: true, Polling: true}
}

// SupportsCurrency accepts only the currency the site is configured to settle.
func (provider *CloudPayments) SupportsCurrency(currency string) bool {
	return strings.ToUpper(strings.TrimSpace(currency)) == provider.currency
}

type cloudPaymentsEnvelope struct {
	Success bool            `json:"Success"`
	Message string          `json:"Message"`
	Model   json.RawMessage `json:"Model"`
}

type cloudPaymentsTransaction struct {
	TransactionID json.Number `json:"TransactionId"`
	Amount        json.Number `json:"Amount"`
	Currency      string      `json:"Currency"`
	Status        string      `json:"Status"`
	InvoiceID     string      `json:"InvoiceId"`
}

func (provider *CloudPayments) Create(ctx context.Context, request CreateRequest) (Intent, error) {
	if !provider.SupportsCurrency(request.Amount.Currency) {
		return Intent{}, commerce.ErrCurrencyMismatch
	}
	amount, err := cloudPaymentsAmount(request.Amount)
	if err != nil {
		return Intent{}, err
	}
	body := map[string]any{
		"Amount":      amount,
		"Currency":    provider.currency,
		"Description": request.Description,
		"InvoiceId":   request.OrderID,
		"JsonData":    map[string]string{"invoice_id": request.OrderID},
	}
	if request.ReturnURL != "" {
		body["SuccessRedirectUrl"] = request.ReturnURL
		body["FailRedirectUrl"] = request.ReturnURL
	}
	var envelope cloudPaymentsEnvelope
	if err := provider.call(ctx, "/orders/create", "", body, &envelope); err != nil {
		return Intent{}, err
	}
	var order struct {
		ID  string `json:"Id"`
		URL string `json:"Url"`
	}
	if !envelope.Success || json.Unmarshal(envelope.Model, &order) != nil || order.URL == "" {
		return Intent{}, fmt.Errorf("%w: orders/create was not accepted", ErrProviderResponse)
	}
	return Intent{ProviderReference: request.OrderID, Status: "pending", CheckoutURL: order.URL, Amount: request.Amount}, nil
}

// Poll looks the payment up by invoice ID. CloudPayments answers
// `Success: false` until a transaction exists for the invoice, which is an
// open checkout rather than an error.
func (provider *CloudPayments) Poll(ctx context.Context, reference string) (Intent, error) {
	transaction, found, err := provider.find(ctx, reference)
	if err != nil {
		return Intent{}, err
	}
	if !found {
		return Intent{ProviderReference: reference, Status: "pending"}, nil
	}
	money, err := cloudPaymentsMoney(transaction.Amount.String(), transaction.Currency)
	if err != nil {
		return Intent{}, err
	}
	return Intent{ProviderReference: reference, Status: normalizeCloudPaymentsStatus(transaction.Status), Amount: money}, nil
}

// Refund returns money from the transaction the invoice settled through.
//
// The caller's idempotency key travels as `X-Request-ID`, which CloudPayments
// uses to answer a repeated request with the original result instead of
// refunding twice.
func (provider *CloudPayments) Refund(ctx context.Context, request RefundRequest) (Refund, error) {
	if !provider.SupportsCurrency(request.Amount.Currency) {
		return Refund{}, commerce.ErrCurrencyMismatch
	}
	amount, err := cloudPaymentsAmount(request.Amount)
	if err != nil {
		return Refund{}, err
	}
	transaction, found, err := provider.find(ctx, request.PaymentReference)
	if err != nil {
		return Refund{}, err
	}
	if !found || transaction.TransactionID == "" {
		return Refund{}, fmt.Errorf("%w: no transaction for invoice", ErrProviderResponse)
	}
	body := map[string]any{"TransactionId": transaction.TransactionID, "Amount": amount}
	var envelope cloudPaymentsEnvelope
	if err := provider.call(ctx, "/payments/refund", request.IdempotencyKey, body, &envelope); err != nil {
		return Refund{}, err
	}
	if !envelope.Success {
		return Refund{}, fmt.Errorf("%w: refund was not accepted", ErrProviderResponse)
	}
	var refund struct {
		TransactionID json.Number `json:"TransactionId"`
	}
	_ = json.Unmarshal(envelope.Model, &refund)
	reference := refund.TransactionID.String()
	if reference == "" {
		reference = request.IdempotencyKey
	}
	return Refund{ProviderReference: reference, Status: "succeeded", Amount: request.Amount}, nil
}

// VerifyWebhook checks the HMAC CloudPayments puts on every notification and
// classifies it the way the provider's single notification URL requires.
//
// The signature is base64(HMAC-SHA256(API secret, body)), taken from
// `X-Content-HMAC` or else `Content-HMAC`. CloudPayments computes one of them
// over the URL-encoded body and the other over the decoded body, so both
// forms are tried.
//
// One URL receives Check, Pay, and Fail notifications. Pay carries `Reason`
// or `AuthCode`; Check carries neither and arrives before the card is
// charged, so it must never settle anything.
func (provider *CloudPayments) VerifyWebhook(headers http.Header, body []byte) (WebhookEvent, error) {
	signature := headers.Get("X-Content-HMAC")
	if signature == "" {
		signature = headers.Get("Content-HMAC")
	}
	if !cloudPaymentsSignatureValid(provider.secret, body, signature) {
		return WebhookEvent{}, ErrInvalidSignature
	}
	fields, err := cloudPaymentsFields(body)
	if err != nil {
		return WebhookEvent{}, err
	}
	transactionID := fields["TransactionId"]
	if transactionID == "" {
		return WebhookEvent{}, ErrProviderResponse
	}
	status := fields["Status"]
	kind, normalized := "check", "pending"
	paid := fields["Reason"] != "" || fields["AuthCode"] != ""
	switch {
	case status == "Declined" || status == "Cancelled":
		kind, normalized = "fail", "failed"
	case status == "Completed" && paid:
		kind, normalized = "pay", "succeeded"
	case status == "Authorized" && paid:
		kind, normalized = "authorized", "processing"
	}
	testPayment := fields["TestMode"] == "1" || strings.EqualFold(fields["TestMode"], "true")
	if testPayment && !provider.testMode && normalized == "succeeded" {
		// A test-card payment on a live installation moved no money. It is
		// recorded and acknowledged, never settled.
		kind, normalized = "test", "failed"
	}
	event := WebhookEvent{
		ID: kind + ":" + transactionID, Type: kind,
		ProviderReference: fields["InvoiceId"], MerchantReference: fields["InvoiceId"],
		Status: normalized, OccurredAt: provider.clock().UTC(),
	}
	if fields["Amount"] != "" {
		currency := fields["Currency"]
		if currency == "" {
			currency = provider.currency
		}
		money, err := cloudPaymentsMoney(fields["Amount"], currency)
		if err != nil {
			return WebhookEvent{}, err
		}
		event.Amount = money
	}
	return event, nil
}

// WebhookAck answers `{"code":0}`, the only body CloudPayments accepts as
// "notification received"; anything else is retried and, for Check, refuses
// the payment.
func (provider *CloudPayments) WebhookAck(string) (int, string, []byte) {
	return http.StatusOK, "application/json", []byte(`{"code":0}`)
}

// Probe asks `/payments/find` for an invoice that cannot exist. Valid
// credentials get `Success: false`; wrong ones are rejected with HTTP 401.
// Nothing is created.
func (provider *CloudPayments) Probe(ctx context.Context) error {
	_, _, err := provider.find(ctx, "omniflow-connection-probe")
	return err
}

func (provider *CloudPayments) find(ctx context.Context, invoiceID string) (cloudPaymentsTransaction, bool, error) {
	var envelope cloudPaymentsEnvelope
	if err := provider.call(ctx, "/payments/find", "", map[string]string{"InvoiceId": invoiceID}, &envelope); err != nil {
		return cloudPaymentsTransaction{}, false, err
	}
	if !envelope.Success {
		return cloudPaymentsTransaction{}, false, nil
	}
	var transaction cloudPaymentsTransaction
	decoder := json.NewDecoder(bytes.NewReader(envelope.Model))
	decoder.UseNumber()
	if err := decoder.Decode(&transaction); err != nil {
		return cloudPaymentsTransaction{}, false, fmt.Errorf("%w: unreadable payment", ErrProviderResponse)
	}
	if transaction.InvoiceID != "" && transaction.InvoiceID != invoiceID {
		return cloudPaymentsTransaction{}, false, fmt.Errorf("%w: invoice mismatch", ErrProviderResponse)
	}
	return transaction, true, nil
}

func (provider *CloudPayments) call(ctx context.Context, path, requestID string, body, target any) error {
	headers := make(http.Header)
	headers.Set("Authorization", "Basic "+basicAuth(provider.publicID, provider.secret))
	if requestID != "" {
		headers.Set("X-Request-ID", requestID)
	}
	return doJSON(ctx, provider.http, http.MethodPost, provider.baseURL+path, headers, body, target)
}

func cloudPaymentsSignatureValid(secret string, body []byte, signature string) bool {
	if secret == "" || signature == "" {
		return false
	}
	provided := []byte(strings.TrimSpace(signature))
	sign := func(data []byte) []byte {
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write(data)
		return []byte(base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	}
	if hmac.Equal(sign(body), provided) {
		return true
	}
	decoded, err := url.QueryUnescape(string(body))
	return err == nil && hmac.Equal(sign([]byte(decoded)), provided)
}

// cloudPaymentsFields reads a notification. CloudPayments posts
// `application/x-www-form-urlencoded` by default and JSON when the site is
// switched to it; both are flattened to strings.
func cloudPaymentsFields(body []byte) (map[string]string, error) {
	trimmed := bytes.TrimSpace(body)
	fields := make(map[string]string)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		var raw map[string]any
		decoder := json.NewDecoder(bytes.NewReader(trimmed))
		decoder.UseNumber()
		if err := decoder.Decode(&raw); err != nil {
			return nil, ErrProviderResponse
		}
		for key, value := range raw {
			switch typed := value.(type) {
			case string:
				fields[key] = typed
			case json.Number:
				fields[key] = typed.String()
			case bool:
				fields[key] = fmt.Sprint(typed)
			}
		}
		return fields, nil
	}
	values, err := url.ParseQuery(string(trimmed))
	if err != nil {
		return nil, ErrProviderResponse
	}
	for key := range values {
		fields[key] = values.Get(key)
	}
	return fields, nil
}

// cloudPaymentsAmount is the decimal major-unit amount CloudPayments expects,
// sent as a JSON number.
func cloudPaymentsAmount(money commerce.Money) (json.Number, error) {
	exponent, err := currencyExponent(money.Currency)
	if err != nil {
		return "", err
	}
	return json.Number(formatMinor(money.Amount, exponent)), nil
}

func cloudPaymentsMoney(amount, currency string) (commerce.Money, error) {
	currency = strings.ToUpper(strings.TrimSpace(currency))
	exponent, err := currencyExponent(currency)
	if err != nil {
		return commerce.Money{}, err
	}
	minor, err := parseMinor(strings.TrimSpace(amount), exponent)
	if err != nil {
		return commerce.Money{}, err
	}
	return commerce.Money{Amount: minor, Currency: currency}, nil
}

// normalizeCloudPaymentsStatus maps a CloudPayments transaction status.
// `Authorized` is a two-stage hold that has not been captured, so it is not
// money received; Omniflow never captures it, and a site must use one-stage
// payments.
func normalizeCloudPaymentsStatus(status string) string {
	switch status {
	case "Completed":
		return "succeeded"
	case "Authorized":
		return "processing"
	case "AwaitingAuthentication":
		return "requires_action"
	case "Declined":
		return "failed"
	case "Cancelled":
		return "cancelled"
	default:
		return "pending"
	}
}

var (
	_ Provider            = (*CloudPayments)(nil)
	_ WebhookAcknowledger = (*CloudPayments)(nil)
	_ CurrencySupport     = (*CloudPayments)(nil)
	_ ConnectionProbe     = (*CloudPayments)(nil)
)
