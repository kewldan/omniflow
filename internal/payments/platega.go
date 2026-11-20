package payments

import (
	"bytes"
	"context"
	"crypto/hmac"
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
	plategaDefaultBaseURL = "https://app.platega.io"
	// plategaDescriptionLimit is Platega's description limit, in bytes.
	plategaDescriptionLimit = 64
	// plategaSubscriptionMethod is the recurring-subscription method, which
	// this adapter does not open.
	plategaSubscriptionMethod = 6
)

// Platega is the Platega (app.platega.io) adapter: one-off SBP, card and
// crypto payments.
//
// Every request authenticates with the X-MerchantId and X-Secret headers, and
// Platega authenticates its callbacks with the same two headers, compared here
// in constant time.
type Platega struct {
	merchantID    string
	secret        string
	baseURL       string
	apiVersion    string
	paymentMethod int
	// methodOrder and methodCodes are APP_PLATEGA_PAYMENT_METHODS.
	methodOrder []string
	methodCodes map[string]int
	currency    string
	returnURL   string
	failedURL   string
	http        *http.Client
	clock       func() time.Time
}

// buildPlatega reads APP_PLATEGA_* and constructs the adapter.
func buildPlatega(settings Settings) (Provider, error) {
	merchantID := settings.String("APP_PLATEGA_MERCHANT_ID")
	secret := settings.String("APP_PLATEGA_SECRET")
	if merchantID == "" && secret == "" {
		return nil, ErrNotConfigured
	}
	if merchantID == "" || secret == "" {
		return nil, errors.New("platega: APP_PLATEGA_MERCHANT_ID and APP_PLATEGA_SECRET must both be set")
	}
	baseURL := strings.TrimSuffix(settings.String("APP_PLATEGA_BASE_URL"), "/")
	if baseURL == "" {
		baseURL = plategaDefaultBaseURL
	}
	version := strings.ToLower(settings.String("APP_PLATEGA_API_VERSION"))
	// A base URL that already ends in the version is read as that version, as
	// the reference integration does, so /v2 is never appended twice.
	for _, candidate := range []string{"v1", "v2"} {
		if strings.HasSuffix(strings.ToLower(baseURL), "/"+candidate) {
			version = candidate
			baseURL = strings.TrimSuffix(baseURL[:len(baseURL)-len(candidate)-1], "/")
			break
		}
	}
	switch version {
	case "":
		version = "v1"
	case "v1", "v2":
	default:
		return nil, errors.New("platega: APP_PLATEGA_API_VERSION must be v1 or v2")
	}
	method := settings.Int("APP_PLATEGA_PAYMENT_METHOD", 2)
	if method <= 0 || method == plategaSubscriptionMethod {
		return nil, errors.New("platega: APP_PLATEGA_PAYMENT_METHOD must be a one-off payment method code")
	}
	methodOrder, methodCodes, err := parseMethodIDs("APP_PLATEGA_PAYMENT_METHODS",
		settings.String("APP_PLATEGA_PAYMENT_METHODS"), map[string]int{"sbp": 2, "card": 11, "intl": 12, "crypto": 13})
	if err != nil {
		return nil, fmt.Errorf("platega: %w", err)
	}
	for key, code := range methodCodes {
		if code == plategaSubscriptionMethod {
			return nil, fmt.Errorf("platega: APP_PLATEGA_PAYMENT_METHODS method %q names the subscription code", key)
		}
	}
	currency := strings.ToUpper(settings.String("APP_PLATEGA_CURRENCY"))
	if currency == "" {
		currency = "RUB"
	}
	if _, err := currencyExponent(currency); err != nil {
		return nil, fmt.Errorf("platega: unsupported APP_PLATEGA_CURRENCY %q", currency)
	}
	return &Platega{
		merchantID:    merchantID,
		secret:        secret,
		baseURL:       baseURL,
		apiVersion:    version,
		paymentMethod: method, methodOrder: methodOrder, methodCodes: methodCodes,
		currency:  currency,
		returnURL: settings.String("APP_PLATEGA_RETURN_URL"),
		failedURL: settings.String("APP_PLATEGA_FAILED_URL"),
		http:      tracedClient(30 * time.Second),
		clock:     time.Now,
	}, nil
}

