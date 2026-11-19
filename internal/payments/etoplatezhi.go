package payments

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/omniflow/omniflow/internal/commerce"
)

// EtoPlatezhi is the adapter for the EtoPlatezhi payment page
// (paymentpage.etoplatezhi.ru).
//
// There is no API call at creation: the checkout is a payment-page URL whose
// parameters are signed locally. The signature flattens the parameters into
// `key:value` entries (nested keys joined with `:`, booleans as 1/0, empty
// lists dropped, `signature` and `frame_mode` ignored), sorts them, joins
// them with `;`, and takes a base64 HMAC-SHA512 keyed with the project
// secret. Callbacks carry a signature computed the same way over their body.
//
// EtoPlatezhi's `payment.id` is the merchant's own payment ID, so the order ID
// is both the provider reference and the merchant reference. Bedolaga has no
// status call for this provider, so the adapter does not poll.
type EtoPlatezhi struct {
	projectID   int64
	secret      string
	currency    string
	forceMethod string
	callbackURL string
	baseURL     string
	clock       func() time.Time
}

const etoplatezhiDefaultBaseURL = "https://paymentpage.etoplatezhi.ru/payment"

func buildEtoPlatezhi(settings Settings) (Provider, error) {
	rawProject := settings.String("APP_ETOPLATEZHI_PROJECT_ID")
	secret := settings.String("APP_ETOPLATEZHI_SECRET_KEY")
	if rawProject == "" && secret == "" {
		return nil, ErrNotConfigured
	}
	if rawProject == "" || secret == "" {
		return nil, errors.New("EtoPlatezhi needs both APP_ETOPLATEZHI_PROJECT_ID and APP_ETOPLATEZHI_SECRET_KEY")
	}
	projectID, err := strconv.ParseInt(rawProject, 10, 64)
	if err != nil || projectID <= 0 {
		return nil, errors.New("APP_ETOPLATEZHI_PROJECT_ID must be a positive integer")
	}
	currency := strings.ToUpper(settings.String("APP_ETOPLATEZHI_CURRENCY"))
	if currency == "" {
		currency = "RUB"
	}
	if _, err := currencyExponent(currency); err != nil {
		return nil, fmt.Errorf("APP_ETOPLATEZHI_CURRENCY: %w", err)
	}
	forceMethod := strings.ToLower(settings.String("APP_ETOPLATEZHI_FORCE_PAYMENT_METHOD"))
	switch forceMethod {
	case "", "sbp", "card":
	default:
		return nil, fmt.Errorf("APP_ETOPLATEZHI_FORCE_PAYMENT_METHOD must be sbp or card, got %q", forceMethod)
	}
	return &EtoPlatezhi{
		projectID: projectID, secret: secret, currency: currency, forceMethod: forceMethod,
		callbackURL: settings.WebhookURL("etoplatezhi"), baseURL: etoplatezhiDefaultBaseURL, clock: time.Now,
	}, nil
}

func (provider *EtoPlatezhi) Name() string { return "etoplatezhi" }

// Capabilities declares webhooks only: the integration has no API client, so
// there is nothing to poll, refund or charge through.
func (provider *EtoPlatezhi) Capabilities() Capabilities {
	return Capabilities{Webhooks: true}
}

// SupportsCurrency reports the single currency the project is configured for.
func (provider *EtoPlatezhi) SupportsCurrency(currency string) bool {
	return strings.EqualFold(strings.TrimSpace(currency), provider.currency)
}

