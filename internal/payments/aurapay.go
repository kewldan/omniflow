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
	"io"
	"math"
	"math/big"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/omniflow/omniflow/internal/commerce"
)

// AuraPay settles through AuraPay (aurapay.tech).
//
// Invoices are created with the API key and shop UUID in the X-ApiKey and
// X-ShopId headers. Notifications are signed in X-SIGNATURE with
// HMAC-SHA256, keyed with secret key #2, over every top-level value of the
// body concatenated in key order.
type AuraPay struct {
	apiKey      string
	shopID      string
	secretKey   string
	currency    string
	exponent    int
	method      string
	lifetime    int
	returnURL   string
	callbackURL string
	baseURL     string
	http        *http.Client
	clock       func() time.Time
}

const (
	aurapayAPIBaseURL = "https://app.aurapay.tech"
	// aurapayDefaultLifetime is Bedolaga's default invoice lifetime in minutes.
	aurapayDefaultLifetime = 60
)

// aurapayMethods are the services Bedolaga offers as AuraPay options.
var aurapayMethods = map[string]bool{"card": true, "sbp": true}

func buildAuraPay(settings Settings) (Provider, error) {
	required := map[string]string{
		"APP_AURAPAY_API_KEY":    settings.String("APP_AURAPAY_API_KEY"),
		"APP_AURAPAY_SHOP_ID":    settings.String("APP_AURAPAY_SHOP_ID"),
		"APP_AURAPAY_SECRET_KEY": settings.String("APP_AURAPAY_SECRET_KEY"),
	}
	var missing []string
	for name, value := range required {
		if value == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) == len(required) {
		return nil, ErrNotConfigured
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("AuraPay is partially configured: %s must also be set", strings.Join(missing, ", "))
	}
	currency := strings.ToUpper(settings.String("APP_AURAPAY_CURRENCY"))
	if currency == "" {
		currency = "RUB"
	}
	exponent, err := currencyExponent(currency)
	if err != nil {
		return nil, fmt.Errorf("AuraPay currency %q is not supported", currency)
	}
	method := strings.ToLower(settings.String("APP_AURAPAY_PAYMENT_METHOD"))
	if method != "" && !aurapayMethods[method] {
		return nil, errors.New("APP_AURAPAY_PAYMENT_METHOD must be card or sbp")
	}
	lifetime := aurapayDefaultLifetime
	if raw := settings.String("APP_AURAPAY_PAYMENT_LIFETIME_MINUTES"); raw != "" {
		lifetime, err = strconv.Atoi(raw)
		if err != nil || lifetime <= 0 {
			return nil, errors.New("APP_AURAPAY_PAYMENT_LIFETIME_MINUTES must be a positive number of minutes")
		}
	}
	return &AuraPay{
		apiKey: required["APP_AURAPAY_API_KEY"], shopID: required["APP_AURAPAY_SHOP_ID"],
		secretKey: required["APP_AURAPAY_SECRET_KEY"], currency: currency, exponent: exponent,
		method: method, lifetime: lifetime, returnURL: settings.String("APP_AURAPAY_RETURN_URL"),
		// Sent with each invoice when the installation has a public origin,
		// as Bedolaga did.
		callbackURL: settings.WebhookURL("aurapay"),
		baseURL:     aurapayAPIBaseURL, http: tracedClient(30 * time.Second), clock: time.Now,
	}, nil
}

func (provider *AuraPay) Name() string { return "aurapay" }

func (provider *AuraPay) Capabilities() Capabilities {
	return Capabilities{Webhooks: true, Polling: true}
}

// SupportsCurrency reports the single currency this shop is configured to take.
func (provider *AuraPay) SupportsCurrency(currency string) bool {
	return strings.ToUpper(strings.TrimSpace(currency)) == provider.currency
}

type aurapayInvoice struct {
	ID          aurapayText `json:"id"`
	OrderID     aurapayText `json:"order_id"`
	Status      string      `json:"status"`
	Amount      json.Number `json:"amount"`
	Currency    string      `json:"currency"`
	ExpiresAt   string      `json:"expires_at"`
	PaymentData struct {
		URL string `json:"url"`
	} `json:"payment_data"`
}

func (provider *AuraPay) Create(ctx context.Context, request CreateRequest) (Intent, error) {
	if !provider.SupportsCurrency(request.Amount.Currency) {
		return Intent{}, commerce.ErrCurrencyMismatch
	}
	if request.Amount.Amount <= 0 {
		return Intent{}, errors.New("AuraPay amount must be positive")
	}
	body := map[string]any{
		"amount":   json.Number(formatMinor(request.Amount.Amount, provider.exponent)),
		"order_id": request.OrderID,
		"lifetime": provider.lifetime,
	}
	if request.Description != "" {
		body["comment"] = request.Description
	}
	if provider.method != "" {
		body["service"] = provider.method
	}
	returnURL := request.ReturnURL
	if returnURL == "" {
		returnURL = provider.returnURL
	}
	if returnURL != "" {
		body["success_url"] = returnURL
		body["fail_url"] = returnURL
	}
	if provider.callbackURL != "" {
		body["callback_url"] = provider.callbackURL
	}
	var invoice aurapayInvoice
	if err := doJSON(ctx, provider.http, http.MethodPost, provider.baseURL+"/invoice/create", provider.headers(), body, &invoice); err != nil {
		return Intent{}, err
	}
	if invoice.ID == "" || invoice.PaymentData.URL == "" {
		return Intent{}, fmt.Errorf("%w: AuraPay returned no invoice link", ErrProviderResponse)
	}
	intent := Intent{
		ProviderReference: string(invoice.ID), Status: aurapayStatus(invoice.Status),
		CheckoutURL: invoice.PaymentData.URL, Amount: request.Amount,
	}
	if expires, err := time.Parse(time.RFC3339, invoice.ExpiresAt); err == nil {
		expires = expires.UTC()
		intent.ExpiresAt = &expires
	}
	return intent, nil
}