func (provider *Platega) Name() string { return "platega" }

func (provider *Platega) Capabilities() Capabilities {
	return Capabilities{Webhooks: true, Polling: true}
}

// SupportsCurrency reports the one currency the merchant is configured for.
func (provider *Platega) SupportsCurrency(currency string) bool {
	return strings.EqualFold(strings.TrimSpace(currency), provider.currency)
}

// plategaAmount keeps a JSON number or string amount as its literal digits.
type plategaAmount string

func (amount *plategaAmount) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if bytes.Equal(trimmed, []byte("null")) {
		*amount = ""
		return nil
	}
	var text string
	if err := json.Unmarshal(trimmed, &text); err == nil {
		*amount = plategaAmount(text)
		return nil
	}
	var number json.Number
	if err := json.Unmarshal(trimmed, &number); err != nil {
		return err
	}
	*amount = plategaAmount(number.String())
	return nil
}

// plategaTransaction covers the create answer, GET /transaction/{id} and the
// callback body. The amount appears either at the top level or under
// paymentDetails, so both are read.
type plategaTransaction struct {
	ID             string          `json:"id"`
	TransactionID  string          `json:"transactionId"`
	Status         string          `json:"status"`
	Redirect       string          `json:"redirect"`
	URL            string          `json:"url"`
	ExpiresIn      json.RawMessage `json:"expiresIn"`
	Payload        string          `json:"payload"`
	Amount         plategaAmount   `json:"amount"`
	Currency       string          `json:"currency"`
	PaymentDetails *struct {
		Amount   plategaAmount `json:"amount"`
		Currency string        `json:"currency"`
	} `json:"paymentDetails"`
}

func (transaction plategaTransaction) reference() string {
	if transaction.TransactionID != "" {
		return transaction.TransactionID
	}
	return transaction.ID
}

func (transaction plategaTransaction) money() (commerce.Money, bool, error) {
	amount, currency := transaction.Amount, transaction.Currency
	if amount == "" && transaction.PaymentDetails != nil {
		amount, currency = transaction.PaymentDetails.Amount, transaction.PaymentDetails.Currency
	}
	if amount == "" {
		return commerce.Money{}, false, nil
	}
	currency = strings.ToUpper(strings.TrimSpace(currency))
	exponent, err := currencyExponent(currency)
	if err != nil {
		return commerce.Money{}, true, err
	}
	minor, err := parseMinor(strings.TrimSpace(string(amount)), exponent)
	if err != nil {
		return commerce.Money{}, true, err
	}
	return commerce.Money{Amount: minor, Currency: currency}, true, nil
}

