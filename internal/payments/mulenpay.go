package payments

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
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
	mulenPayDefaultBaseURL = "https://mulenpay.ru/api"
	// mulenPayItemDescriptionLimit is the receipt line length the reference
	// integration trims to.
	mulenPayItemDescriptionLimit = 128
	// mulenPayClientLimit is a defensive bound: MulenPay documents no limit.
	mulenPayClientLimit = 255
)

// MulenPay is the Mulen Pay adapter (cards and SBP in RUB).
//
// Requests authenticate with a bearer API key and carry a SHA-1 `sign` over
// currency, amount, shop ID and secret. Callbacks are not trustworthy on their
// own — the optional SHA-1 checksum they may carry is a legacy integrity check,
// not authentication — so they are only a hint: the payment service re-reads
// the payment through the authenticated API before any money moves.
type MulenPay struct {
	apiKey         string
	secretKey      string
	shopID         int64
	baseURL        string
	language       string
	vatCode        int
	paymentSubject int
	paymentMode    int
	websiteURL     string
	http           *http.Client
	clock          func() time.Time
}

// buildMulenPay reads APP_MULENPAY_* and constructs the adapter.
func buildMulenPay(settings Settings) (Provider, error) {
	apiKey := settings.String("APP_MULENPAY_API_KEY")
	secretKey := settings.String("APP_MULENPAY_SECRET_KEY")
	rawShopID := settings.String("APP_MULENPAY_SHOP_ID")
	if apiKey == "" && secretKey == "" && rawShopID == "" {
		return nil, ErrNotConfigured
	}
	if apiKey == "" || secretKey == "" || rawShopID == "" {
		return nil, errors.New("mulenpay: APP_MULENPAY_API_KEY, APP_MULENPAY_SECRET_KEY and APP_MULENPAY_SHOP_ID must all be set")
	}
	shopID, err := strconv.ParseInt(rawShopID, 10, 64)
	if err != nil || shopID <= 0 {
		return nil, errors.New("mulenpay: APP_MULENPAY_SHOP_ID must be a positive integer")
	}
	baseURL := settings.String("APP_MULENPAY_BASE_URL")
	if baseURL == "" {
		baseURL = mulenPayDefaultBaseURL
	}
	language := settings.String("APP_MULENPAY_LANGUAGE")
	if language == "" {
		language = "ru"
	}
	websiteURL := settings.String("APP_MULENPAY_WEBSITE_URL")
	if websiteURL == "" {
		websiteURL = strings.TrimSuffix(strings.TrimSpace(settings.PublicURL), "/")
	}
	return &MulenPay{
		apiKey:         apiKey,
		secretKey:      secretKey,
		shopID:         shopID,
		baseURL:        strings.TrimSuffix(baseURL, "/"),
		language:       language,
		vatCode:        settings.Int("APP_MULENPAY_VAT_CODE", 0),
		paymentSubject: settings.Int("APP_MULENPAY_PAYMENT_SUBJECT", 4),
		paymentMode:    settings.Int("APP_MULENPAY_PAYMENT_MODE", 4),
		websiteURL:     websiteURL,
		http:           tracedClient(30 * time.Second),
		clock:          time.Now,
	}, nil
}

func (provider *MulenPay) Name() string { return "mulenpay" }

func (provider *MulenPay) Capabilities() Capabilities {
	return Capabilities{Webhooks: true, Polling: true}
}

// SupportsCurrency reports RUB only: every payment is created in `rub`.
func (provider *MulenPay) SupportsCurrency(currency string) bool {
	return strings.EqualFold(strings.TrimSpace(currency), "RUB")
}

// WebhooksNeedPolling marks MulenPay callbacks as hints to be confirmed by
// GET /v2/payments/{id} before they can settle anything.
func (provider *MulenPay) WebhooksNeedPolling() bool { return true }

// mulenPayAmount accepts an amount sent as a JSON string or a JSON number and
// keeps its literal digits, so no float rounding touches money.
type mulenPayAmount string

func (amount *mulenPayAmount) UnmarshalJSON(data []byte) error {
	if bytes.Equal(data, []byte("null")) {
		*amount = ""
		return nil
	}
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		*amount = mulenPayAmount(text)
		return nil
	}
	var number json.Number
	if err := json.Unmarshal(data, &number); err != nil {
		return err
	}
	*amount = mulenPayAmount(number.String())
	return nil
}

