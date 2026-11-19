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
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/omniflow/omniflow/internal/commerce"
)

// Lava settles RUB invoices through the Lava Business API (api.lava.ru).
//
// Every request is a JSON POST signed with HMAC-SHA256 over the exact bytes
// sent, keyed by the shop's secret key and carried in the `Signature` header.
// Notifications are signed with the shop's additional ("webhook") key in the
// `Authorization` header. Lava's product subscriptions charge on Lava's own
// schedule rather than on a merchant's request, so the adapter declares no
// saved-method charging.
type Lava struct {
	shopID        string
	secretKey     string
	webhookSecret string
	hookURL       string
	lifetime      int
	baseURL       string
	http          *http.Client
	clock         func() time.Time
}

const lavaDefaultURL = "https://api.lava.ru"

func buildLava(settings Settings) (Provider, error) {
	shopID := settings.String("APP_LAVA_SHOP_ID")
	secretKey := settings.String("APP_LAVA_SECRET_KEY")
	webhookSecret := settings.String("APP_LAVA_WEBHOOK_SECRET")
	if shopID == "" && secretKey == "" && webhookSecret == "" {
		return nil, ErrNotConfigured
	}
	if shopID == "" || secretKey == "" || webhookSecret == "" {
		return nil, errors.New("Lava needs APP_LAVA_SHOP_ID, APP_LAVA_SECRET_KEY, and APP_LAVA_WEBHOOK_SECRET together")
	}
	hookURL := settings.WebhookURL("lava")
	if hookURL == "" {
		return nil, errors.New("Lava sends a notification URL with every invoice, so APP_PUBLIC_URL is required")
	}
	baseURL := strings.TrimSuffix(settings.String("APP_LAVA_BASE_URL"), "/")
	if baseURL == "" {
		baseURL = lavaDefaultURL
	}
	// Lava accepts an invoice lifetime of 1 to 7200 minutes.
	lifetime := min(max(settings.Int("APP_LAVA_PAYMENT_LIFETIME_MINUTES", 60), 1), 7200)
	return &Lava{
		shopID: shopID, secretKey: secretKey, webhookSecret: webhookSecret, hookURL: hookURL,
		lifetime: lifetime, baseURL: baseURL, http: tracedClient(30 * time.Second), clock: time.Now,
	}, nil
}

func (provider *Lava) Name() string { return "lava" }

// Capabilities declares signed webhooks and status polling. The Business API
// has no refund call, and its subscriptions are product-scheduled rather than
// charged on demand.
func (provider *Lava) Capabilities() Capabilities {
	return Capabilities{Webhooks: true, Polling: true}
}

// SupportsCurrency reports RUB only: Lava invoices carry a rouble sum and no
// currency field.
func (provider *Lava) SupportsCurrency(currency string) bool {
	return strings.ToUpper(strings.TrimSpace(currency)) == "RUB"
}

// lavaInvoiceRequest keeps Lava's field order. The signature covers the bytes
// as sent, so the order only has to be stable, but a struct makes it so.
type lavaInvoiceRequest struct {
	Sum        json.Number `json:"sum"`
	OrderID    string      `json:"orderId"`
	ShopID     string      `json:"shopId"`
	HookURL    string      `json:"hookUrl,omitempty"`
	SuccessURL string      `json:"successUrl,omitempty"`
	FailURL    string      `json:"failUrl,omitempty"`
	Expire     int         `json:"expire,omitempty"`
	Comment    string      `json:"comment,omitempty"`
}

type lavaInvoice struct {
	ID      string          `json:"id"`
	OrderID string          `json:"order_id"`
	Status  string          `json:"status"`
	Amount  json.RawMessage `json:"amount"`
	URL     string          `json:"url"`
	Expire  string          `json:"expire"`
}

func (provider *Lava) Create(ctx context.Context, request CreateRequest) (Intent, error) {
	if !provider.SupportsCurrency(request.Amount.Currency) {
		return Intent{}, commerce.ErrCurrencyMismatch
	}
	returnURL := lavaStripQuery(request.ReturnURL)
	body := lavaInvoiceRequest{
		Sum: json.Number(formatMinor(request.Amount.Amount, 2)), OrderID: request.OrderID, ShopID: provider.shopID,
		HookURL: lavaTruncate(provider.hookURL, 500), SuccessURL: lavaTruncate(returnURL, 500), FailURL: lavaTruncate(returnURL, 500),
		Expire: provider.lifetime, Comment: lavaTruncate(request.Description, 255),
	}
	invoice, err := provider.post(ctx, "/business/invoice/create", body)
	if err != nil {
		return Intent{}, err
	}
	if invoice.URL == "" || invoice.ID == "" {
		return Intent{}, fmt.Errorf("%w: invoice/create returned no checkout link", ErrProviderResponse)
	}
	intent := Intent{ProviderReference: invoice.ID, Status: "pending", CheckoutURL: invoice.URL, Amount: request.Amount}
	if expires, err := time.Parse(time.RFC3339, invoice.Expire); err == nil {
		utc := expires.UTC()
		intent.ExpiresAt = &utc
	}
	return intent, nil
}

