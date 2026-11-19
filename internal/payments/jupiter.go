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
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/omniflow/omniflow/internal/commerce"
)

// Jupiter is the adapter for Jupiter (app.juppiter.tech), an FPGate P2P v2.1
// gateway.
//
// Each request and each callback carries a `signature` field: a hex
// HMAC-SHA256, keyed with the merchant secret, of selected fields concatenated
// as `name=value` with no separator, in a fixed order, skipping absent and
// empty values. Amounts are major units formatted `0.00`.
//
// FPGate requires a customer email, phone and name. Bedolaga fills absent
// ones from configurable fallbacks, and so does this adapter.
//
// Bedolaga reads the status endpoint but never interprets its answer, so this
// adapter does not poll: settlement arrives only through the signed callback.
type Jupiter struct {
	token       string
	secret      string
	currency    string
	description string
	methodID    string
	fallback    jupiterCustomer
	baseURL     string
	callbackURL string
	http        *http.Client
	clock       func() time.Time
}

const jupiterDefaultBaseURL = "https://app.juppiter.tech"

func buildJupiter(settings Settings) (Provider, error) {
	token := settings.String("APP_JUPITER_TOKEN")
	secret := settings.String("APP_JUPITER_SECRET")
	if token == "" && secret == "" {
		return nil, ErrNotConfigured
	}
	if token == "" || secret == "" {
		return nil, errors.New("Jupiter needs both APP_JUPITER_TOKEN and APP_JUPITER_SECRET")
	}
	currency := strings.ToUpper(settings.String("APP_JUPITER_CURRENCY"))
	if currency == "" {
		currency = "RUB"
	}
	if _, err := currencyExponent(currency); err != nil {
		return nil, fmt.Errorf("APP_JUPITER_CURRENCY: %w", err)
	}
	description := settings.String("APP_JUPITER_METHOD_DESCRIPTION")
	if description == "" {
		description = "SBP"
	}
	fallback := jupiterCustomer{
		Email: jupiterDefault(settings.String("APP_JUPITER_FALLBACK_EMAIL"), "user@vpn.bot"),
		Phone: jupiterDefault(settings.String("APP_JUPITER_FALLBACK_PHONE"), "0000000000"),
		Name:  jupiterDefault(settings.String("APP_JUPITER_FALLBACK_NAME"), "User"),
	}
	baseURL := strings.TrimSuffix(settings.String("APP_JUPITER_BASE_URL"), "/")
	if baseURL == "" {
		baseURL = jupiterDefaultBaseURL
	}
	return &Jupiter{
		token: token, secret: secret, currency: currency, description: description, fallback: fallback,
		methodID: settings.String("APP_JUPITER_METHOD_ID"), baseURL: baseURL,
		callbackURL: settings.WebhookURL("jupiter"), http: tracedClient(30 * time.Second), clock: time.Now,
	}, nil
}

func (provider *Jupiter) Name() string { return "jupiter" }

// Capabilities declares webhooks only: no refund call, no saved-method
// charge, and no status answer this adapter can interpret.
func (provider *Jupiter) Capabilities() Capabilities {
	return Capabilities{Webhooks: true}
}

// SupportsCurrency reports the single currency the merchant configured.
func (provider *Jupiter) SupportsCurrency(currency string) bool {
	return strings.EqualFold(strings.TrimSpace(currency), provider.currency)
}

type jupiterAmount struct {
	Value    string `json:"value"`
	Currency string `json:"currency"`
}

type jupiterCustomer struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Phone string `json:"phone"`
	Name  string `json:"name"`
}

type jupiterPayinBody struct {
	Token       string          `json:"token"`
	OrderID     string          `json:"order_id"`
	Amount      jupiterAmount   `json:"amount"`
	Customer    jupiterCustomer `json:"customer"`
	Redirect    string          `json:"redirect"`
	Description string          `json:"description"`
	MethodID    string          `json:"method_id,omitempty"`
	CallbackURL string          `json:"callback_url,omitempty"`
	Signature   string          `json:"signature"`
}

