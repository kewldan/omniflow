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
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/omniflow/omniflow/internal/commerce"
)

// ParityPay settles RUB invoices, paid by SBP or card, through the ParityPay
// v2 API.
//
// Requests authenticate with the `X-ShopId` and `X-SecretKey` headers (secret
// key №1). Notifications are signed with secret key №2: HMAC-SHA256 over the
// body's values concatenated in key order, in the `X-SIGNATURE` header.
type ParityPay struct {
	shopID         string
	secretKey      string
	callbackSecret string
	callbackURL    string
	service        string
	lifetime       int
	baseURL        string
	http           *http.Client
	clock          func() time.Time
}

const parityPayDefaultURL = "https://api.paritypay.net"

func buildParityPay(settings Settings) (Provider, error) {
	shopID := settings.String("APP_PARITYPAY_SHOP_ID")
	secretKey := settings.String("APP_PARITYPAY_SECRET_KEY")
	callbackSecret := settings.String("APP_PARITYPAY_CALLBACK_SECRET")
	if shopID == "" && secretKey == "" && callbackSecret == "" {
		return nil, ErrNotConfigured
	}
	if shopID == "" || secretKey == "" || callbackSecret == "" {
		return nil, errors.New("ParityPay needs APP_PARITYPAY_SHOP_ID, APP_PARITYPAY_SECRET_KEY, and APP_PARITYPAY_CALLBACK_SECRET together")
	}
	service := strings.ToLower(settings.String("APP_PARITYPAY_SERVICE"))
	if service != "" && service != "sbp" && service != "card" {
		return nil, fmt.Errorf("APP_PARITYPAY_SERVICE must be sbp, card, or empty, not %q", service)
	}
	baseURL := strings.TrimSuffix(settings.String("APP_PARITYPAY_BASE_URL"), "/")
	if baseURL == "" {
		baseURL = parityPayDefaultURL
	}
	return &ParityPay{
		shopID: shopID, secretKey: secretKey, callbackSecret: callbackSecret,
		callbackURL: settings.WebhookURL("paritypay"), service: service,
		lifetime: max(settings.Int("APP_PARITYPAY_INVOICE_LIFETIME_MINUTES", 60), 1),
		baseURL:  baseURL, http: tracedClient(30 * time.Second), clock: time.Now,
	}, nil
}

func (provider *ParityPay) Name() string { return "paritypay" }

// Capabilities declares signed webhooks and polling. ParityPay's API offers
// no refund call, and its subscriptions need separate approval from the
// provider and are not charged on a merchant's request.
func (provider *ParityPay) Capabilities() Capabilities {
	return Capabilities{Webhooks: true, Polling: true}
}

// SupportsCurrency reports RUB only: ParityPay invoices are priced in roubles.
func (provider *ParityPay) SupportsCurrency(currency string) bool {
	return strings.ToUpper(strings.TrimSpace(currency)) == "RUB"
}

type parityPayInvoice struct {
	ID      string          `json:"id"`
	OrderID string          `json:"order_id"`
	Status  string          `json:"status"`
	Amount  json.RawMessage `json:"amount"`
	Link    string          `json:"link"`
}

func (provider *ParityPay) Create(ctx context.Context, request CreateRequest) (Intent, error) {
	if !provider.SupportsCurrency(request.Amount.Currency) {
		return Intent{}, commerce.ErrCurrencyMismatch
	}
	body := map[string]any{
		"order_id": request.OrderID,
		"amount":   json.Number(formatMinor(request.Amount.Amount, 2)),
		"expire":   provider.lifetime,
	}
	if comment := []rune(request.Description); len(comment) > 0 {
		body["comment"] = string(comment[:min(len(comment), 255)])
	}
	if provider.service != "" {
		body["service"] = provider.service
	}
	if request.ReturnURL != "" {
		body["success_url"] = request.ReturnURL
		body["fail_url"] = request.ReturnURL
	}
	if provider.callbackURL != "" {
		body["callback_url"] = provider.callbackURL
	}
	var invoice parityPayInvoice
	err := provider.do(ctx, http.MethodPost, "/v2/invoice/create", nil, body, &invoice)
	var status *StatusError
	if errors.As(err, &status) && status.StatusCode == http.StatusUnprocessableEntity {
		// ParityPay refuses a repeated order_id with 422. When the invoice is
		// already there — a retry after a lost answer — it is reused rather
		// than reported as a failure; any other 422 stays a refusal.
		existing, found, lookupErr := provider.lookup(ctx, url.Values{"order_id": {request.OrderID}})
		if lookupErr != nil || !found {
			return Intent{}, err
		}
		invoice, err = existing, nil
	}
	if err != nil {
		return Intent{}, err
	}
	if invoice.ID == "" || invoice.Link == "" {
		return Intent{}, fmt.Errorf("%w: invoice has no id or payment link", ErrProviderResponse)
	}
	intent, err := provider.intent(invoice)
	if err != nil {
		return Intent{}, err
	}
	if intent.Amount.Currency == "" {
		intent.Amount = request.Amount
	}
	expires := provider.clock().UTC().Add(time.Duration(provider.lifetime) * time.Minute)
	intent.ExpiresAt = &expires
	return intent, nil
}

// Poll reads `GET /v2/invoice/status` by ParityPay's invoice ID.
func (provider *ParityPay) Poll(ctx context.Context, reference string) (Intent, error) {
	invoice, found, err := provider.lookup(ctx, url.Values{"id": {reference}})
	if err != nil {
		return Intent{}, err
	}
	if !found {
		return Intent{}, &StatusError{StatusCode: http.StatusNotFound}
	}
	if invoice.ID != "" && invoice.ID != reference {
		return Intent{}, fmt.Errorf("%w: invoice mismatch", ErrProviderResponse)
	}
	return provider.intent(invoice)
}

