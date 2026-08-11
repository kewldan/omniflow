package goods

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// maxProviderBody bounds what a provider response may cost in memory. A
// provider that answers with a stream is a provider that can exhaust the
// worker, whatever the response was supposed to contain.
const maxProviderBody = 1 << 20

// Fragment sells Telegram Premium and Telegram Stars.
//
// It is the only digital-goods implementation that ships in v0.7. Nothing about
// it is special-cased anywhere else in the application: it satisfies the
// Provider interface and is selected by the `provider_slug` on a product, so a
// second implementation needs no changes outside its own file.
//
// The base URL is configurable because Fragment's API is reached through a
// gateway an operator supplies credentials for, and because a sandbox has to be
// addressable for the integration suite.
type Fragment struct {
	token   string
	baseURL string
	http    *http.Client
}

// FragmentOptions configures the adapter.
type FragmentOptions struct {
	// Token is the API credential. It is read from the sealed
	// `goods_providers.credentials_ciphertext` column and never logged.
	Token string
	// BaseURL overrides the default endpoint.
	BaseURL string
	Timeout time.Duration
}

// NewFragment builds the adapter.
func NewFragment(options FragmentOptions) (*Fragment, error) {
	if strings.TrimSpace(options.Token) == "" {
		return nil, errors.New("Fragment API token is required")
	}
	base := strings.TrimSuffix(strings.TrimSpace(options.BaseURL), "/")
	if base == "" {
		base = "https://api.fragment-api.com/v1"
	}
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	return &Fragment{
		token:   options.Token,
		baseURL: base,
		// Traced, so a slow provider is visible in the span for the delivery job
		// that waited on it rather than only as an elapsed-time mystery.
		http: &http.Client{Timeout: timeout, Transport: otelhttp.NewTransport(http.DefaultTransport)},
	}, nil
}

// Name identifies the adapter and matches `goods_products.provider_slug`.
func (provider *Fragment) Name() string { return "fragment" }

// Supports reports the two product kinds Fragment sells.
func (provider *Fragment) Supports(kind string) bool {
	return kind == KindTelegramPremium || kind == KindTelegramStars
}

type fragmentQuoteResponse struct {
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
	ExpiresAt   string `json:"expires_at"`
	Error       string `json:"error"`
}

// Quote asks what a purchase would cost the operator.
//
// It reserves nothing. A quote that is acted on later is re-checked for
// freshness by the service, and a stale one produces a new quote rather than a
// charge at the old number.
func (provider *Fragment) Quote(ctx context.Context, request Request) (Quote, error) {
	if !provider.Supports(request.Kind) {
		return Quote{}, ErrUnsupported
	}
	payload := map[string]any{
		"kind":     request.Kind,
		"quantity": max(request.Quantity, 1),
		"currency": request.Currency,
	}
	if request.Kind == KindTelegramPremium {
		payload["months"] = request.DurationMonths
	} else {
		payload["stars"] = request.StarQuantity
	}

	var response fragmentQuoteResponse
	if err := provider.call(ctx, http.MethodPost, "/quote", nil, payload, &response); err != nil {
		return Quote{}, err
	}
	if response.AmountMinor <= 0 || response.Currency == "" {
		return Quote{}, fmt.Errorf("%w: quote is not usable", ErrUnsupported)
	}

	quote := Quote{CostMinor: response.AmountMinor, Currency: strings.ToUpper(response.Currency)}
	if response.ExpiresAt != "" {
		if parsed, err := time.Parse(time.RFC3339, response.ExpiresAt); err == nil {
			quote.ExpiresAt = parsed.UTC()
		}
	}
	return quote, nil
}

type fragmentDeliveryResponse struct {
	Reference string `json:"reference"`
	Status    string `json:"status"`
	ErrorCode string `json:"error_code"`
	Error     string `json:"error"`
}

// Deliver submits a purchase.
//
// The idempotency key travels as a header, so a retried attempt after a
// timeout returns the original submission rather than buying a second one. That
// is the provider-side half of the double-delivery guard; the database primary
// key on `goods_deliveries.order_id` is the half that does not depend on the
// provider honouring anything.
func (provider *Fragment) Deliver(ctx context.Context, request DeliveryRequest) (Delivery, error) {
	if !provider.Supports(request.Kind) {
		return Delivery{}, ErrUnsupported
	}
	recipient, err := NormalizeRecipient(request.Recipient)
	if err != nil {
		return Delivery{
			Status: "failed", FailureClass: FailureRecipientInvalid, ErrorCode: "invalid_username",
		}, nil
	}

	payload := map[string]any{
		"kind":      request.Kind,
		"quantity":  max(request.Quantity, 1),
		"recipient": recipient,
		"reference": request.OrderID,
	}
	if request.Kind == KindTelegramPremium {
		payload["months"] = request.DurationMonths
	} else {
		payload["stars"] = request.StarQuantity
	}

	headers := http.Header{}
	headers.Set("Idempotency-Key", request.IdempotencyKey)

	var response fragmentDeliveryResponse
	if err := provider.call(ctx, http.MethodPost, "/purchase", headers, payload, &response); err != nil {
		return classifyTransport(err), nil
	}
	return fragmentDelivery(response), nil
}

// Poll re-reads a submission that did not complete synchronously.
func (provider *Fragment) Poll(ctx context.Context, reference string) (Delivery, error) {
	if strings.TrimSpace(reference) == "" {
		return Delivery{}, ErrUnsupported
	}
	var response fragmentDeliveryResponse
	err := provider.call(ctx, http.MethodGet, "/purchase/"+reference, nil, nil, &response)
	if err != nil {
		return classifyTransport(err), nil
	}
	return fragmentDelivery(response), nil
}