// mulenPayPayment is the payment object of GET /v2/payments and its list.
type mulenPayPayment struct {
	ID       json.Number    `json:"id"`
	UUID     string         `json:"uuid"`
	Status   *int           `json:"status"`
	Amount   mulenPayAmount `json:"amount"`
	Currency string         `json:"currency"`
}

type mulenPayItem struct {
	Description    string      `json:"description"`
	Quantity       int         `json:"quantity"`
	Price          json.Number `json:"price"`
	VATCode        int         `json:"vat_code"`
	PaymentSubject int         `json:"payment_subject"`
	PaymentMode    int         `json:"payment_mode"`
}

func (provider *MulenPay) Create(ctx context.Context, request CreateRequest) (Intent, error) {
	if !provider.SupportsCurrency(request.Amount.Currency) {
		return Intent{}, fmt.Errorf("mulenpay: currency %s is not supported", request.Amount.Currency)
	}
	if request.Amount.Amount <= 0 {
		return Intent{}, errors.New("mulenpay: amount must be positive")
	}
	amount := formatMinor(request.Amount.Amount, 2)
	const currency = "rub"
	body := map[string]any{
		"currency":    currency,
		"amount":      amount,
		"uuid":        request.OrderID,
		"shopId":      provider.shopID,
		"description": request.Description,
		"items": []mulenPayItem{{
			Description:    mulenPayTruncate(request.Description, mulenPayItemDescriptionLimit),
			Quantity:       1,
			Price:          json.Number(amount),
			VATCode:        provider.vatCode,
			PaymentSubject: provider.paymentSubject,
			PaymentMode:    provider.paymentMode,
		}},
		"language": provider.language,
		"sign":     mulenPayCreateSign(currency, amount, provider.shopID, provider.secretKey),
		"client":   mulenPayTruncate(mulenPayClient(request), mulenPayClientLimit),
	}
	if provider.websiteURL != "" {
		body["website_url"] = provider.websiteURL
	}
	var response struct {
		Success    bool        `json:"success"`
		ID         json.Number `json:"id"`
		PaymentURL string      `json:"paymentUrl"`
	}
	if err := provider.do(ctx, http.MethodPost, "/v2/payments", body, &response); err != nil {
		return Intent{}, err
	}
	if !response.Success || response.ID.String() == "" {
		return Intent{}, fmt.Errorf("%w: mulenpay payment was not created", ErrProviderResponse)
	}
	return Intent{ProviderReference: response.ID.String(), Status: "pending", CheckoutURL: response.PaymentURL, Amount: request.Amount}, nil
}

func (provider *MulenPay) Poll(ctx context.Context, reference string) (Intent, error) {
	if _, err := strconv.ParseInt(reference, 10, 64); err != nil {
		return Intent{}, fmt.Errorf("%w: mulenpay payment id must be numeric", ErrProviderResponse)
	}
	var raw json.RawMessage
	if err := provider.do(ctx, http.MethodGet, "/v2/payments/"+url.PathEscape(reference), nil, &raw); err != nil {
		return Intent{}, err
	}
	payment, err := mulenPayExtractPayment(raw)
	if err != nil {
		return Intent{}, err
	}
	// The API is the source of truth only for the payment that was asked for.
	if payment.ID.String() != reference {
		return Intent{}, fmt.Errorf("%w: mulenpay answered for another payment", ErrProviderResponse)
	}
	money, err := mulenPayMoney(payment.Amount, payment.Currency)
	if err != nil {
		return Intent{}, err
	}
	status := "pending"
	if payment.Status != nil {
		status = mulenPayStatusCode(*payment.Status)
	}
	return Intent{ProviderReference: reference, Status: status, Amount: money}, nil
}

// mulenPayExtractPayment accepts both answer shapes the reference integration
// handles: {"success":true,"payment":{...}} and the bare payment object.
func mulenPayExtractPayment(raw json.RawMessage) (mulenPayPayment, error) {
	var wrapped struct {
		Success bool             `json:"success"`
		Payment *mulenPayPayment `json:"payment"`
	}
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		return mulenPayPayment{}, fmt.Errorf("decode provider response: %w", err)
	}
	if wrapped.Success && wrapped.Payment != nil {
		return *wrapped.Payment, nil
	}
	var bare mulenPayPayment
	if err := json.Unmarshal(raw, &bare); err != nil {
		return mulenPayPayment{}, fmt.Errorf("decode provider response: %w", err)
	}
	if bare.ID.String() == "" || bare.Status == nil {
		return mulenPayPayment{}, fmt.Errorf("%w: mulenpay payment has no id or status", ErrProviderResponse)
	}
	return bare, nil
}