func (provider *ParityPay) Refund(context.Context, RefundRequest) (Refund, error) {
	return Refund{}, ErrUnsupported
}

// VerifyWebhook checks `X-SIGNATURE` before reading anything else.
//
// Subscription notifications share the URL. They carry no order of this
// installation's, so they are acknowledged as pending events and change
// nothing; refusing them would only make ParityPay retry.
func (provider *ParityPay) VerifyWebhook(headers http.Header, body []byte) (WebhookEvent, error) {
	fields, ok := parityPayFields(body)
	if !ok {
		return WebhookEvent{}, ErrProviderResponse
	}
	if !parityPaySignatureValid(provider.callbackSecret, fields, headers.Get("X-SIGNATURE")) {
		return WebhookEvent{}, ErrInvalidSignature
	}
	id, orderID := fields["id"].text, fields["order_id"].text
	status := strings.ToUpper(strings.TrimSpace(fields["status"].text))
	occurred := provider.clock().UTC()
	if fields["subscription_id"].text != "" || (orderID == "" && fields["shop_subscription_id"].text != "") {
		return WebhookEvent{ID: "subscription:" + id + ":" + status, Type: "subscription", Status: "pending", OccurredAt: occurred}, nil
	}
	if id == "" || orderID == "" || status == "" {
		return WebhookEvent{}, ErrProviderResponse
	}
	event := WebhookEvent{
		ID: strings.ToLower(status) + ":" + id, Type: status, ProviderReference: id, MerchantReference: orderID,
		Status: normalizeParityPayStatus(status), OccurredAt: occurred,
	}
	if amount := fields["amount"].text; amount != "" {
		minor, err := parseMinor(amount, 2)
		if err != nil {
			return WebhookEvent{}, err
		}
		event.Amount = commerce.Money{Amount: minor, Currency: "RUB"}
	} else if event.Status == "succeeded" {
		// A payment whose amount cannot be compared is not settled.
		return WebhookEvent{}, fmt.Errorf("%w: paid invoice without an amount", ErrProviderResponse)
	}
	return event, nil
}

func (provider *ParityPay) lookup(ctx context.Context, query url.Values) (parityPayInvoice, bool, error) {
	var invoice parityPayInvoice
	err := provider.do(ctx, http.MethodGet, "/v2/invoice/status", query, nil, &invoice)
	var status *StatusError
	if errors.As(err, &status) && status.StatusCode == http.StatusNotFound {
		return parityPayInvoice{}, false, nil
	}
	if err != nil {
		return parityPayInvoice{}, false, err
	}
	return invoice, true, nil
}

func (provider *ParityPay) do(ctx context.Context, method, path string, query url.Values, body, target any) error {
	headers := make(http.Header)
	headers.Set("X-ShopId", provider.shopID)
	headers.Set("X-SecretKey", provider.secretKey)
	endpoint := provider.baseURL + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	return doJSON(ctx, provider.http, method, endpoint, headers, body, target)
}

func (provider *ParityPay) intent(invoice parityPayInvoice) (Intent, error) {
	intent := Intent{ProviderReference: invoice.ID, Status: normalizeParityPayStatus(invoice.Status), CheckoutURL: invoice.Link}
	if text := strings.Trim(string(bytes.TrimSpace(invoice.Amount)), `"`); text != "" && text != "null" {
		minor, err := parseMinor(text, 2)
		if err != nil {
			return Intent{}, err
		}
		intent.Amount = commerce.Money{Amount: minor, Currency: "RUB"}
	}
	return intent, nil
}

// parityPayField is one top-level notification value: its text for signing,
// with numbers kept exactly as sent so 1200 is not rewritten as 1200.0.
type parityPayField struct {
	text string
}

func parityPayFields(body []byte) (map[string]parityPayField, bool) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil || raw == nil {
		return nil, false
	}
	fields := make(map[string]parityPayField, len(raw))
	for key, value := range raw {
		value = bytes.TrimSpace(value)
		switch {
		case len(value) == 0 || string(value) == "null":
			fields[key] = parityPayField{}
		case value[0] == '"':
			var text string
			if json.Unmarshal(value, &text) != nil {
				return nil, false
			}
			fields[key] = parityPayField{text: text}
		case value[0] == '{' || value[0] == '[':
			var compact bytes.Buffer
			if json.Compact(&compact, value) != nil {
				return nil, false
			}
			fields[key] = parityPayField{text: compact.String()}
		default:
			fields[key] = parityPayField{text: string(value)}
		}
	}
	return fields, true
}

func parityPaySignatureValid(secret string, fields map[string]parityPayField, signature string) bool {
	received := []byte(strings.ToLower(strings.TrimSpace(signature)))
	if secret == "" || len(received) == 0 {
		return false
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var payload strings.Builder
	for _, key := range keys {
		payload.WriteString(fields[key].text)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(payload.String()))
	return hmac.Equal([]byte(hex.EncodeToString(mac.Sum(nil))), received)
}

func normalizeParityPayStatus(status string) string {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "PAID":
		return "succeeded"
	case "EXPIRED":
		return "expired"
	case "ERROR":
		return "failed"
	case "REFUNDED":
		return "cancelled"
	default:
		return "pending"
	}
}

var (
	_ Provider        = (*ParityPay)(nil)
	_ CurrencySupport = (*ParityPay)(nil)
)