func (provider *AuraPay) Poll(ctx context.Context, reference string) (Intent, error) {
	var invoice aurapayInvoice
	if err := doJSON(ctx, provider.http, http.MethodPost, provider.baseURL+"/invoice/status", provider.headers(), map[string]string{"id": reference}, &invoice); err != nil {
		return Intent{}, err
	}
	if invoice.Status == "" {
		return Intent{}, fmt.Errorf("%w: AuraPay returned no invoice status", ErrProviderResponse)
	}
	status := aurapayStatus(invoice.Status)
	amount, err := provider.amount(invoice, status)
	if err != nil {
		return Intent{}, err
	}
	return Intent{ProviderReference: reference, Status: status, CheckoutURL: invoice.PaymentData.URL, Amount: amount}, nil
}

func (provider *AuraPay) Refund(context.Context, RefundRequest) (Refund, error) {
	return Refund{}, ErrUnsupported
}

func (provider *AuraPay) VerifyWebhook(headers http.Header, body []byte) (WebhookEvent, error) {
	provided, err := hex.DecodeString(strings.TrimSpace(headers.Get("X-SIGNATURE")))
	if err != nil || len(provided) == 0 {
		return WebhookEvent{}, ErrInvalidSignature
	}
	message, err := aurapaySignedMessage(body)
	if err != nil {
		return WebhookEvent{}, ErrProviderResponse
	}
	mac := hmac.New(sha256.New, []byte(provider.secretKey))
	_, _ = mac.Write([]byte(message))
	if !hmac.Equal(provided, mac.Sum(nil)) {
		return WebhookEvent{}, ErrInvalidSignature
	}
	var invoice aurapayInvoice
	if err := json.Unmarshal(body, &invoice); err != nil || invoice.ID == "" || invoice.Status == "" {
		return WebhookEvent{}, ErrProviderResponse
	}
	status := aurapayStatus(invoice.Status)
	amount, err := provider.amount(invoice, status)
	if err != nil {
		return WebhookEvent{}, err
	}
	return WebhookEvent{
		ID: invoice.Status + ":" + string(invoice.ID), Type: "invoice", ProviderReference: string(invoice.ID),
		MerchantReference: string(invoice.OrderID), Status: status, Amount: amount,
		OccurredAt: provider.clock().UTC(),
	}, nil
}

// WebhookAck answers as Bedolaga did; AuraPay retries anything but a 200.
func (provider *AuraPay) WebhookAck(string) (int, string, []byte) {
	return http.StatusOK, "application/json", []byte(`{"status":true}`)
}

func (provider *AuraPay) headers() http.Header {
	headers := make(http.Header)
	headers.Set("X-ApiKey", provider.apiKey)
	headers.Set("X-ShopId", provider.shopID)
	return headers
}

func (provider *AuraPay) amount(invoice aurapayInvoice, status string) (commerce.Money, error) {
	if invoice.Amount == "" {
		if status == "succeeded" {
			return commerce.Money{}, fmt.Errorf("%w: paid AuraPay invoice carries no amount", ErrProviderResponse)
		}
		return commerce.Money{}, nil
	}
	currency := strings.ToUpper(invoice.Currency)
	if currency == "" {
		currency = provider.currency
	}
	exponent, err := currencyExponent(currency)
	if err != nil {
		return commerce.Money{}, err
	}
	amount, err := parseMinor(invoice.Amount.String(), exponent)
	if err != nil {
		return commerce.Money{}, err
	}
	return commerce.Money{Amount: amount, Currency: currency}, nil
}

// aurapayStatus maps an invoice status; an unknown one stays pending.
func aurapayStatus(status string) string {
	switch status {
	case "PAID":
		return "succeeded"
	case "EXPIRED":
		return "expired"
	default:
		return "pending"
	}
}

// aurapaySignedMessage is the text AuraPay signs: every top-level value, keys
// sorted, converted with Python's str() exactly as Bedolaga converts them, and
// concatenated. A null becomes the empty string, as PHP's implode() does.
func aurapaySignedMessage(body []byte) (string, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil {
		return "", err
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		return "", errors.New("notification is not an object")
	}
	values := map[string]string{}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return "", err
		}
		key, _ := keyToken.(string)
		rendered, err := aurapayValue(decoder, true)
		if err != nil {
			return "", err
		}
		values[key] = rendered
	}
	if _, err := decoder.Token(); err != nil {
		return "", err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return "", errors.New("trailing data after notification")
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var builder strings.Builder
	for _, key := range keys {
		builder.WriteString(values[key])
	}
	return builder.String(), nil
}