// Poll reads `/business/invoice/status` by Lava's invoice ID.
func (provider *Lava) Poll(ctx context.Context, reference string) (Intent, error) {
	invoice, err := provider.post(ctx, "/business/invoice/status", map[string]string{"shopId": provider.shopID, "invoiceId": reference})
	if err != nil {
		return Intent{}, err
	}
	if invoice.ID != "" && invoice.ID != reference {
		return Intent{}, fmt.Errorf("%w: invoice mismatch", ErrProviderResponse)
	}
	intent := Intent{ProviderReference: reference, Status: normalizeLavaStatus(invoice.Status), CheckoutURL: invoice.URL}
	if len(invoice.Amount) > 0 && string(invoice.Amount) != "null" {
		minor, err := lavaMinor(invoice.Amount)
		if err != nil {
			return Intent{}, err
		}
		intent.Amount = commerce.Money{Amount: minor, Currency: "RUB"}
	}
	return intent, nil
}

func (provider *Lava) Refund(context.Context, RefundRequest) (Refund, error) {
	return Refund{}, ErrUnsupported
}

// VerifyWebhook checks the `Authorization` HMAC and reads the invoice.
//
// Lava signs the raw body on current shops and a key-sorted re-serialisation
// on shops created for the old PHP SDK; a notification is accepted when
// either matches, as the reference integration does.
func (provider *Lava) VerifyWebhook(headers http.Header, body []byte) (WebhookEvent, error) {
	if !lavaSignatureValid(provider.webhookSecret, body, headers.Get("Authorization")) {
		return WebhookEvent{}, ErrInvalidSignature
	}
	var notification struct {
		InvoiceID string          `json:"invoice_id"`
		OrderID   string          `json:"order_id"`
		Status    string          `json:"status"`
		Amount    json.RawMessage `json:"amount"`
	}
	if err := json.Unmarshal(body, &notification); err != nil || notification.OrderID == "" || notification.Status == "" {
		return WebhookEvent{}, ErrProviderResponse
	}
	status := strings.ToLower(strings.TrimSpace(notification.Status))
	reference := notification.InvoiceID
	if reference == "" {
		reference = notification.OrderID
	}
	event := WebhookEvent{
		ID: status + ":" + reference, Type: status,
		ProviderReference: notification.InvoiceID, MerchantReference: notification.OrderID,
		Status: normalizeLavaStatus(status), OccurredAt: provider.clock().UTC(),
	}
	if len(notification.Amount) > 0 && string(notification.Amount) != "null" {
		minor, err := lavaMinor(notification.Amount)
		if err != nil {
			return WebhookEvent{}, err
		}
		event.Amount = commerce.Money{Amount: minor, Currency: "RUB"}
	}
	return event, nil
}

// Probe lists the payment methods available to the shop, an authenticated
// read that creates nothing.
func (provider *Lava) Probe(ctx context.Context) error {
	_, err := provider.post(ctx, "/business/invoice/get-available-tariffs", map[string]string{"shopId": provider.shopID})
	return err
}

