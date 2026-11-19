package payments

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/omniflow/omniflow/internal/commerce"
)

const (
	heleketDefaultBaseURL  = "https://api.heleket.com/v1"
	heleketDefaultCurrency = "USDT"
	heleketDefaultLifetime = 3600
	heleketMinLifetime     = 300
	heleketMaxLifetime     = 43200
)

// Heleket is the Heleket crypto-invoice adapter.
//
// An invoice is priced in fiat and paid in a cryptocurrency the merchant
// chose. Every request is signed with md5(base64(body) + API key), carried in
// the `sign` header next to the `merchant` header; every notification carries
// the same signature over its own body, which is what makes it trustworthy.
type Heleket struct {
	merchantID      string
	apiKey          string
	baseURL         string
	toCurrency      string
	network         string
	lifetime        int
	discountPercent *int
	callbackURL     string
	http            *http.Client
	clock           func() time.Time
}

// buildHeleket reads APP_HELEKET_* and constructs the adapter.
func buildHeleket(settings Settings) (Provider, error) {
	merchantID := settings.String("APP_HELEKET_MERCHANT_ID")
	apiKey := settings.String("APP_HELEKET_API_KEY")
	if merchantID == "" && apiKey == "" {
		return nil, ErrNotConfigured
	}
	if merchantID == "" || apiKey == "" {
		return nil, errors.New("heleket: APP_HELEKET_MERCHANT_ID and APP_HELEKET_API_KEY must both be set")
	}
	callbackURL := settings.WebhookURL("heleket")
	if callbackURL == "" {
		return nil, errors.New("heleket: APP_PUBLIC_URL is required to send url_callback with each invoice")
	}
	baseURL := settings.String("APP_HELEKET_BASE_URL")
	if baseURL == "" {
		baseURL = heleketDefaultBaseURL
	}
	toCurrency := settings.String("APP_HELEKET_DEFAULT_CURRENCY")
	if toCurrency == "" {
		toCurrency = heleketDefaultCurrency
	}
	lifetime := settings.Int("APP_HELEKET_INVOICE_LIFETIME", heleketDefaultLifetime)
	lifetime = max(heleketMinLifetime, min(heleketMaxLifetime, lifetime))
	provider := &Heleket{
		merchantID:  merchantID,
		apiKey:      apiKey,
		baseURL:     strings.TrimSuffix(baseURL, "/"),
		toCurrency:  toCurrency,
		network:     settings.String("APP_HELEKET_DEFAULT_NETWORK"),
		lifetime:    lifetime,
		callbackURL: callbackURL,
		http:        tracedClient(30 * time.Second),
		clock:       time.Now,
	}
	if raw := settings.String("APP_HELEKET_MARKUP_PERCENT"); raw != "" {
		markup, err := strconv.ParseFloat(raw, 64)
		if err != nil || math.IsNaN(markup) || math.IsInf(markup, 0) {
			return nil, errors.New("heleket: APP_HELEKET_MARKUP_PERCENT must be a number")
		}
		// Heleket has no markup field: a markup is a negative discount, rounded
		// half-to-even to a whole percent as the reference integration does.
		if rounded := int(math.RoundToEven(markup)); rounded != 0 {
			discount := -rounded
			provider.discountPercent = &discount
		}
	}
	return provider, nil
}

func (provider *Heleket) Name() string { return "heleket" }

func (provider *Heleket) Capabilities() Capabilities {
	return Capabilities{Webhooks: true, Polling: true}
}

// SupportsCurrency reports the invoice currencies this adapter prices in. The
// reference integration invoices in RUB only; USD is Heleket's own documented
// invoice currency. Others are not offered until verified against a merchant.
func (provider *Heleket) SupportsCurrency(currency string) bool {
	switch strings.ToUpper(strings.TrimSpace(currency)) {
	case "RUB", "USD":
		return true
	default:
		return false
	}
}

// heleketPayment is the payment object Heleket returns from payment and
// payment/info, and posts to url_callback.
type heleketPayment struct {
	UUID          string `json:"uuid"`
	OrderID       string `json:"order_id"`
	Amount        string `json:"amount"`
	Currency      string `json:"currency"`
	URL           string `json:"url"`
	Status        string `json:"status"`
	PaymentStatus string `json:"payment_status"`
	ExpiredAt     int64  `json:"expired_at"`
}