func (provider *Jupiter) Create(ctx context.Context, request CreateRequest) (Intent, error) {
	if !provider.SupportsCurrency(request.Amount.Currency) || request.Amount.Amount <= 0 {
		return Intent{}, fmt.Errorf("%w: Jupiter is configured for %s only", ErrUnsupported, provider.currency)
	}
	exponent, err := currencyExponent(provider.currency)
	if err != nil {
		return Intent{}, err
	}
	customer := strings.TrimSpace(request.Metadata["customer_id"])
	if customer == "" {
		customer = request.OrderID
	}
	body := jupiterPayinBody{
		Token:   provider.token,
		OrderID: request.OrderID,
		Amount:  jupiterAmount{Value: jupiterFormatAmount(request.Amount.Amount, exponent), Currency: provider.currency},
		Customer: jupiterCustomer{
			ID:    customer,
			Email: jupiterDefault(request.Metadata["customer_email"], provider.fallback.Email),
			Phone: jupiterDefault(request.Metadata["customer_phone"], provider.fallback.Phone),
			Name:  jupiterDefault(request.Metadata["customer_name"], provider.fallback.Name),
		},
		Redirect:    "false",
		Description: jupiterTruncate(jupiterDefault(request.Description, provider.description), 255),
		MethodID:    provider.methodID,
		CallbackURL: provider.callbackURL,
	}
	body.Signature = provider.sign([][2]string{
		{"token", body.Token}, {"order_id", body.OrderID}, {"amount.value", body.Amount.Value},
		{"amount.currency", body.Amount.Currency}, {"customer.id", body.Customer.ID}, {"redirect", body.Redirect},
	})
	var answer struct {
		TransactionID jupiterID `json:"transaction_id"`
		Details       struct {
			QRCodeURL string `json:"qrcode_url"`
			URL       string `json:"url"`
		} `json:"details"`
		Status struct {
			Type string `json:"type"`
		} `json:"status"`
	}
	if err := provider.post(ctx, "/p2p_payin_v2.1", body, &answer); err != nil {
		return Intent{}, err
	}
	checkout := answer.Details.QRCodeURL
	if checkout == "" {
		checkout = answer.Details.URL
	}
	if answer.TransactionID == "" || checkout == "" {
		return Intent{}, fmt.Errorf("%w: Jupiter returned no transaction or payment URL", ErrProviderResponse)
	}
	return Intent{
		ProviderReference: string(answer.TransactionID),
		Status:            jupiterStatus(answer.Status.Type),
		CheckoutURL:       checkout,
		Amount:            request.Amount,
	}, nil
}

func (provider *Jupiter) Poll(context.Context, string) (Intent, error) {
	return Intent{}, ErrUnsupported
}

func (provider *Jupiter) Refund(context.Context, RefundRequest) (Refund, error) {
	return Refund{}, ErrUnsupported
}

// VerifyWebhook recomputes the callback signature over token, transaction_id,
// order_id, amount.value, amount.currency, recalculated and status.type, and
// compares it in constant time with the `signature` field.
func (provider *Jupiter) VerifyWebhook(_ http.Header, body []byte) (WebhookEvent, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil {
		return WebhookEvent{}, ErrInvalidSignature
	}
	received, _ := payload["signature"].(string)
	provided, err := hex.DecodeString(strings.ToLower(strings.TrimSpace(received)))
	if err != nil || len(provided) != sha256.Size {
		return WebhookEvent{}, ErrInvalidSignature
	}
	amount, _ := payload["amount"].(map[string]any)
	status, _ := payload["status"].(map[string]any)
	fields := [][2]any{
		{"token", payload["token"]}, {"transaction_id", payload["transaction_id"]}, {"order_id", payload["order_id"]},
		{"amount.value", jupiterField(amount, "value")}, {"amount.currency", jupiterField(amount, "currency")},
		{"recalculated", payload["recalculated"]}, {"status.type", jupiterField(status, "type")},
	}
	parts := make([][2]string, 0, len(fields))
	for _, field := range fields {
		value, ok, err := jupiterSignatureValue(field[1])
		if err != nil {
			return WebhookEvent{}, ErrInvalidSignature
		}
		if ok {
			parts = append(parts, [2]string{field[0].(string), value})
		}
	}
	expected, _ := hex.DecodeString(provider.sign(parts))
	if !hmac.Equal(provided, expected) {
		return WebhookEvent{}, ErrInvalidSignature
	}
	transaction, _, _ := jupiterSignatureValue(payload["transaction_id"])
	order, _ := payload["order_id"].(string)
	statusType, _ := jupiterField(status, "type").(string)
	statusType = strings.ToLower(strings.TrimSpace(statusType))
	if order == "" || statusType == "" {
		return WebhookEvent{}, fmt.Errorf("%w: Jupiter callback has no order or status", ErrProviderResponse)
	}
	event := WebhookEvent{
		ID:                statusType + ":" + transaction,
		Type:              statusType,
		ProviderReference: transaction,
		MerchantReference: order,
		Status:            jupiterStatus(statusType),
		OccurredAt:        provider.clock().UTC(),
	}
	if transaction == "" {
		event.ID = statusType + ":order:" + order
	}
	currency, _ := jupiterField(amount, "currency").(string)
	value, _, _ := jupiterSignatureValue(jupiterField(amount, "value"))
	if parsed, parseErr := jupiterParseAmount(value, currency); parseErr == nil {
		event.Amount = parsed
	} else if event.Status == "succeeded" {
		return WebhookEvent{}, fmt.Errorf("%w: Jupiter success without a readable amount", ErrProviderResponse)
	}
	return event, nil
}

