package payments

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
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
	"unicode/utf8"

	"github.com/omniflow/omniflow/internal/commerce"
)

// SeverPay settles through SeverPay (severpay.io).
//
// Every request and every notification is signed the same way: the top-level
// fields other than `sign` are sorted by key, serialised the way Python's
// json.dumps(..., ensure_ascii=False, separators=(",", ":")) does, and
// HMAC-SHA256'd with the merchant token. Requests also carry the merchant ID
// (`mid`) and a random `salt`. Notifications must be answered with
// {"status":true}.
type SeverPay struct {
	mid       int64
	token     string
	currency  string
	exponent  int
	lifetime  int
	returnURL string
	baseURL   string
	http      *http.Client
	clock     func() time.Time
	salt      func() (string, error)
}

const (
	severpayAPIBaseURL = "https://severpay.io/api/merchant"
	// severpayDefaultLifetime is Bedolaga's default payment lifetime in
	// minutes; SeverPay accepts 30 to 4320.
	severpayDefaultLifetime = 1440
	// severpayClientID and severpayClientEmail are what Bedolaga sends for a
	// guest checkout. Omniflow does not hand a customer identifier to the
	// provider, so every checkout is sent that way.
	severpayClientID    = "guest"
	severpayClientEmail = "guest@telegram.org"
)

func buildSeverPay(settings Settings) (Provider, error) {
	rawMID := settings.String("APP_SEVERPAY_MID")
	token := settings.String("APP_SEVERPAY_TOKEN")
	switch {
	case rawMID == "" && token == "":
		return nil, ErrNotConfigured
	case rawMID == "":
		return nil, errors.New("SeverPay is partially configured: APP_SEVERPAY_MID must also be set")
	case token == "":
		return nil, errors.New("SeverPay is partially configured: APP_SEVERPAY_TOKEN must also be set")
	}
	mid, err := strconv.ParseInt(rawMID, 10, 64)
	if err != nil || mid <= 0 {
		return nil, errors.New("APP_SEVERPAY_MID must be a positive integer")
	}
	currency := strings.ToUpper(settings.String("APP_SEVERPAY_CURRENCY"))
	if currency == "" {
		currency = "RUB"
	}
	exponent, err := currencyExponent(currency)
	if err != nil {
		return nil, fmt.Errorf("SeverPay currency %q is not supported", currency)
	}
	lifetime := severpayDefaultLifetime
	if raw := settings.String("APP_SEVERPAY_LIFETIME"); raw != "" {
		lifetime, err = strconv.Atoi(raw)
		if err != nil || lifetime < 30 || lifetime > 4320 {
			return nil, errors.New("APP_SEVERPAY_LIFETIME must be between 30 and 4320 minutes")
		}
	}
	return &SeverPay{
		mid: mid, token: token, currency: currency, exponent: exponent, lifetime: lifetime,
		returnURL: settings.String("APP_SEVERPAY_RETURN_URL"), baseURL: severpayAPIBaseURL,
		http: tracedClient(30 * time.Second), clock: time.Now, salt: severpaySalt,
	}, nil
}

// severpaySalt is a random 32-hex-digit salt, the uuid4().hex Bedolaga sends.
func severpaySalt() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate SeverPay salt: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

func (provider *SeverPay) Name() string { return "severpay" }

func (provider *SeverPay) Capabilities() Capabilities {
	return Capabilities{Webhooks: true, Polling: true}
}

// SupportsCurrency reports the single currency this merchant is configured to take.
func (provider *SeverPay) SupportsCurrency(currency string) bool {
	return strings.ToUpper(strings.TrimSpace(currency)) == provider.currency
}

// severpayPayment is the `data` object of a payin, as returned by the API and
// posted in notifications.
type severpayPayment struct {
	ID       severpayText `json:"id"`
	UID      string       `json:"uid"`
	OrderID  string       `json:"order_id"`
	Status   string       `json:"status"`
	URL      string       `json:"url"`
	ExpireAt severpayText `json:"expire_at"`
	Amount   json.Number  `json:"amount"`
	Currency string       `json:"currency"`
}