type heleketEnvelope struct {
	State   *int            `json:"state"`
	Message string          `json:"message"`
	Result  json.RawMessage `json:"result"`
}

func (provider *Heleket) Create(ctx context.Context, request CreateRequest) (Intent, error) {
	exponent, err := currencyExponent(request.Amount.Currency)
	if err != nil {
		return Intent{}, err
	}
	if request.Amount.Amount <= 0 {
		return Intent{}, errors.New("heleket: amount must be positive")
	}
	body := map[string]any{
		"amount":       formatMinor(request.Amount.Amount, exponent),
		"currency":     request.Amount.Currency,
		"order_id":     request.OrderID,
		"lifetime":     provider.lifetime,
		"url_callback": provider.callbackURL,
	}
	if provider.toCurrency != "" {
		body["to_currency"] = provider.toCurrency
	}
	if provider.network != "" {
		body["network"] = provider.network
	}
	if request.ReturnURL != "" {
		body["url_return"] = request.ReturnURL
		body["url_success"] = request.ReturnURL
	}
	if provider.discountPercent != nil {
		body["discount_percent"] = *provider.discountPercent
	}
	var payment heleketPayment
	if err := provider.call(ctx, "payment", body, &payment); err != nil {
		return Intent{}, err
	}
	if payment.UUID == "" {
		return Intent{}, fmt.Errorf("%w: heleket payment without uuid", ErrProviderResponse)
	}
	intent, err := provider.intent(payment)
	if err != nil {
		// The invoice exists even when its echo is incomplete; keep the amount
		// that was asked for rather than losing the reference.
		intent = Intent{ProviderReference: payment.UUID, Status: heleketStatus(payment), CheckoutURL: payment.URL, Amount: request.Amount}
	}
	return intent, nil
}

func (provider *Heleket) Poll(ctx context.Context, reference string) (Intent, error) {
	var payment heleketPayment
	if err := provider.call(ctx, "payment/info", map[string]any{"uuid": reference}, &payment); err != nil {
		return Intent{}, err
	}
	if payment.UUID == "" {
		return Intent{}, fmt.Errorf("%w: heleket payment/info without uuid", ErrProviderResponse)
	}
	return provider.intent(payment)
}

func (provider *Heleket) Refund(context.Context, RefundRequest) (Refund, error) {
	return Refund{}, ErrUnsupported
}

// Probe lists payments: an authenticated read that moves nothing.
func (provider *Heleket) Probe(ctx context.Context) error {
	var result json.RawMessage
	return provider.call(ctx, "payment/list", map[string]any{}, &result)
}

func (provider *Heleket) VerifyWebhook(_ http.Header, body []byte) (WebhookEvent, error) {
	signature, canonical, err := heleketCanonicalWebhook(body)
	if err != nil {
		return WebhookEvent{}, err
	}
	if signature == "" {
		return WebhookEvent{}, ErrInvalidSignature
	}
	expected := heleketSign(canonical, provider.apiKey)
	if !hmac.Equal([]byte(expected), []byte(signature)) {
		return WebhookEvent{}, ErrInvalidSignature
	}
	var payment heleketPayment
	if err := json.Unmarshal(body, &payment); err != nil || (payment.UUID == "" && payment.OrderID == "") {
		return WebhookEvent{}, ErrProviderResponse
	}
	amount, err := heleketMoney(payment.Amount, payment.Currency)
	if err != nil {
		return WebhookEvent{}, err
	}
	status := heleketStatus(payment)
	raw := strings.ToLower(payment.Status)
	if raw == "" {
		raw = strings.ToLower(payment.PaymentStatus)
	}
	return WebhookEvent{
		ID:                raw + ":" + payment.UUID + ":" + payment.OrderID,
		Type:              raw,
		ProviderReference: payment.UUID,
		MerchantReference: payment.OrderID,
		Status:            status,
		Amount:            amount,
		OccurredAt:        provider.clock().UTC(),
	}, nil
}