func (provider *Jupiter) sign(parts [][2]string) string {
	var message strings.Builder
	for _, part := range parts {
		if part[1] == "" {
			continue
		}
		message.WriteString(part[0])
		message.WriteByte('=')
		message.WriteString(part[1])
	}
	mac := hmac.New(sha256.New, []byte(provider.secret))
	_, _ = mac.Write([]byte(message.String()))
	return hex.EncodeToString(mac.Sum(nil))
}

// post sends a signed JSON request. Jupiter answers with a status object even
// on failure; a payin whose status is not processing or success was
// refused by Jupiter and moved nothing.
func (provider *Jupiter) post(ctx context.Context, path string, payload, target any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode Jupiter request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, provider.baseURL+path, bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("create Jupiter request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := provider.http.Do(request)
	if err != nil {
		return fmt.Errorf("call Jupiter: %w", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxProviderBody))
	if err != nil {
		return fmt.Errorf("read Jupiter response: %w", err)
	}
	var envelope struct {
		Status *struct {
			Type string `json:"type"`
		} `json:"status"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.Status == nil {
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return &StatusError{StatusCode: response.StatusCode}
		}
		return fmt.Errorf("%w: Jupiter answered without a status", ErrProviderResponse)
	}
	switch envelope.Status.Type {
	case "processing", "success":
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return &StatusError{StatusCode: response.StatusCode}
		}
		if err := json.Unmarshal(raw, target); err != nil {
			return fmt.Errorf("decode Jupiter response: %w", err)
		}
		return nil
	}
	status := response.StatusCode
	if status >= 200 && status < 300 {
		status = http.StatusUnprocessableEntity
	}
	return fmt.Errorf("Jupiter refused the payin (%s): %w", envelope.Status.Type, &StatusError{StatusCode: status})
}

// jupiterStatus normalises a Jupiter status type. An unknown type is pending,
// which Omniflow ignores, as Bedolaga does.
func jupiterStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "success":
		return "succeeded"
	case "cancelled":
		return "cancelled"
	case "declined", "error":
		return "failed"
	default:
		return "pending"
	}
}

// jupiterID is a transaction ID Jupiter may send as a string or a number.
type jupiterID string

func (id *jupiterID) UnmarshalJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	text, _, err := jupiterSignatureValue(value)
	*id = jupiterID(text)
	return err
}

func jupiterField(object map[string]any, key string) any {
	if object == nil {
		return nil
	}
	return object[key]
}

// jupiterSignatureValue renders a decoded JSON value the way Bedolaga's Python
// does when it builds the signature string: strings as they are, booleans as
// true/false, integers as written, and fractional numbers in Python's float
// repr. Absent and empty values are skipped.
func jupiterSignatureValue(value any) (string, bool, error) {
	switch typed := value.(type) {
	case nil:
		return "", false, nil
	case string:
		return typed, typed != "", nil
	case bool:
		if typed {
			return "true", true, nil
		}
		return "false", true, nil
	case json.Number:
		literal := typed.String()
		if !strings.ContainsAny(literal, ".eE") {
			return literal, true, nil
		}
		number, err := typed.Float64()
		if err != nil {
			return "", false, err
		}
		return jupiterPythonFloat(number), true, nil
	default:
		return "", false, errors.New("unsupported signature value")
	}
}

func jupiterPythonFloat(number float64) string {
	if math.IsInf(number, 0) || math.IsNaN(number) {
		return strconv.FormatFloat(number, 'g', -1, 64)
	}
	magnitude := math.Abs(number)
	if magnitude != 0 && (magnitude < 1e-4 || magnitude >= 1e16) {
		return strconv.FormatFloat(number, 'e', -1, 64)
	}
	text := strconv.FormatFloat(number, 'f', -1, 64)
	if !strings.Contains(text, ".") {
		text += ".0"
	}
	return text
}

func jupiterFormatAmount(amount int64, exponent int) string {
	text := formatMinor(amount, exponent)
	if exponent == 0 {
		return text + ".00"
	}
	return text
}

func jupiterParseAmount(value, currency string) (commerce.Money, error) {
	currency = strings.ToUpper(strings.TrimSpace(currency))
	exponent, err := currencyExponent(currency)
	if err != nil {
		return commerce.Money{}, err
	}
	// Python renders 100.00 as 100.0; both mean the same amount.
	if exponent == 0 && strings.Contains(value, ".") {
		value = strings.TrimSuffix(strings.TrimRight(value, "0"), ".")
	}
	amount, err := parseMinor(value, exponent)
	if err != nil || amount <= 0 {
		return commerce.Money{}, errors.New("invalid Jupiter amount")
	}
	return commerce.Money{Amount: amount, Currency: currency}, nil
}

var _ Provider = (*Jupiter)(nil)

func jupiterDefault(value, fallback string) string {
	if trimmed := strings.TrimSpace(value); trimmed != "" {
		return trimmed
	}
	return fallback
}

func jupiterTruncate(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