func (provider *MulenPay) Refund(context.Context, RefundRequest) (Refund, error) {
	return Refund{}, ErrUnsupported
}

// Probe lists at most one payment: an authenticated read that moves nothing.
func (provider *MulenPay) Probe(ctx context.Context) error {
	var raw json.RawMessage
	return provider.do(ctx, http.MethodGet, "/v2/payments?offset=0&limit=1", nil, &raw)
}

// VerifyWebhook parses a MulenPay callback into an untrusted hint.
//
// A callback that carries a `sign` must carry the right one (SHA-1 over the
// values in wire order, then the secret); a callback without one is accepted
// as a hint. Either way the payment service polls the payment before acting.
func (provider *MulenPay) VerifyWebhook(_ http.Header, body []byte) (WebhookEvent, error) {
	fields, err := mulenPayOrderedFields(body)
	if err != nil {
		return WebhookEvent{}, ErrProviderResponse
	}
	values := map[string]mulenPayField{}
	for _, field := range fields {
		values[field.key] = field
	}
	if sign, present := values["sign"]; present {
		if !provider.validSign(fields, sign) {
			return WebhookEvent{}, ErrInvalidSignature
		}
	}
	id, ok := values["id"]
	if !ok || id.kind == mulenPayKindNull {
		return WebhookEvent{}, ErrProviderResponse
	}
	reference, err := strconv.ParseInt(strings.TrimSpace(id.text), 10, 64)
	if err != nil || reference <= 0 {
		return WebhookEvent{}, ErrProviderResponse
	}
	if _, ok := values["amount"]; !ok {
		return WebhookEvent{}, ErrProviderResponse
	}
	if _, ok := values["currency"]; !ok {
		return WebhookEvent{}, ErrProviderResponse
	}
	rawStatus := ""
	found := false
	for _, key := range []string{"payment_status", "status", "paymentStatus"} {
		if field, ok := values[key]; ok {
			found = true
			if rawStatus == "" && field.kind == mulenPayKindString {
				rawStatus = strings.ToLower(field.text)
			}
		}
	}
	if !found {
		return WebhookEvent{}, ErrProviderResponse
	}
	status := "pending"
	switch rawStatus {
	case "success":
		status = "succeeded"
	case "cancel":
		status = "cancelled"
	}
	// The amount is replaced by the polled one before anything settles; it is
	// parsed here only so the hint reads sensibly in the webhook log.
	money, _ := mulenPayMoney(mulenPayAmount(values["amount"].text), values["currency"].text)
	providerReference := strconv.FormatInt(reference, 10)
	return WebhookEvent{
		ID:                rawStatus + ":" + providerReference,
		Type:              rawStatus,
		ProviderReference: providerReference,
		MerchantReference: values["uuid"].text,
		Status:            status,
		Amount:            money,
		OccurredAt:        provider.clock().UTC(),
	}, nil
}

func (provider *MulenPay) validSign(fields []mulenPayField, sign mulenPayField) bool {
	if sign.kind != mulenPayKindString || sign.text == "" {
		return false
	}
	var data strings.Builder
	for _, field := range fields {
		if field.key == "sign" {
			continue
		}
		if field.kind == mulenPayKindContainer {
			// Python's repr of a nested object is not reproduced: fail closed.
			return false
		}
		data.WriteString(field.python)
	}
	digest := sha1.Sum([]byte(data.String() + provider.secretKey))
	expected := hex.EncodeToString(digest[:])
	return hmac.Equal([]byte(strings.ToLower(sign.text)), []byte(expected))
}

func (provider *MulenPay) do(ctx context.Context, method, path string, body, target any) error {
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer "+provider.apiKey)
	return doJSON(ctx, provider.http, method, provider.baseURL+path, headers, body, target)
}

