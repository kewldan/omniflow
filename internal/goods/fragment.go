package goods

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
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

// Fragment sells Telegram Premium and Telegram Stars through an
// operator-operated gateway.
//
// It does not talk to fragment.com. Fragment has no API: buying there means
// driving an authenticated browser session through a stateful multi-step flow
// and settling the result with an on-chain TON transfer from a wallet derived
// from a mnemonic. Omniflow deliberately does not hold that wallet — a
// self-hosted product should not ship a config field that can spend real money
// — so it calls a gateway the operator runs, and the gateway owns the Fragment
// session, the TON wallet, and the on-chain settlement.
//
// Two properties of that gateway shape everything below, and neither is
// something this adapter can fix on its own.
//
// **It honours no idempotency key.** Submitting a purchase creates and
// processes it immediately. A request whose answer never arrives is genuinely
// ambiguous: the goods may already have been sent and the operator's funds
// already spent. Such an outcome is classified `ambiguous`, which is neither
// retried nor refunded — it waits for a person.
//
// **A success is not proof of delivery.** The gateway answers 200 for a
// purchase its own processor abandoned: insufficient TON, an expired Fragment
// session, and a rejected purchase are all reported to the operator's chat and
// then returned from normally. Until the gateway distinguishes those, an
// accepted submission means "the gateway took it", and this adapter records it
// as delivered because there is no other signal to wait for. The limitation is
// documented on the operator page rather than hidden here.
type Fragment struct {
	baseURL  string
	token    string
	currency string
	http     *http.Client
}

// FragmentOptions configures the adapter.
type FragmentOptions struct {
	// BaseURL is where the operator's gateway is reachable, without a trailing
	// slash. The admin routes are mounted under /admin.
	BaseURL string
	// Token authenticates as the fixed `omniflow` HTTP Basic user.
	//
	// It is read from the sealed `goods_providers.credentials_ciphertext`
	// column and never logged.
	Token string
	// Currency the gateway prices in. Its star value is published as a plain
	// number with no currency attached, so the operator states which one.
	Currency string
	Timeout  time.Duration
}

// NewFragment builds the adapter.
func NewFragment(options FragmentOptions) (*Fragment, error) {
	base := strings.TrimSuffix(strings.TrimSpace(options.BaseURL), "/")
	if base == "" {
		return nil, errors.New("digital-goods gateway URL is required")
	}
	if !strings.HasPrefix(base, "https://") && !strings.HasPrefix(base, "http://") {
		return nil, errors.New("digital-goods gateway URL must be absolute")
	}
	if strings.TrimSpace(options.Token) == "" {
		return nil, errors.New("digital-goods gateway token is required")
	}
	currency := strings.ToUpper(strings.TrimSpace(options.Currency))
	if currency == "" {
		currency = "RUB"
	}
	timeout := options.Timeout
	if timeout <= 0 {
		// Generous, because the gateway settles on-chain before it answers.
		// Cutting it short would turn ordinary slowness into an ambiguous
		// outcome that needs a human.
		timeout = 90 * time.Second
	}
	return &Fragment{
		baseURL:  base,
		token:    options.Token,
		currency: currency,
		// Traced, so a slow gateway is visible in the span for the delivery job
		// that waited on it rather than only as an elapsed-time mystery.
		http: &http.Client{Timeout: timeout, Transport: otelhttp.NewTransport(http.DefaultTransport)},
	}, nil
}

// Name identifies the adapter and matches `goods_products.provider_slug`.
func (provider *Fragment) Name() string { return "fragment" }

// Supports reports the two product kinds the gateway sells.
func (provider *Fragment) Supports(kind string) bool {
	return kind == KindTelegramPremium || kind == KindTelegramStars
}

// MinStarQuantity is the smallest Stars purchase the gateway accepts, and is
// Fragment's own floor rather than the gateway's.
const MinStarQuantity = 50

// PremiumDurations are the only Premium terms the gateway sells.
var PremiumDurations = []int{3, 6, 12}

// ErrCostUnavailable reports a product whose cost the gateway does not publish.
//
// It is distinct from ErrUnsupported: the product is sellable, but Omniflow
// cannot learn what it costs, so the operator sets the price directly and the
// margin on the resulting order is unknown rather than zero.
var ErrCostUnavailable = errors.New("digital goods provider publishes no cost for this product")

// Quote asks what a purchase costs the operator.
//
// Only Stars have a published value. Premium prices live in the gateway's own
// table and are not exposed, so the operator configures a fixed sale price in
// the panel and this returns ErrCostUnavailable.
func (provider *Fragment) Quote(ctx context.Context, request Request) (Quote, error) {
	switch request.Kind {
	case KindTelegramStars:
		var response struct {
			OK bool `json:"ok"`
			// Published as a plain number in the gateway's own currency, in
			// major units — 1.18 means one rouble eighteen kopeks per star.
			Value float64 `json:"value"`
		}
		if err := provider.call(ctx, http.MethodGet, "/admin/stars/value", nil, &response); err != nil {
			return Quote{}, err
		}
		if !response.OK || response.Value <= 0 {
			return Quote{}, fmt.Errorf("%w: star value is not usable", ErrUnsupported)
		}

		quantity := max(request.StarQuantity, 1)
		// Rounded to the minor unit per star and then multiplied, which is what
		// the gateway itself charges against; multiplying first and rounding
		// once would drift from its own arithmetic on large orders.
		perStar := int64(math.Round(response.Value * 100))
		return Quote{CostMinor: perStar * int64(quantity), Currency: provider.currency}, nil

	case KindTelegramPremium:
		return Quote{}, ErrCostUnavailable

	default:
		return Quote{}, ErrUnsupported
	}
}