// Balance reports the operator's remaining funds so the low-balance alert has
// something to fire on before a purchase fails for want of them.
func (provider *Fragment) Balance(ctx context.Context) (int64, string, error) {
	var response struct {
		AmountMinor int64  `json:"amount_minor"`
		Currency    string `json:"currency"`
	}
	if err := provider.call(ctx, http.MethodGet, "/balance", nil, nil, &response); err != nil {
		return 0, "", err
	}
	if response.Currency == "" {
		return 0, "", fmt.Errorf("%w: balance is not usable", ErrUnsupported)
	}
	return response.AmountMinor, strings.ToUpper(response.Currency), nil
}

// ValidateRecipient checks a username before the customer pays.
//
// Catching an unreachable handle here is what turns "your money is being
// refunded" into "check the username", which is the difference between a
// support ticket and a corrected typo.
func (provider *Fragment) ValidateRecipient(ctx context.Context, username string) error {
	recipient, err := NormalizeRecipient(username)
	if err != nil {
		return err
	}
	var response struct {
		Exists bool `json:"exists"`
	}
	if err := provider.call(
		ctx, http.MethodGet, "/recipient/"+recipient, nil, nil, &response,
	); err != nil {
		// A provider that cannot answer is not evidence the username is wrong.
		// The purchase proceeds and the delivery path classifies the outcome.
		return nil
	}
	if !response.Exists {
		return ErrInvalidRecipient
	}
	return nil
}

// fragmentDelivery maps a provider response onto the delivery vocabulary.
//
// Anything the provider does not name explicitly is treated as retryable, so an
// unrecognised status delays a delivery rather than refunding a purchase that
// may still be on its way.
func fragmentDelivery(response fragmentDeliveryResponse) Delivery {
	switch strings.ToLower(response.Status) {
	case "completed", "delivered", "success":
		return Delivery{Reference: response.Reference, Status: "delivered"}
	case "pending", "processing", "submitted", "queued":
		return Delivery{Reference: response.Reference, Status: "submitted"}
	case "failed", "rejected", "cancelled":
		return Delivery{
			Reference:    response.Reference,
			Status:       "failed",
			FailureClass: fragmentFailureClass(response.ErrorCode),
			ErrorCode:    fallbackCode(response.ErrorCode),
		}
	default:
		return Delivery{
			Reference:    response.Reference,
			Status:       "failed",
			FailureClass: FailureRetryable,
			ErrorCode:    "unknown_status",
		}
	}
}

// fragmentFailureClass maps the provider's own error codes onto the classes
// that decide retry and refund.
func fragmentFailureClass(code string) string {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "recipient_not_found", "invalid_username", "recipient_unreachable",
		"premium_already_active", "recipient_restricted":
		return FailureRecipientInvalid
	case "insufficient_funds", "balance_too_low":
		return FailureProviderBalance
	case "rate_limited", "temporarily_unavailable", "timeout":
		return FailureRetryable
	case "unsupported_product", "invalid_request", "order_rejected":
		return FailurePermanent
	default:
		// An unrecognised code is retried rather than refunded: retrying an
		// order that was permanently rejected wastes a few attempts, whereas
		// refunding one that would have succeeded loses the delivery.
		return FailureRetryable
	}
}

// classifyTransport turns a transport-level failure into a delivery outcome.
//
// A request that never reached the provider, or whose answer never came back,
// cannot be assumed to have done nothing. It is recorded as provider-
// unavailable and retried under the same idempotency key, which is why the key
// has to be honoured by the provider for this to be safe.
func classifyTransport(err error) Delivery {
	code := "provider_unreachable"
	var status statusError
	if errors.As(err, &status) {
		code = "http_" + strconv.Itoa(status.status)
		if status.status == http.StatusTooManyRequests {
			return Delivery{Status: "failed", FailureClass: FailureRetryable, ErrorCode: code}
		}
		if status.status >= 400 && status.status < 500 {
			// A client error will not resolve itself on retry.
			return Delivery{Status: "failed", FailureClass: FailurePermanent, ErrorCode: code}
		}
	}
	return Delivery{Status: "failed", FailureClass: FailureProviderUnavailable, ErrorCode: code}
}

func fallbackCode(code string) string {
	if strings.TrimSpace(code) == "" {
		return "provider_error"
	}
	return code
}

// statusError carries an HTTP status so the classifier can distinguish a
// provider that refused from one that was unreachable, without the adapter
// leaking a response body into an error string.
type statusError struct{ status int }

func (err statusError) Error() string {
	return "provider responded with HTTP " + strconv.Itoa(err.status)
}

func (provider *Fragment) call(
	ctx context.Context, method, path string, headers http.Header, payload any, out any,
) error {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("encode provider request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}

	request, err := http.NewRequestWithContext(ctx, method, provider.baseURL+path, body)
	if err != nil {
		return fmt.Errorf("create provider request: %w", err)
	}
	if headers != nil {
		request.Header = headers.Clone()
	}
	request.Header.Set("Authorization", "Bearer "+provider.token)
	request.Header.Set("Accept", "application/json")
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := provider.http.Do(request)
	if err != nil {
		return fmt.Errorf("call provider: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		// The body is drained and discarded rather than read into the error: it
		// can echo the recipient and the credential, and neither belongs in a
		// log line.
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxProviderBody))
		return statusError{status: response.StatusCode}
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxProviderBody)).Decode(out); err != nil {
		return fmt.Errorf("decode provider response: %w", err)
	}
	return nil
}