// mulenPayStatusCode maps the numeric status of the payments API.
func mulenPayStatusCode(code int) string {
	switch code {
	case 0:
		return "pending"
	case 1:
		return "processing"
	case 2:
		return "cancelled"
	case 3:
		return "succeeded"
	case 4:
		return "failed"
	case 5, 6:
		// Held funds are not settled funds.
		return "processing"
	default:
		return "pending"
	}
}

func mulenPayMoney(amount mulenPayAmount, currency string) (commerce.Money, error) {
	currency = strings.ToUpper(strings.TrimSpace(currency))
	exponent, err := currencyExponent(currency)
	if err != nil {
		return commerce.Money{}, err
	}
	minor, err := parseMinor(strings.TrimSpace(string(amount)), exponent)
	if err != nil {
		return commerce.Money{}, err
	}
	return commerce.Money{Amount: minor, Currency: currency}, nil
}

func mulenPayCreateSign(currency, amount string, shopID int64, secret string) string {
	digest := sha1.Sum([]byte(currency + amount + strconv.FormatInt(shopID, 10) + secret))
	return hex.EncodeToString(digest[:])
}

// mulenPayClient is the payer contact MulenPay requires on every payment.
// Omniflow passes no contact by default, so the order reference stands in, as
// the reference integration falls back to an internal identifier.
func mulenPayClient(request CreateRequest) string {
	for _, key := range []string{"customer_email", "customer_id"} {
		if value := strings.TrimSpace(request.Metadata[key]); value != "" {
			return value
		}
	}
	return request.OrderID
}

func mulenPayTruncate(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit])
}

const (
	mulenPayKindString = iota
	mulenPayKindNumber
	mulenPayKindBool
	mulenPayKindNull
	mulenPayKindContainer
)

// mulenPayField is one top-level callback field: its raw text, and the text
// Python's str() gives it, which is what the checksum is computed over.
type mulenPayField struct {
	key    string
	kind   int
	text   string
	python string
}

func mulenPayOrderedFields(body []byte) ([]mulenPayField, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, ErrProviderResponse
	}
	var fields []mulenPayField
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, _ := keyToken.(string)
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		field, err := mulenPayParseField(key, value)
		if err != nil {
			return nil, err
		}
		fields = append(fields, field)
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	if decoder.More() {
		return nil, ErrProviderResponse
	}
	return fields, nil
}

func mulenPayParseField(key string, raw json.RawMessage) (mulenPayField, error) {
	trimmed := bytes.TrimSpace(raw)
	field := mulenPayField{key: key}
	switch {
	case len(trimmed) == 0:
		return field, ErrProviderResponse
	case trimmed[0] == '"':
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return field, err
		}
		field.kind, field.text, field.python = mulenPayKindString, text, text
	case trimmed[0] == '{' || trimmed[0] == '[':
		field.kind, field.text = mulenPayKindContainer, string(trimmed)
	case bytes.Equal(trimmed, []byte("true")):
		field.kind, field.text, field.python = mulenPayKindBool, "true", "True"
	case bytes.Equal(trimmed, []byte("false")):
		field.kind, field.text, field.python = mulenPayKindBool, "false", "False"
	case bytes.Equal(trimmed, []byte("null")):
		field.kind, field.text, field.python = mulenPayKindNull, "", "None"
	default:
		literal := string(trimmed)
		field.kind, field.text, field.python = mulenPayKindNumber, literal, mulenPayPythonNumber(literal)
	}
	return field, nil
}

// mulenPayPythonNumber renders a JSON number as Python's str() renders it
// after json.loads: integers keep their digits, fractions become floats.
func mulenPayPythonNumber(literal string) string {
	if !strings.ContainsAny(literal, ".eE") {
		return literal
	}
	value, err := strconv.ParseFloat(literal, 64)
	if err != nil {
		return literal
	}
	scientific := strconv.FormatFloat(value, 'e', -1, 64)
	exponent, _ := strconv.Atoi(scientific[strings.IndexByte(scientific, 'e')+1:])
	if exponent < -4 || exponent > 15 {
		return scientific
	}
	fixed := strconv.FormatFloat(value, 'f', -1, 64)
	if !strings.Contains(fixed, ".") {
		fixed += ".0"
	}
	return fixed
}

var (
	_ Provider             = (*MulenPay)(nil)
	_ CurrencySupport      = (*MulenPay)(nil)
	_ ConnectionProbe      = (*MulenPay)(nil)
	_ PollVerifiedWebhooks = (*MulenPay)(nil)
)