func (provider *Heleket) intent(payment heleketPayment) (Intent, error) {
	amount, err := heleketMoney(payment.Amount, payment.Currency)
	if err != nil {
		return Intent{}, err
	}
	intent := Intent{ProviderReference: payment.UUID, Status: heleketStatus(payment), CheckoutURL: payment.URL, Amount: amount}
	if payment.ExpiredAt > 0 {
		expires := time.Unix(payment.ExpiredAt, 0).UTC()
		intent.ExpiresAt = &expires
	}
	return intent, nil
}

// call posts a signed JSON body. The body is serialised once, with sorted keys
// and escaped slashes as Heleket's reference clients do, and the exact bytes
// sent are the bytes signed.
func (provider *Heleket) call(ctx context.Context, endpoint string, payload map[string]any, target any) error {
	body, err := heleketRequestBody(payload)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, provider.baseURL+"/"+endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create provider request: %w", err)
	}
	request.Header.Set("merchant", provider.merchantID)
	request.Header.Set("sign", heleketSign(body, provider.apiKey))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := provider.http.Do(request)
	if err != nil {
		return fmt.Errorf("call provider: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxProviderBody))
		return &StatusError{StatusCode: response.StatusCode}
	}
	var envelope heleketEnvelope
	if err := json.NewDecoder(io.LimitReader(response.Body, maxProviderBody)).Decode(&envelope); err != nil {
		return fmt.Errorf("decode provider response: %w", err)
	}
	if envelope.State == nil || *envelope.State != 0 {
		return fmt.Errorf("%w: heleket state is not 0", ErrProviderResponse)
	}
	if target == nil || len(envelope.Result) == 0 {
		return nil
	}
	if err := json.Unmarshal(envelope.Result, target); err != nil {
		return fmt.Errorf("decode provider response: %w", err)
	}
	return nil
}

func heleketStatus(payment heleketPayment) string {
	status := payment.Status
	if status == "" {
		status = payment.PaymentStatus
	}
	switch strings.ToLower(status) {
	case "paid", "paid_over":
		return "succeeded"
	case "cancel":
		return "cancelled"
	case "fail", "system_fail":
		return "failed"
	case "refund_paid":
		// Final and never credited by the reference integration. Omniflow has
		// no reversal status for an intent, and this one must not settle.
		return "cancelled"
	case "process", "confirm_check":
		return "processing"
	default:
		return "pending"
	}
}

func heleketMoney(value, currency string) (commerce.Money, error) {
	currency = strings.ToUpper(strings.TrimSpace(currency))
	exponent, err := currencyExponent(currency)
	if err != nil {
		return commerce.Money{}, err
	}
	amount, err := parseMinor(strings.TrimSpace(value), exponent)
	if err != nil {
		return commerce.Money{}, err
	}
	return commerce.Money{Amount: amount, Currency: currency}, nil
}

func heleketSign(body []byte, apiKey string) string {
	digest := md5.Sum([]byte(base64.StdEncoding.EncodeToString(body) + apiKey))
	return hex.EncodeToString(digest[:])
}

// heleketRequestBody serialises an outgoing request the way the reference
// client does: compact, keys sorted, nil values dropped, non-ASCII left as
// UTF-8 and every slash escaped as `\/`.
func heleketRequestBody(payload map[string]any) ([]byte, error) {
	keys := make([]string, 0, len(payload))
	for key, value := range payload {
		if value != nil {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	var out bytes.Buffer
	out.WriteByte('{')
	for index, key := range keys {
		if index > 0 {
			out.WriteByte(',')
		}
		heleketWriteString(&out, key)
		out.WriteByte(':')
		switch value := payload[key].(type) {
		case string:
			heleketWriteString(&out, value)
		case int:
			out.WriteString(strconv.Itoa(value))
		case int64:
			out.WriteString(strconv.FormatInt(value, 10))
		default:
			return nil, fmt.Errorf("heleket: unsupported request value for %s", key)
		}
	}
	out.WriteByte('}')
	return out.Bytes(), nil
}

// heleketCanonicalWebhook rebuilds the string Heleket signed a notification
// over: the body as parsed, in its original key order, without the `sign`
// field, re-serialised compactly with UTF-8 left unescaped and slashes escaped.
// It also returns the signature the body carried.
func heleketCanonicalWebhook(body []byte) (string, []byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return "", nil, ErrProviderResponse
	}
	var out bytes.Buffer
	out.WriteByte('{')
	signature := ""
	first := true
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return "", nil, ErrProviderResponse
		}
		key, _ := keyToken.(string)
		if key == "sign" {
			var value any
			if err := decoder.Decode(&value); err != nil {
				return "", nil, ErrProviderResponse
			}
			if text, ok := value.(string); ok {
				signature = text
			}
			continue
		}
		if !first {
			out.WriteByte(',')
		}
		first = false
		heleketWriteString(&out, key)
		out.WriteByte(':')
		if err := heleketWriteValue(&out, decoder); err != nil {
			return "", nil, ErrProviderResponse
		}
	}
	if _, err := decoder.Token(); err != nil {
		return "", nil, ErrProviderResponse
	}
	if decoder.More() {
		return "", nil, ErrProviderResponse
	}
	out.WriteByte('}')
	return signature, out.Bytes(), nil
}

