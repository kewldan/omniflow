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

// Donut is the adapter for the Donut P2P gateway (gw.donut.business).
//
// Each request and each callback carries a `signature` field: a hex
// HMAC-SHA256, keyed with the merchant secret, of selected fields concatenated
// as `name=value` with no separator, in a fixed order, skipping absent and
// empty values. Amounts are major units formatted `0.00`.
//
// Bedolaga reads Donut's status endpoint but never interprets its answer, so
// this adapter does not poll: settlement arrives only through the signed
// callback.
type Donut struct {
	token       string
	secret      string
	currency    string
	method      string
	methodID    string
	baseURL     string
	callbackURL string
	http        *http.Client
	clock       func() time.Time
}

const donutDefaultBaseURL = "https://gw.donut.business"

func buildDonut(settings Settings) (Provider, error) {
	token := settings.String("APP_DONUT_TOKEN")
	secret := settings.String("APP_DONUT_SECRET")
	if token == "" && secret == "" {
		return nil, ErrNotConfigured
	}
	if token == "" || secret == "" {
		return nil, errors.New("Donut needs both APP_DONUT_TOKEN and APP_DONUT_SECRET")
	}
	currency := strings.ToUpper(settings.String("APP_DONUT_CURRENCY"))
	if currency == "" {
		currency = "RUB"
	}
	if _, err := currencyExponent(currency); err != nil {
		return nil, fmt.Errorf("APP_DONUT_CURRENCY: %w", err)
	}
	method := strings.ToUpper(settings.String("APP_DONUT_METHOD"))
	switch method {
	case "":
		method = "CARD"
	case "CARD", "SBP", "SBP_QR":
	default:
		return nil, fmt.Errorf("APP_DONUT_METHOD must be CARD, SBP or SBP_QR, got %q", method)
	}
	baseURL := strings.TrimSuffix(settings.String("APP_DONUT_BASE_URL"), "/")
	if baseURL == "" {
		baseURL = donutDefaultBaseURL
	}
	return &Donut{
		token: token, secret: secret, currency: currency, method: method,
		methodID: settings.String("APP_DONUT_METHOD_ID"), baseURL: baseURL,
		callbackURL: settings.WebhookURL("donut"), http: tracedClient(30 * time.Second), clock: time.Now,
	}, nil
}

func (provider *Donut) Name() string { return "donut" }

// Capabilities declares webhooks only: no refund call, no saved-method
// charge, and no status answer this adapter can interpret.
func (provider *Donut) Capabilities() Capabilities {
	return Capabilities{Webhooks: true}
}

// SupportsCurrency reports the single currency the merchant configured.
func (provider *Donut) SupportsCurrency(currency string) bool {
	return strings.EqualFold(strings.TrimSpace(currency), provider.currency)
}

type donutAmount struct {
	Value    string `json:"value"`
	Currency string `json:"currency"`
}

type donutCustomer struct {
	ID    string `json:"id"`
	Email string `json:"email,omitempty"`
	Phone string `json:"phone,omitempty"`
}

type donutPayinBody struct {
	Token       string        `json:"token"`
	OrderID     string        `json:"order_id"`
	Amount      donutAmount   `json:"amount"`
	Customer    donutCustomer `json:"customer"`
	Redirect    string        `json:"redirect"`
	Description string        `json:"description"`
	MethodID    string        `json:"method_id,omitempty"`
	CallbackURL string        `json:"callback_url,omitempty"`
	ReturnURL   string        `json:"return_url,omitempty"`
	Signature   string        `json:"signature"`
}

func (provider *Donut) Create(ctx context.Context, request CreateRequest) (Intent, error) {
	if !provider.SupportsCurrency(request.Amount.Currency) || request.Amount.Amount <= 0 {
		return Intent{}, fmt.Errorf("%w: Donut is configured for %s only", ErrUnsupported, provider.currency)
	}
	exponent, err := currencyExponent(provider.currency)
	if err != nil {
		return Intent{}, err
	}
	customer := strings.TrimSpace(request.Metadata["customer_id"])
	if customer == "" {
		customer = request.OrderID
	}
	body := donutPayinBody{
		Token:   provider.token,
		OrderID: request.OrderID,
		Amount:  donutAmount{Value: donutFormatAmount(request.Amount.Amount, exponent), Currency: provider.currency},
		Customer: donutCustomer{
			ID: customer, Email: strings.TrimSpace(request.Metadata["customer_email"]),
			Phone: strings.TrimSpace(request.Metadata["customer_phone"]),
		},
		Redirect: "true",
		// Donut reads the payment method from `description`.
		Description: provider.method,
		MethodID:    provider.methodID,
		CallbackURL: provider.callbackURL,
		ReturnURL:   request.ReturnURL,
	}
	body.Signature = provider.sign([][2]string{
		{"token", body.Token}, {"order_id", body.OrderID}, {"amount.value", body.Amount.Value},
		{"amount.currency", body.Amount.Currency}, {"customer.id", body.Customer.ID}, {"redirect", body.Redirect},
	})
	var answer struct {
		TransactionID donutID `json:"transaction_id"`
		RedirectURL   string  `json:"redirect_url"`
		Details       struct {
			QRCodeURL string `json:"qrcode_url"`
		} `json:"details"`
		Status struct {
			Type string `json:"type"`
		} `json:"status"`
	}
	if err := provider.post(ctx, "/p2p_payin", body, &answer); err != nil {
		return Intent{}, err
	}
	checkout := answer.RedirectURL
	if checkout == "" {
		checkout = answer.Details.QRCodeURL
	}
	if answer.TransactionID == "" || checkout == "" {
		return Intent{}, fmt.Errorf("%w: Donut returned no transaction or payment URL", ErrProviderResponse)
	}
	return Intent{
		ProviderReference: string(answer.TransactionID),
		Status:            donutStatus(answer.Status.Type),
		CheckoutURL:       checkout,
		Amount:            request.Amount,
	}, nil
}