// Create signs a payment-page URL. Nothing is sent to EtoPlatezhi until the
// customer opens it, so the intent starts pending with the order ID as its
// reference.
func (provider *EtoPlatezhi) Create(_ context.Context, request CreateRequest) (Intent, error) {
	if !provider.SupportsCurrency(request.Amount.Currency) || request.Amount.Amount <= 0 {
		return Intent{}, fmt.Errorf("%w: EtoPlatezhi is configured for %s only", ErrUnsupported, provider.currency)
	}
	customer := strings.TrimSpace(request.Metadata["customer_id"])
	if customer == "" {
		customer = request.OrderID
	}
	params := map[string]any{
		"project_id":       json.Number(strconv.FormatInt(provider.projectID, 10)),
		"payment_id":       request.OrderID,
		"payment_amount":   json.Number(strconv.FormatInt(request.Amount.Amount, 10)),
		"payment_currency": provider.currency,
		"customer_id":      customer,
	}
	optional := map[string]string{
		"payment_description":   request.Description,
		"merchant_callback_url": provider.callbackURL,
		"redirect_success_url":  request.ReturnURL,
		"redirect_fail_url":     request.ReturnURL,
		"force_payment_method":  provider.forceMethod,
		"customer_email":        strings.TrimSpace(request.Metadata["customer_email"]),
	}
	for key, value := range optional {
		if value != "" {
			params[key] = value
		}
	}
	signature, err := provider.sign(params)
	if err != nil {
		return Intent{}, err
	}
	query := url.Values{}
	for key, value := range params {
		rendered, _, _ := etoplatezhiScalar(value)
		query.Set(key, rendered)
	}
	query.Set("signature", signature)
	return Intent{
		ProviderReference: request.OrderID,
		Status:            "pending",
		CheckoutURL:       provider.baseURL + "?" + query.Encode(),
		Amount:            request.Amount,
	}, nil
}

func (provider *EtoPlatezhi) Poll(context.Context, string) (Intent, error) {
	return Intent{}, ErrUnsupported
}

func (provider *EtoPlatezhi) Refund(context.Context, RefundRequest) (Refund, error) {
	return Refund{}, ErrUnsupported
}

// VerifyWebhook recomputes the body signature with every `signature` key
// removed and compares it in constant time. The body looks like
// {project_id, payment: {id, status, sum: {amount, currency}}, customer, …}
// where `sum.amount` is in minor units.
func (provider *EtoPlatezhi) VerifyWebhook(_ http.Header, body []byte) (WebhookEvent, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil {
		return WebhookEvent{}, ErrInvalidSignature
	}
	received, _ := payload["signature"].(string)
	if received == "" {
		return WebhookEvent{}, ErrInvalidSignature
	}
	expected, err := provider.sign(payload)
	if err != nil || !hmac.Equal([]byte(expected), []byte(received)) {
		return WebhookEvent{}, ErrInvalidSignature
	}
	if project, ok := payload["project_id"]; ok {
		rendered, _, _ := etoplatezhiScalar(project)
		if rendered != strconv.FormatInt(provider.projectID, 10) {
			return WebhookEvent{}, fmt.Errorf("%w: EtoPlatezhi callback is for another project", ErrProviderResponse)
		}
	}
	payment, _ := payload["payment"].(map[string]any)
	reference, _, _ := etoplatezhiScalar(payment["id"])
	status, _ := payment["status"].(string)
	if reference == "" || status == "" {
		return WebhookEvent{}, fmt.Errorf("%w: EtoPlatezhi callback has no payment ID or status", ErrProviderResponse)
	}
	event := WebhookEvent{
		ID:                status + ":" + reference,
		Type:              status,
		ProviderReference: reference,
		MerchantReference: reference,
		Status:            etoplatezhiStatus(status),
		OccurredAt:        provider.clock().UTC(),
	}
	sum, _ := payment["sum"].(map[string]any)
	if amount, err := etoplatezhiAmount(sum); err == nil {
		event.Amount = amount
	} else if event.Status == "succeeded" {
		return WebhookEvent{}, fmt.Errorf("%w: EtoPlatezhi success without a readable amount", ErrProviderResponse)
	}
	return event, nil
}