// Deliver submits a purchase.
//
// The gateway takes a recipient username and a quantity, and delivers to that
// username. `user_id` is bookkeeping on the gateway's side — it finds or
// creates a customer record there — so the buyer's Telegram identifier is what
// travels, never the recipient's, which Omniflow does not know for an arbitrary
// handle.
func (provider *Fragment) Deliver(ctx context.Context, request DeliveryRequest) (Delivery, error) {
	recipient, err := NormalizeRecipient(request.Recipient)
	if err != nil {
		return Delivery{
			Status: "failed", FailureClass: FailureRecipientInvalid, ErrorCode: "invalid_username",
		}, nil
	}

	path, quantity, err := provider.route(request.Request)
	if err != nil {
		// A product configured outside what the gateway sells will never be
		// deliverable, so the customer is refunded rather than made to wait
		// through a retry schedule that cannot succeed.
		return Delivery{
			Status: "failed", FailureClass: FailurePermanent, ErrorCode: "unsellable_product",
		}, nil
	}

	payload := map[string]any{
		"user_id":  request.BuyerTelegramID,
		"username": recipient,
		"quantity": quantity,
	}

	var response struct {
		OK bool `json:"ok"`
	}
	if err := provider.call(ctx, http.MethodPost, path, payload, &response); err != nil {
		return classifyTransport(err), nil
	}
	if !response.OK {
		return Delivery{Status: "failed", FailureClass: FailurePermanent, ErrorCode: "gateway_rejected"}, nil
	}

	// Recorded as delivered because the gateway offers nothing further to wait
	// on: it exposes no reference to poll and no delivery callback. See the type
	// comment for why that is weaker than it reads.
	return Delivery{Status: "delivered"}, nil
}

// route maps a request onto the gateway endpoint and quantity for its kind, and
// refuses anything the gateway will not accept.
func (provider *Fragment) route(request Request) (string, int, error) {
	switch request.Kind {
	case KindTelegramStars:
		if request.StarQuantity < MinStarQuantity {
			return "", 0, ErrUnsupported
		}
		return "/admin/stars/send", request.StarQuantity, nil
	case KindTelegramPremium:
		for _, months := range PremiumDurations {
			if request.DurationMonths == months {
				return "/admin/premium/send", months, nil
			}
		}
		return "", 0, ErrUnsupported
	default:
		return "", 0, ErrUnsupported
	}
}

// Poll is not supported: the gateway returns no reference to poll against.
//
// This is why a lost answer is ambiguous rather than merely unfinished — there
// is no way to ask afterwards what happened.
func (provider *Fragment) Poll(context.Context, string) (Delivery, error) {
	return Delivery{}, ErrUnsupported
}

// Balance is not supported: the gateway does not publish its TON wallet balance.
//
// The low-balance alert therefore has nothing to fire on for this provider, and
// an exhausted wallet surfaces as deliveries that the gateway accepts and
// silently abandons. Exposing a balance endpoint is the single change that
// would fix that.
func (provider *Fragment) Balance(context.Context) (int64, string, error) {
	return 0, "", ErrUnsupported
}

// classifyTransport turns a transport-level failure into a delivery outcome.
//
// The mapping is deliberately cautious about server errors. The gateway's own
// processor transfers TON and only afterwards writes its records, so a 5xx can
// be raised from code that runs *after* the goods were paid for. Retrying that
// could buy a second time, and refunding it could give money back for goods the
// recipient received. Neither is safe, so it stops for a person.
func classifyTransport(err error) Delivery {
	var status statusError
	if errors.As(err, &status) {
		code := "http_" + strconv.Itoa(status.status)
		switch {
		case status.status == http.StatusTooManyRequests:
			return Delivery{Status: "failed", FailureClass: FailureRetryable, ErrorCode: code}
		case status.status == http.StatusUnauthorized, status.status == http.StatusForbidden:
			// A rejected credential is an operator configuration problem, and a
			// corrected token should let the delivery through rather than
			// having refunded the customer in the meantime.
			return Delivery{Status: "failed", FailureClass: FailureProviderUnavailable, ErrorCode: code}
		case status.status >= 500:
			return Delivery{Status: "failed", FailureClass: FailureAmbiguous, ErrorCode: code}
		default:
			return Delivery{Status: "failed", FailureClass: FailurePermanent, ErrorCode: code}
		}
	}
	// No status at all: the request may never have arrived, or its answer may
	// have been lost after the gateway acted on it. Indistinguishable from here.
	return Delivery{Status: "failed", FailureClass: FailureAmbiguous, ErrorCode: "gateway_unreachable"}
}

// statusError carries an HTTP status so the classifier can distinguish a
// gateway that refused from one that was unreachable, without the adapter
// leaking a response body into an error string.
type statusError struct{ status int }

func (err statusError) Error() string {
	return "digital-goods gateway responded with HTTP " + strconv.Itoa(err.status)
}

func (provider *Fragment) call(ctx context.Context, method, path string, payload, out any) error {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("encode gateway request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}

	request, err := http.NewRequestWithContext(ctx, method, provider.baseURL+path, body)
	if err != nil {
		return fmt.Errorf("create gateway request: %w", err)
	}
	// The gateway authenticates the fixed `omniflow` user with a shared token.
	request.SetBasicAuth("omniflow", provider.token)
	request.Header.Set("Accept", "application/json")
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := provider.http.Do(request)
	if err != nil {
		return fmt.Errorf("call gateway: %w", err)
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
		return fmt.Errorf("decode gateway response: %w", err)
	}
	return nil
}