func (provider *Platega) Create(ctx context.Context, request CreateRequest) (Intent, error) {
	if !provider.SupportsCurrency(request.Amount.Currency) {
		return Intent{}, fmt.Errorf("platega: currency %s is not configured", request.Amount.Currency)
	}
	if request.Amount.Amount <= 0 {
		return Intent{}, errors.New("platega: amount must be positive")
	}
	payer, err := plategaPayer(request.Metadata)
	if err != nil {
		return Intent{}, err
	}
	exponent, err := currencyExponent(provider.currency)
	if err != nil {
		return Intent{}, err
	}
	body := map[string]any{
		"paymentMethod": provider.methodFor(request.Method),
		"paymentDetails": map[string]any{
			"amount":   json.Number(formatMinor(request.Amount.Amount, exponent)),
			"currency": provider.currency,
		},
		"metadata": payer,
		"payload":  request.OrderID,
	}
	if description := plategaTrimBytes(strings.TrimSpace(request.Description), plategaDescriptionLimit); description != "" {
		body["description"] = description
	}
	returnURL := request.ReturnURL
	if returnURL == "" {
		returnURL = provider.returnURL
	}
	if returnURL != "" {
		body["return"] = returnURL
	}
	failedURL := provider.failedURL
	if failedURL == "" {
		failedURL = returnURL
	}
	if failedURL != "" {
		body["failedUrl"] = failedURL
	}
	path := "/transaction/process"
	if provider.apiVersion == "v2" {
		path = "/v2/transaction/process"
	}
	var transaction plategaTransaction
	if err := provider.do(ctx, http.MethodPost, path, body, &transaction); err != nil {
		return Intent{}, err
	}
	reference := transaction.reference()
	if reference == "" {
		return Intent{}, fmt.Errorf("%w: platega transaction without id", ErrProviderResponse)
	}
	checkout := transaction.Redirect
	if checkout == "" {
		checkout = transaction.URL
	}
	status := "pending"
	if transaction.Status != "" {
		status = plategaStatus(transaction.Status)
	}
	intent := Intent{ProviderReference: reference, Status: status, CheckoutURL: checkout, Amount: request.Amount}
	if lifetime, ok := plategaParseExpiresIn(transaction.ExpiresIn); ok {
		expires := provider.clock().UTC().Add(lifetime)
		intent.ExpiresAt = &expires
	}
	return intent, nil
}

func (provider *Platega) Poll(ctx context.Context, reference string) (Intent, error) {
	var transaction plategaTransaction
	if err := provider.do(ctx, http.MethodGet, "/transaction/"+url.PathEscape(reference), nil, &transaction); err != nil {
		return Intent{}, err
	}
	if transaction.Status == "" {
		return Intent{}, fmt.Errorf("%w: platega transaction without status", ErrProviderResponse)
	}
	intent := Intent{ProviderReference: reference, Status: plategaStatus(transaction.Status)}
	money, present, err := transaction.money()
	if err != nil {
		return Intent{}, err
	}
	if !present && intent.Status == "succeeded" {
		return Intent{}, fmt.Errorf("%w: platega confirmed transaction without amount", ErrProviderResponse)
	}
	intent.Amount = money
	return intent, nil
}

func (provider *Platega) Refund(context.Context, RefundRequest) (Refund, error) {
	return Refund{}, ErrUnsupported
}

// VerifyWebhook authenticates a callback by its X-MerchantId and X-Secret
// headers. An empty body with neither header is Platega's verification ping
// from the merchant cabinet, answered as an event that changes nothing.
func (provider *Platega) VerifyWebhook(headers http.Header, body []byte) (WebhookEvent, error) {
	merchantID, secret := headers.Get("X-MerchantId"), headers.Get("X-Secret")
	if merchantID == "" && secret == "" && len(bytes.TrimSpace(body)) == 0 {
		return WebhookEvent{ID: "ping", Type: "ping", Status: "pending", OccurredAt: provider.clock().UTC()}, nil
	}
	merchantOK := hmac.Equal([]byte(merchantID), []byte(provider.merchantID))
	secretOK := hmac.Equal([]byte(secret), []byte(provider.secret))
	if !merchantOK || !secretOK {
		return WebhookEvent{}, ErrInvalidSignature
	}
	var transaction plategaTransaction
	if err := json.Unmarshal(body, &transaction); err != nil {
		return WebhookEvent{}, ErrProviderResponse
	}
	reference := strings.TrimSpace(transaction.ID)
	rawStatus := strings.ToUpper(strings.TrimSpace(transaction.Status))
	if reference == "" && transaction.Payload == "" {
		return WebhookEvent{}, ErrProviderResponse
	}
	if rawStatus == "" {
		return WebhookEvent{}, ErrProviderResponse
	}
	status := plategaStatus(rawStatus)
	money, present, err := transaction.money()
	if err != nil {
		return WebhookEvent{}, err
	}
	if !present && status == "succeeded" {
		return WebhookEvent{}, fmt.Errorf("%w: platega confirmation without amount", ErrProviderResponse)
	}
	identity := reference
	if identity == "" {
		identity = transaction.Payload
	}
	return WebhookEvent{
		ID:                rawStatus + ":" + identity,
		Type:              rawStatus,
		ProviderReference: reference,
		MerchantReference: transaction.Payload,
		Status:            status,
		Amount:            money,
		OccurredAt:        provider.clock().UTC(),
	}, nil
}