func (provider *EtoPlatezhi) sign(params map[string]any) (string, error) {
	entries, err := etoplatezhiFlatten(params, "")
	if err != nil {
		return "", err
	}
	sort.Strings(entries)
	mac := hmac.New(sha512.New, []byte(provider.secret))
	_, _ = mac.Write([]byte(strings.Join(entries, ";")))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil)), nil
}

func etoplatezhiIgnored(key, fullKey string) bool {
	return key == "signature" || key == "frame_mode" || fullKey == "signature" || fullKey == "frame_mode"
}

func etoplatezhiFlatten(params map[string]any, prefix string) ([]string, error) {
	var entries []string
	for key, value := range params {
		fullKey := key
		if prefix != "" {
			fullKey = prefix + ":" + key
		}
		if etoplatezhiIgnored(key, fullKey) {
			continue
		}
		switch typed := value.(type) {
		case map[string]any:
			nested, err := etoplatezhiFlatten(typed, fullKey)
			if err != nil {
				return nil, err
			}
			entries = append(entries, nested...)
		case []any:
			for index, item := range typed {
				itemKey := fullKey + ":" + strconv.Itoa(index)
				if object, ok := item.(map[string]any); ok {
					nested, err := etoplatezhiFlatten(object, itemKey)
					if err != nil {
						return nil, err
					}
					entries = append(entries, nested...)
					continue
				}
				rendered, present, err := etoplatezhiScalar(item)
				if err != nil {
					return nil, err
				}
				if present {
					entries = append(entries, itemKey+":"+rendered)
				}
			}
		default:
			rendered, present, err := etoplatezhiScalar(value)
			if err != nil {
				return nil, err
			}
			if present {
				entries = append(entries, fullKey+":"+rendered)
			}
		}
	}
	return entries, nil
}

// etoplatezhiScalar renders a leaf the way Bedolaga's Python does: booleans
// as 1/0, integers as written, fractional numbers in Python's float repr, and
// strings as they are — including the empty string. Only null is absent.
func etoplatezhiScalar(value any) (string, bool, error) {
	switch typed := value.(type) {
	case nil:
		return "", false, nil
	case string:
		return typed, true, nil
	case bool:
		if typed {
			return "1", true, nil
		}
		return "0", true, nil
	case json.Number:
		literal := typed.String()
		if !strings.ContainsAny(literal, ".eE") {
			return literal, true, nil
		}
		number, err := typed.Float64()
		if err != nil {
			return "", false, err
		}
		return etoplatezhiPythonFloat(number), true, nil
	default:
		return "", false, errors.New("unsupported EtoPlatezhi signature value")
	}
}

func etoplatezhiPythonFloat(number float64) string {
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

func etoplatezhiAmount(sum map[string]any) (commerce.Money, error) {
	number, ok := sum["amount"].(json.Number)
	currency, _ := sum["currency"].(string)
	if !ok {
		return commerce.Money{}, errors.New("missing EtoPlatezhi amount")
	}
	amount, err := strconv.ParseInt(number.String(), 10, 64)
	if err != nil || amount <= 0 {
		return commerce.Money{}, errors.New("invalid EtoPlatezhi amount")
	}
	currency = strings.ToUpper(currency)
	if _, err := currencyExponent(currency); err != nil {
		return commerce.Money{}, err
	}
	return commerce.Money{Amount: amount, Currency: currency}, nil
}

// etoplatezhiStatus normalises EtoPlatezhi's payment statuses. The awaiting
// states and unknown values are pending; a full refund or reversal is a
// reversal of a settled payment, a partial one names no amount and is ignored.
func etoplatezhiStatus(status string) string {
	switch status {
	case "success":
		return "succeeded"
	case "decline", "error":
		return "failed"
	case "refunded", "reversed":
		return StatusRefunded
	case "cancelled", "partially refunded":
		// A partial refund names no amount Omniflow could reverse.
		return "cancelled"
	default:
		return "pending"
	}
}

var _ Provider = (*EtoPlatezhi)(nil)