func (provider *Donut) Poll(context.Context, string) (Intent, error) {
	return Intent{}, ErrUnsupported
}

func (provider *Donut) Refund(context.Context, RefundRequest) (Refund, error) {
	return Refund{}, ErrUnsupported
}

// VerifyWebhook recomputes the callback signature over token, transaction_id,
// order_id, amount.value, amount.currency, recalculated and status.type, and
// compares it in constant time with the `signature` field.
func (provider *Donut) VerifyWebhook(_ http.Header, body []byte) (WebhookEvent, error) {
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
		{"amount.value", donutField(amount, "value")}, {"amount.currency", donutField(amount, "currency")},
		{"recalculated", payload["recalculated"]}, {"status.type", donutField(status, "type")},
	}
	parts := make([][2]string, 0, len(fields))
	for _, field := range fields {
		value, ok, err := donutSignatureValue(field[1])
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
	transaction, _, _ := donutSignatureValue(payload["transaction_id"])
	order, _ := payload["order_id"].(string)
	statusType, _ := donutField(status, "type").(string)
	statusType = strings.ToLower(strings.TrimSpace(statusType))
	if order == "" || statusType == "" {
		return WebhookEvent{}, fmt.Errorf("%w: Donut callback has no order or status", ErrProviderResponse)
	}
	event := WebhookEvent{
		ID:                statusType + ":" + transaction,
		Type:              statusType,
		ProviderReference: transaction,
		MerchantReference: order,
		Status:            donutStatus(statusType),
		OccurredAt:        provider.clock().UTC(),
	}
	if transaction == "" {
		event.ID = statusType + ":order:" + order
	}
	currency, _ := donutField(amount, "currency").(string)
	value, _, _ := donutSignatureValue(donutField(amount, "value"))
	if parsed, parseErr := donutParseAmount(value, currency); parseErr == nil {
		event.Amount = parsed
	} else if event.Status == "succeeded" {
		return WebhookEvent{}, fmt.Errorf("%w: Donut success without a readable amount", ErrProviderResponse)
	}
	return event, nil
}

func (provider *Donut) sign(parts [][2]string) string {
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

// post sends a signed JSON request. Donut answers with a status object even
// on failure; a payin whose status is not created, processing or success was
// refused by Donut and moved nothing.
func (provider *Donut) post(ctx context.Context, path string, payload, target any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode Donut request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, provider.baseURL+path, bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("create Donut request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := provider.http.Do(request)
	if err != nil {
		return fmt.Errorf("call Donut: %w", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxProviderBody))
	if err != nil {
		return fmt.Errorf("read Donut response: %w", err)
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
		return fmt.Errorf("%w: Donut answered without a status", ErrProviderResponse)
	}
	switch envelope.Status.Type {
	case "created", "processing", "success":
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return &StatusError{StatusCode: response.StatusCode}
		}
		if err := json.Unmarshal(raw, target); err != nil {
			return fmt.Errorf("decode Donut response: %w", err)
		}
		return nil
	}
	status := response.StatusCode
	if status >= 200 && status < 300 {
		status = http.StatusUnprocessableEntity
	}
	return fmt.Errorf("Donut refused the payin (%s): %w", envelope.Status.Type, &StatusError{StatusCode: status})
}

// donutStatus normalises a Donut status type. An unknown type is pending,
// which Omniflow ignores, as Bedolaga does.
func donutStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "success":
		return "succeeded"
	case "cancelled":
		return "cancelled"
	case "error":
		return "failed"
	default:
		return "pending"
	}
}

// donutID is a transaction ID Donut may send as a string or a number.
type donutID string

func (id *donutID) UnmarshalJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	text, _, err := donutSignatureValue(value)
	*id = donutID(text)
	return err
}

func donutField(object map[string]any, key string) any {
	if object == nil {
		return nil
	}
	return object[key]
}

// donutSignatureValue renders a decoded JSON value the way Bedolaga's Python
// does when it builds the signature string: strings as they are, booleans as
// true/false, integers as written, and fractional numbers in Python's float
// repr. Absent and empty values are skipped.
func donutSignatureValue(value any) (string, bool, error) {
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
		return donutPythonFloat(number), true, nil
	default:
		return "", false, errors.New("unsupported signature value")
	}
}

func donutPythonFloat(number float64) string {
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

func donutFormatAmount(amount int64, exponent int) string {
	text := formatMinor(amount, exponent)
	if exponent == 0 {
		return text + ".00"
	}
	return text
}

func donutParseAmount(value, currency string) (commerce.Money, error) {
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
		return commerce.Money{}, errors.New("invalid Donut amount")
	}
	return commerce.Money{Amount: amount, Currency: currency}, nil
}

var _ Provider = (*Donut)(nil)