type severpayEnvelope struct {
	Status bool            `json:"status"`
	Msg    string          `json:"msg"`
	Data   severpayPayment `json:"data"`
}

func (provider *SeverPay) Create(ctx context.Context, request CreateRequest) (Intent, error) {
	if !provider.SupportsCurrency(request.Amount.Currency) {
		return Intent{}, commerce.ErrCurrencyMismatch
	}
	if request.Amount.Amount <= 0 {
		return Intent{}, errors.New("SeverPay amount must be positive")
	}
	amount := float64(request.Amount.Amount) / math.Pow10(provider.exponent)
	fields := []severpayField{
		{key: "order_id", value: severpayPyString(request.OrderID)},
		{key: "amount", value: severpayPyFloat(amount)},
		{key: "currency", value: severpayPyString(provider.currency)},
		{key: "client_email", value: severpayPyString(severpayClientEmail)},
		{key: "client_id", value: severpayPyString(severpayClientID)},
		{key: "lifetime", value: strconv.Itoa(provider.lifetime)},
	}
	returnURL := request.ReturnURL
	if returnURL == "" {
		returnURL = provider.returnURL
	}
	if returnURL != "" {
		fields = append(fields, severpayField{key: "url_return", value: severpayPyString(returnURL)})
	}
	var envelope severpayEnvelope
	if err := provider.call(ctx, "/payin/create", fields, &envelope); err != nil {
		return Intent{}, err
	}
	if !envelope.Status || envelope.Data.ID == "" || envelope.Data.URL == "" {
		return Intent{}, fmt.Errorf("%w: SeverPay payin was not created", ErrProviderResponse)
	}
	intent := Intent{
		ProviderReference: string(envelope.Data.ID), Status: severpayStatus(envelope.Data.Status),
		CheckoutURL: envelope.Data.URL, Amount: request.Amount,
	}
	if expires, err := strconv.ParseInt(string(envelope.Data.ExpireAt), 10, 64); err == nil && expires > 0 {
		at := time.Unix(expires, 0).UTC()
		intent.ExpiresAt = &at
	}
	return intent, nil
}

func (provider *SeverPay) Poll(ctx context.Context, reference string) (Intent, error) {
	var envelope severpayEnvelope
	if err := provider.call(ctx, "/payin/get", []severpayField{{key: "id", value: severpayPyString(reference)}}, &envelope); err != nil {
		return Intent{}, err
	}
	if !envelope.Status || envelope.Data.Status == "" {
		return Intent{}, fmt.Errorf("%w: SeverPay payin lookup was rejected", ErrProviderResponse)
	}
	status := severpayStatus(envelope.Data.Status)
	amount, err := provider.amount(envelope.Data, status)
	if err != nil {
		return Intent{}, err
	}
	return Intent{ProviderReference: reference, Status: status, CheckoutURL: envelope.Data.URL, Amount: amount}, nil
}

func (provider *SeverPay) Refund(context.Context, RefundRequest) (Refund, error) {
	return Refund{}, ErrUnsupported
}