func (provider *Platega) do(ctx context.Context, method, path string, body any, target any) error {
	headers := make(http.Header)
	headers.Set("X-MerchantId", provider.merchantID)
	headers.Set("X-Secret", provider.secret)
	return doJSON(ctx, provider.http, method, provider.baseURL+path, headers, body, target)
}

func plategaStatus(status string) string {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "CONFIRMED":
		return "succeeded"
	case "PENDING":
		return "pending"
	case "INPROGRESS":
		return "processing"
	case "FAILED":
		return "failed"
	case "CANCELED":
		return "cancelled"
	case "EXPIRED":
		return "expired"
	default:
		return "pending"
	}
}

// plategaPayer builds the metadata Platega requires on every payment:
// user_email identifies the buyer (not the order) and must be a real address
// or, for a Telegram customer, <telegram id>@telegram.org as the reference
// integration sends. Without either the payment is not sent.
func plategaPayer(metadata map[string]string) (map[string]string, error) {
	telegramID := strings.TrimSpace(metadata["customer_telegram_id"])
	email := strings.TrimSpace(metadata["customer_email"])
	if telegramID != "" {
		if _, err := strconv.ParseInt(telegramID, 10, 64); err != nil {
			return nil, errors.New("platega: customer_telegram_id must be numeric")
		}
		email = telegramID + "@telegram.org"
	}
	if email == "" || !plategaPlausibleEmail(email) {
		return nil, errors.New("platega: a customer email or Telegram ID is required for metadata.user_email")
	}
	userID := telegramID
	if userID == "" {
		userID = strings.TrimSpace(metadata["customer_id"])
	}
	if userID == "" {
		userID = email
	}
	userName := strings.TrimSpace(metadata["customer_name"])
	if userName == "" {
		userName = userID
	}
	return map[string]string{"userId": userID, "userName": userName, "user_email": email}, nil
}

func plategaPlausibleEmail(email string) bool {
	if strings.ContainsAny(email, " \t\r\n<>") {
		return false
	}
	at := strings.LastIndexByte(email, '@')
	return at > 0 && at < len(email)-1 && strings.Contains(email[at+1:], ".")
}

// plategaTrimBytes cuts a string to a byte budget on a rune boundary.
func plategaTrimBytes(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	cut := value[:limit]
	for !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
}

// plategaParseExpiresIn reads "HH:MM:SS"; any other shape is ignored.
func plategaParseExpiresIn(raw json.RawMessage) (time.Duration, bool) {
	var text string
	if len(raw) == 0 || json.Unmarshal(raw, &text) != nil {
		return 0, false
	}
	parts := strings.SplitN(text, ":", 3)
	if len(parts) != 3 {
		return 0, false
	}
	var units [3]int
	for index, part := range parts {
		value, err := strconv.Atoi(part)
		if err != nil || value < 0 {
			return 0, false
		}
		units[index] = value
	}
	return time.Duration(units[0])*time.Hour + time.Duration(units[1])*time.Minute + time.Duration(units[2])*time.Second, true
}

var (
	_ Provider        = (*Platega)(nil)
	_ CurrencySupport = (*Platega)(nil)
)

// Methods lists the Platega methods shown as separate buttons.
func (provider *Platega) Methods() []string { return provider.methodOrder }

// methodFor is the Platega method code for a chosen method, or the default.
func (provider *Platega) methodFor(method string) int {
	if code, ok := provider.methodCodes[method]; ok {
		return code
	}
	return provider.paymentMethod
}