// post sends a signed request and unwraps Lava's `{"data": …}` envelope.
func (provider *Lava) post(ctx context.Context, path string, payload any) (lavaInvoice, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return lavaInvoice{}, fmt.Errorf("encode provider request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, provider.baseURL+path, bytes.NewReader(encoded))
	if err != nil {
		return lavaInvoice{}, fmt.Errorf("create provider request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Signature", lavaHMAC(provider.secretKey, encoded))
	response, err := provider.http.Do(request)
	if err != nil {
		return lavaInvoice{}, fmt.Errorf("call provider: %w", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxProviderBody))
	if err != nil {
		return lavaInvoice{}, fmt.Errorf("read provider response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return lavaInvoice{}, &StatusError{StatusCode: response.StatusCode}
	}
	var envelope struct {
		Status json.RawMessage `json:"status"`
		Data   json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return lavaInvoice{}, fmt.Errorf("decode provider response: %w", err)
	}
	var status string
	if json.Unmarshal(envelope.Status, &status) == nil && strings.EqualFold(status, "error") {
		return lavaInvoice{}, fmt.Errorf("%w: Lava answered with an error", ErrProviderResponse)
	}
	var invoice lavaInvoice
	if len(envelope.Data) > 0 && envelope.Data[0] == '{' {
		err = json.Unmarshal(envelope.Data, &invoice)
	} else if len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		err = json.Unmarshal(raw, &invoice)
	}
	if err != nil {
		return lavaInvoice{}, fmt.Errorf("decode provider response: %w", err)
	}
	return invoice, nil
}

func lavaHMAC(secret string, message []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(message)
	return hex.EncodeToString(mac.Sum(nil))
}

func lavaSignatureValid(secret string, body []byte, signature string) bool {
	received := []byte(strings.ToLower(strings.TrimSpace(signature)))
	if secret == "" || len(received) == 0 {
		return false
	}
	if hmac.Equal([]byte(lavaHMAC(secret, body)), received) {
		return true
	}
	canonical, err := lavaCanonical(body)
	return err == nil && hmac.Equal([]byte(lavaHMAC(secret, canonical)), received)
}

// lavaCanonical reproduces the legacy signing form: the body re-serialised
// with sorted keys, no whitespace, ASCII-only escapes, integral numbers
// without a fraction, and every `signature` field removed.
func lavaCanonical(body []byte) ([]byte, error) {
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("canonical form needs an object")
	}
	var buffer bytes.Buffer
	lavaCanonicalWrite(&buffer, object)
	return buffer.Bytes(), nil
}

func lavaCanonicalWrite(buffer *bytes.Buffer, value any) {
	switch typed := value.(type) {
	case nil:
		buffer.WriteString("null")
	case bool:
		buffer.WriteString(strconv.FormatBool(typed))
	case float64:
		buffer.WriteString(lavaCanonicalNumber(typed))
	case string:
		lavaCanonicalString(buffer, typed)
	case []any:
		buffer.WriteByte('[')
		for index, item := range typed {
			if index > 0 {
				buffer.WriteByte(',')
			}
			lavaCanonicalWrite(buffer, item)
		}
		buffer.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			if key != "signature" {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		buffer.WriteByte('{')
		for index, key := range keys {
			if index > 0 {
				buffer.WriteByte(',')
			}
			lavaCanonicalString(buffer, key)
			buffer.WriteByte(':')
			lavaCanonicalWrite(buffer, typed[key])
		}
		buffer.WriteByte('}')
	}
}

func lavaCanonicalNumber(value float64) string {
	if value == math.Trunc(value) && math.Abs(value) < 1e18 {
		return strconv.FormatInt(int64(value), 10)
	}
	if abs := math.Abs(value); abs >= 1e-4 && abs < 1e16 {
		return strconv.FormatFloat(value, 'f', -1, 64)
	}
	return strconv.FormatFloat(value, 'e', -1, 64)
}

func lavaCanonicalString(buffer *bytes.Buffer, value string) {
	buffer.WriteByte('"')
	for _, r := range value {
		switch {
		case r == '"':
			buffer.WriteString(`\"`)
		case r == '\\':
			buffer.WriteString(`\\`)
		case r == '\n':
			buffer.WriteString(`\n`)
		case r == '\r':
			buffer.WriteString(`\r`)
		case r == '\t':
			buffer.WriteString(`\t`)
		case r == '\b':
			buffer.WriteString(`\b`)
		case r == '\f':
			buffer.WriteString(`\f`)
		case r < 0x20 || (r >= 0x7f && r < 0x10000) || r == utf8.RuneError:
			fmt.Fprintf(buffer, `\u%04x`, r)
		case r >= 0x10000:
			r -= 0x10000
			fmt.Fprintf(buffer, `\u%04x\u%04x`, 0xd800+(r>>10), 0xdc00+(r&0x3ff))
		default:
			buffer.WriteRune(r)
		}
	}
	buffer.WriteByte('"')
}

// lavaMinor reads a rouble sum sent as a number or a string. A sum with more
// than two decimals is rounded to the kopeck, as the reference integration
// does with Lava's float amounts.
func lavaMinor(raw json.RawMessage) (int64, error) {
	text := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	if minor, err := parseMinor(text, 2); err == nil {
		return minor, nil
	}
	value, err := strconv.ParseFloat(text, 64)
	if err != nil || value < 0 || math.IsInf(value, 0) || math.IsNaN(value) {
		return 0, errors.New("invalid provider amount")
	}
	return int64(math.Round(value * 100)), nil
}

// lavaStripQuery drops a query string and fragment: Lava refuses a success or
// failure URL that carries one.
func lavaStripQuery(raw string) string {
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	parsed.RawQuery, parsed.Fragment, parsed.RawFragment = "", "", ""
	return parsed.String()
}

func lavaTruncate(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func normalizeLavaStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "success":
		return "succeeded"
	case "cancel", "cancelled":
		return "cancelled"
	case "expired":
		return "expired"
	case "error", "failed":
		return "failed"
	default:
		return "pending"
	}
}

var (
	_ Provider        = (*Lava)(nil)
	_ CurrencySupport = (*Lava)(nil)
	_ ConnectionProbe = (*Lava)(nil)
)