func (provider *SeverPay) VerifyWebhook(_ http.Header, body []byte) (WebhookEvent, error) {
	canonical, sign, err := severpayCanonical(body)
	if err != nil {
		return WebhookEvent{}, ErrProviderResponse
	}
	provided, err := hex.DecodeString(sign)
	if err != nil || len(provided) == 0 || !hmac.Equal(provided, severpaySign(provider.token, canonical)) {
		return WebhookEvent{}, ErrInvalidSignature
	}
	var notification struct {
		Type string          `json:"type"`
		Data severpayPayment `json:"data"`
	}
	if err := json.Unmarshal(body, &notification); err != nil || notification.Data.ID == "" || notification.Data.Status == "" {
		return WebhookEvent{}, ErrProviderResponse
	}
	// Only payins settle an order. Any other notification type is recorded
	// and left alone, as Bedolaga acknowledged and skipped it.
	status := "pending"
	if notification.Type == "payin" {
		status = severpayStatus(notification.Data.Status)
	}
	amount, err := provider.amount(notification.Data, status)
	if err != nil {
		return WebhookEvent{}, err
	}
	return WebhookEvent{
		ID:   notification.Type + ":" + string(notification.Data.ID) + ":" + notification.Data.Status,
		Type: notification.Type, ProviderReference: string(notification.Data.ID),
		MerchantReference: notification.Data.OrderID, Status: status, Amount: amount,
		OccurredAt: provider.clock().UTC(),
	}, nil
}

// WebhookAck answers with the body SeverPay expects; it retries anything but
// a 200.
func (provider *SeverPay) WebhookAck(string) (int, string, []byte) {
	return http.StatusOK, "application/json", []byte(`{"status":true}`)
}

func (provider *SeverPay) amount(payment severpayPayment, status string) (commerce.Money, error) {
	if payment.Amount == "" {
		if status == "succeeded" {
			return commerce.Money{}, fmt.Errorf("%w: successful SeverPay payin carries no amount", ErrProviderResponse)
		}
		return commerce.Money{}, nil
	}
	currency := strings.ToUpper(payment.Currency)
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

// severpayStatus maps a payin status; an unknown one stays pending.
func severpayStatus(status string) string {
	switch status {
	case "success":
		return "succeeded"
	case "process":
		return "processing"
	case "decline", "fail":
		return "failed"
	default:
		return "pending"
	}
}

// severpayField is one top-level request field, already rendered as Python's
// json.dumps renders it, so the signed text and the sent body are the same
// bytes.
type severpayField struct {
	key   string
	value string
}

func severpayRender(fields []severpayField) string {
	sorted := append([]severpayField(nil), fields...)
	sort.Slice(sorted, func(left, right int) bool { return sorted[left].key < sorted[right].key })
	var builder strings.Builder
	builder.WriteByte('{')
	for index, field := range sorted {
		if index > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(severpayPyString(field.key))
		builder.WriteByte(':')
		builder.WriteString(field.value)
	}
	builder.WriteByte('}')
	return builder.String()
}

func severpaySign(token, canonical string) []byte {
	mac := hmac.New(sha256.New, []byte(token))
	_, _ = mac.Write([]byte(canonical))
	return mac.Sum(nil)
}

func (provider *SeverPay) call(ctx context.Context, path string, fields []severpayField, target any) error {
	salt, err := provider.salt()
	if err != nil {
		return err
	}
	fields = append(fields,
		severpayField{key: "mid", value: strconv.FormatInt(provider.mid, 10)},
		severpayField{key: "salt", value: severpayPyString(salt)},
	)
	signature := hex.EncodeToString(severpaySign(provider.token, severpayRender(fields)))
	fields = append(fields, severpayField{key: "sign", value: severpayPyString(signature)})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, provider.baseURL+path, strings.NewReader(severpayRender(fields)))
	if err != nil {
		return fmt.Errorf("create provider request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	response, err := provider.http.Do(request)
	if err != nil {
		return fmt.Errorf("call provider: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxProviderBody))
		return &StatusError{StatusCode: response.StatusCode}
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxProviderBody)).Decode(target); err != nil {
		return fmt.Errorf("decode provider response: %w", err)
	}
	return nil
}

// severpayCanonical renders a notification the way it was signed: the
// top-level object without `sign`, keys sorted, nested values in the order
// they arrived, in Python's compact json.dumps form. It also returns the
// `sign` value.
func severpayCanonical(body []byte) (string, string, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil {
		return "", "", err
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		return "", "", errors.New("notification is not an object")
	}
	values := map[string]string{}
	sign := ""
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return "", "", err
		}
		key, _ := keyToken.(string)
		if key == "sign" {
			var value any
			if err := decoder.Decode(&value); err != nil {
				return "", "", err
			}
			sign, _ = value.(string)
			continue
		}
		rendered, err := severpayValue(decoder)
		if err != nil {
			return "", "", err
		}
		values[key] = rendered
	}
	if _, err := decoder.Token(); err != nil {
		return "", "", err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return "", "", errors.New("trailing data after notification")
	}
	fields := make([]severpayField, 0, len(values))
	for key, value := range values {
		fields = append(fields, severpayField{key: key, value: value})
	}
	return severpayRender(fields), sign, nil
}