func heleketWriteValue(out *bytes.Buffer, decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	switch value := token.(type) {
	case json.Delim:
		switch value {
		case '{':
			out.WriteByte('{')
			first := true
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				if !first {
					out.WriteByte(',')
				}
				first = false
				key, _ := keyToken.(string)
				heleketWriteString(out, key)
				out.WriteByte(':')
				if err := heleketWriteValue(out, decoder); err != nil {
					return err
				}
			}
			if _, err := decoder.Token(); err != nil {
				return err
			}
			out.WriteByte('}')
		case '[':
			out.WriteByte('[')
			first := true
			for decoder.More() {
				if !first {
					out.WriteByte(',')
				}
				first = false
				if err := heleketWriteValue(out, decoder); err != nil {
					return err
				}
			}
			if _, err := decoder.Token(); err != nil {
				return err
			}
			out.WriteByte(']')
		default:
			return ErrProviderResponse
		}
	case string:
		heleketWriteString(out, value)
	case json.Number:
		out.WriteString(heleketPythonNumber(string(value)))
	case bool:
		if value {
			out.WriteString("true")
		} else {
			out.WriteString("false")
		}
	case nil:
		out.WriteString("null")
	default:
		return ErrProviderResponse
	}
	return nil
}

// heleketPythonNumber renders a JSON number the way the reference integration
// sees it after parsing: integers keep their digits, anything with a fraction
// or exponent becomes a float printed by Python's repr.
func heleketPythonNumber(literal string) string {
	if !strings.ContainsAny(literal, ".eE") {
		return literal
	}
	value, err := strconv.ParseFloat(literal, 64)
	if err != nil {
		return literal
	}
	return heleketPythonFloat(value)
}

func heleketPythonFloat(value float64) string {
	scientific := strconv.FormatFloat(value, 'e', -1, 64)
	mark := strings.IndexByte(scientific, 'e')
	exponent, _ := strconv.Atoi(scientific[mark+1:])
	if exponent < -4 || exponent > 15 {
		return scientific
	}
	fixed := strconv.FormatFloat(value, 'f', -1, 64)
	if !strings.Contains(fixed, ".") {
		fixed += ".0"
	}
	return fixed
}

// heleketWriteString writes a JSON string as Python's json.dumps does with
// ensure_ascii disabled, then escapes slashes as the reference client does.
func heleketWriteString(out *bytes.Buffer, value string) {
	out.WriteByte('"')
	for index := 0; index < len(value); {
		r, size := utf8.DecodeRuneInString(value[index:])
		switch {
		case r == '"':
			out.WriteString(`\"`)
		case r == '\\':
			out.WriteString(`\\`)
		case r == '/':
			out.WriteString(`\/`)
		case r == '\n':
			out.WriteString(`\n`)
		case r == '\r':
			out.WriteString(`\r`)
		case r == '\t':
			out.WriteString(`\t`)
		case r == '\b':
			out.WriteString(`\b`)
		case r == '\f':
			out.WriteString(`\f`)
		case r < 0x20:
			fmt.Fprintf(out, `\u%04x`, r)
		default:
			out.WriteString(value[index : index+size])
		}
		index += size
	}
	out.WriteByte('"')
}

var (
	_ Provider        = (*Heleket)(nil)
	_ CurrencySupport = (*Heleket)(nil)
	_ ConnectionProbe = (*Heleket)(nil)
)