// aurapayValue renders the next JSON value as Python's str() (top level) or
// repr() (inside a container) of what json.loads would have produced.
func aurapayValue(decoder *json.Decoder, top bool) (string, error) {
	token, err := decoder.Token()
	if err != nil {
		return "", err
	}
	switch value := token.(type) {
	case json.Delim:
		var builder strings.Builder
		switch value {
		case '{':
			builder.WriteByte('{')
			for first := true; decoder.More(); first = false {
				keyToken, err := decoder.Token()
				if err != nil {
					return "", err
				}
				key, _ := keyToken.(string)
				rendered, err := aurapayValue(decoder, false)
				if err != nil {
					return "", err
				}
				if !first {
					builder.WriteString(", ")
				}
				builder.WriteString(aurapayPyRepr(key))
				builder.WriteString(": ")
				builder.WriteString(rendered)
			}
			builder.WriteByte('}')
		case '[':
			builder.WriteByte('[')
			for first := true; decoder.More(); first = false {
				rendered, err := aurapayValue(decoder, false)
				if err != nil {
					return "", err
				}
				if !first {
					builder.WriteString(", ")
				}
				builder.WriteString(rendered)
			}
			builder.WriteByte(']')
		default:
			return "", errors.New("unexpected delimiter")
		}
		if _, err := decoder.Token(); err != nil {
			return "", err
		}
		return builder.String(), nil
	case string:
		if top {
			return value, nil
		}
		return aurapayPyRepr(value), nil
	case json.Number:
		return aurapayPyNumber(value)
	case bool:
		if value {
			return "True", nil
		}
		return "False", nil
	case nil:
		if top {
			return "", nil
		}
		return "None", nil
	default:
		return "", errors.New("unexpected token")
	}
}

// aurapayPyNumber renders a JSON number as Python's str() of the int or
// float json.loads produces.
func aurapayPyNumber(number json.Number) (string, error) {
	text := number.String()
	if !strings.ContainsAny(text, ".eE") {
		integer, ok := new(big.Int).SetString(text, 10)
		if !ok {
			return "", errors.New("invalid integer")
		}
		return integer.String(), nil
	}
	value, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsInf(value, 0) {
		return "", errors.New("invalid float")
	}
	if value == 0 {
		if math.Signbit(value) {
			return "-0.0", nil
		}
		return "0.0", nil
	}
	scientific := strconv.FormatFloat(value, 'e', -1, 64)
	exponent, _ := strconv.Atoi(scientific[strings.IndexByte(scientific, 'e')+1:])
	if exponent < -4 || exponent >= 16 {
		return scientific, nil
	}
	fixed := strconv.FormatFloat(value, 'f', -1, 64)
	if !strings.Contains(fixed, ".") {
		fixed += ".0"
	}
	return fixed, nil
}

// aurapayPyRepr is Python's repr() of a string: single quotes unless the text
// holds a single quote and no double quote, with non-printable characters
// escaped.
func aurapayPyRepr(value string) string {
	quote := byte('\'')
	if strings.ContainsRune(value, '\'') && !strings.ContainsRune(value, '"') {
		quote = '"'
	}
	var builder strings.Builder
	builder.WriteByte(quote)
	for len(value) > 0 {
		r, size := utf8.DecodeRuneInString(value)
		value = value[size:]
		switch {
		case r == rune(quote) || r == '\\':
			builder.WriteByte('\\')
			builder.WriteRune(r)
		case r == '\t':
			builder.WriteString(`\t`)
		case r == '\n':
			builder.WriteString(`\n`)
		case r == '\r':
			builder.WriteString(`\r`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&builder, `\x%02x`, r)
		case r < 0x7f || unicode.IsPrint(r):
			builder.WriteRune(r)
		case r <= 0xff:
			fmt.Fprintf(&builder, `\x%02x`, r)
		case r <= 0xffff:
			fmt.Fprintf(&builder, `\u%04x`, r)
		default:
			fmt.Fprintf(&builder, `\U%08x`, r)
		}
	}
	builder.WriteByte(quote)
	return builder.String()
}

// aurapayText accepts a JSON string or number.
type aurapayText string

func (text *aurapayText) UnmarshalJSON(raw []byte) error {
	raw = bytes.TrimSpace(raw)
	if bytes.Equal(raw, []byte("null")) {
		*text = ""
		return nil
	}
	if len(raw) > 0 && raw[0] == '"' {
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return err
		}
		*text = aurapayText(value)
		return nil
	}
	*text = aurapayText(raw)
	return nil
}

var (
	_ Provider            = (*AuraPay)(nil)
	_ CurrencySupport     = (*AuraPay)(nil)
	_ WebhookAcknowledger = (*AuraPay)(nil)
)