func severpayValue(decoder *json.Decoder) (string, error) {
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
				rendered, err := severpayValue(decoder)
				if err != nil {
					return "", err
				}
				if !first {
					builder.WriteByte(',')
				}
				builder.WriteString(severpayPyString(key))
				builder.WriteByte(':')
				builder.WriteString(rendered)
			}
			builder.WriteByte('}')
		case '[':
			builder.WriteByte('[')
			for first := true; decoder.More(); first = false {
				rendered, err := severpayValue(decoder)
				if err != nil {
					return "", err
				}
				if !first {
					builder.WriteByte(',')
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
		return severpayPyString(value), nil
	case json.Number:
		return severpayPyNumber(value)
	case bool:
		if value {
			return "true", nil
		}
		return "false", nil
	case nil:
		return "null", nil
	default:
		return "", errors.New("unexpected token")
	}
}

// severpayPyNumber renders a JSON number as Python re-serialises it: an
// integer keeps its digits, anything with a fraction or exponent becomes a
// float and is printed by repr.
func severpayPyNumber(number json.Number) (string, error) {
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
	return severpayPyFloat(value), nil
}

// severpayPyFloat is Python's repr of a float: the shortest round-tripping
// digits, positional between 1e-4 and 1e16 with at least one fractional digit,
// scientific outside it.
func severpayPyFloat(value float64) string {
	if value == 0 {
		if math.Signbit(value) {
			return "-0.0"
		}
		return "0.0"
	}
	scientific := strconv.FormatFloat(value, 'e', -1, 64)
	exponent, _ := strconv.Atoi(scientific[strings.IndexByte(scientific, 'e')+1:])
	if exponent < -4 || exponent >= 16 {
		return scientific
	}
	fixed := strconv.FormatFloat(value, 'f', -1, 64)
	if !strings.Contains(fixed, ".") {
		fixed += ".0"
	}
	return fixed
}

// severpayPyString is a JSON string as Python writes it with
// ensure_ascii=False: only quotes, backslashes, and control characters are
// escaped; everything else is written as UTF-8.
func severpayPyString(value string) string {
	var builder strings.Builder
	builder.WriteByte('"')
	for len(value) > 0 {
		r, size := utf8.DecodeRuneInString(value)
		value = value[size:]
		switch r {
		case '"':
			builder.WriteString(`\"`)
		case '\\':
			builder.WriteString(`\\`)
		case '\n':
			builder.WriteString(`\n`)
		case '\r':
			builder.WriteString(`\r`)
		case '\t':
			builder.WriteString(`\t`)
		case '\b':
			builder.WriteString(`\b`)
		case '\f':
			builder.WriteString(`\f`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&builder, `\u%04x`, r)
			} else {
				builder.WriteRune(r)
			}
		}
	}
	builder.WriteByte('"')
	return builder.String()
}

// severpayText accepts a JSON string or number.
type severpayText string

func (text *severpayText) UnmarshalJSON(raw []byte) error {
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
		*text = severpayText(value)
		return nil
	}
	*text = severpayText(raw)
	return nil
}

var (
	_ Provider            = (*SeverPay)(nil)
	_ CurrencySupport     = (*SeverPay)(nil)
	_ WebhookAcknowledger = (*SeverPay)(nil)
)
