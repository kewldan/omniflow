// Package nervixy tops up Steam wallets through Nervixy's API
// (https://nervixy.digital/docs-api).
//
// Nervixy sells from the operator's prepaid balance, in US dollars. One call,
// api_order, both opens the top-up and takes the money, and Nervixy documents
// no idempotency key. So the adapter checks everything it can before that
// call — the login exists, the balance is reachable — commits the settlement
// marker with its own partner identifier immediately before it, and treats a
// lost or garbled answer to it as ambiguous: the money may have left, and only
// an operator looking at Nervixy's order list can tell. An order Nervixy
// accepted is polled by its identifier until it is delivered or rejected.
package nervixy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/omniflow/omniflow/internal/goods"
)

// Slug is the provider's identifier in `goods_providers`.
const Slug = "nervixy"

// Currency is what Nervixy holds the balance in and what the adapter orders
// in.
const Currency = "USD"

// Order states Nervixy reports.
const (
	statusProcessing = "processing"
	statusDelivered  = "delivered"
	statusRejected   = "rejected"
)

// Config configures the adapter.
type Config struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
	Clock   func() time.Time
}

// Provider is the Nervixy adapter.
type Provider struct {
	baseURL string
	apiKey  string
	http    *http.Client
	clock   func() time.Time
	rates   goods.DollarRate
}

// New builds the adapter. `rates` converts the dollar cost into the shop
// currency.
func New(config Config, rates goods.DollarRate) (*Provider, error) {
	if strings.TrimSpace(config.APIKey) == "" {
		return nil, errors.New("APP_NERVIXY_API_KEY is not set")
	}
	base := strings.TrimSpace(config.BaseURL)
	if !strings.HasPrefix(base, "https://") && !strings.HasPrefix(base, "http://") {
		return nil, errors.New("APP_NERVIXY_BASE_URL is not an http(s) address")
	}
	client := config.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	clock := config.Clock
	if clock == nil {
		clock = time.Now
	}
	return &Provider{baseURL: base, apiKey: strings.TrimSpace(config.APIKey), http: client, clock: clock, rates: rates}, nil
}

// Name implements goods.Provider.
func (provider *Provider) Name() string { return Slug }

// Supports implements goods.Provider: Nervixy sells Steam top-ups here.
func (provider *Provider) Supports(kind string) bool { return kind == goods.KindSteamTopUp }

// Quote is the operator's cost of topping up `StarQuantity` dollars in the
// shop currency. Nervixy's account discount is left out, so the cost is never
// understated.
func (provider *Provider) Quote(ctx context.Context, request goods.Request) (goods.Quote, error) {
	if request.Kind != goods.KindSteamTopUp {
		return goods.Quote{}, goods.ErrUnsupported
	}
	if request.StarQuantity <= 0 {
		return goods.Quote{}, goods.ErrStarAmount
	}
	return goods.QuoteDollars(ctx, int64(request.StarQuantity)*100, request.Currency, provider.rates)
}

// ValidateRecipient asks Nervixy whether a Steam login can be topped up. It
// returns goods.ErrRecipientUnknown for a login Nervixy refuses, and any
// other error when it could not ask.
func (provider *Provider) ValidateRecipient(ctx context.Context, login string) error {
	var answer struct {
		Valid bool `json:"valid"`
	}
	if _, err := provider.call(ctx, http.MethodPost, "api_check_login",
		map[string]any{"steam_login": login}, &answer); err != nil {
		return err
	}
	if !answer.Valid {
		return goods.ErrRecipientUnknown
	}
	return nil
}

// Deliver checks the login, commits the marker, and places the order.
func (provider *Provider) Deliver(ctx context.Context, request goods.DeliveryRequest) (goods.Delivery, error) {
	if request.Kind != goods.KindSteamTopUp {
		return failed(goods.FailurePermanent, "unsellable_product"), nil
	}
	login, err := goods.NormalizeSteamLogin(request.Recipient)
	if err != nil {
		return failed(goods.FailureRecipientInvalid, "invalid_login"), nil
	}
	if request.StarQuantity <= 0 {
		return failed(goods.FailurePermanent, "invalid_amount"), nil
	}

	// Everything before the marker spends nothing and is safe to retry.
	if err := provider.ValidateRecipient(ctx, login); errors.Is(err, goods.ErrRecipientUnknown) {
		return failed(goods.FailureRecipientInvalid, "login_rejected"), nil
	} else if err != nil {
		return failed(goods.FailureProviderUnavailable, "check_unavailable"), nil
	}

	partnerID := partnerID(request.OrderID, provider.clock())
	if request.BeforeSettle != nil {
		if markErr := request.BeforeSettle(ctx, goods.SettlementMarker{
			ProviderRequestID: partnerID, Asset: Currency, CostNano: int64(request.StarQuantity) * 100,
		}); markErr != nil {
			return failed(goods.FailureRetryable, "marker_not_committed"), nil
		}
	}

	var placed struct {
		OrderID string `json:"order_id"`
	}
	status, err := provider.call(ctx, http.MethodPost, "api_order", map[string]any{
		"amount": request.StarQuantity, "steam_login": login, "currency": Currency, "partner_id": partnerID,
	}, &placed)
	if err != nil {
		return orderRefusal(status, err), nil
	}
	if placed.OrderID == "" {
		// Accepted, but with nothing to poll: the money has probably left.
		return failed(goods.FailureAmbiguous, "order_without_id"), nil
	}
	return goods.Delivery{Reference: placed.OrderID, Status: "submitted"}, nil
}

// orderRefusal classifies a failed api_order. Only a definite client-side
// refusal proves nothing was charged; a lost answer or a server error may
// have come after the debit.
func orderRefusal(status int, err error) goods.Delivery {
	var rejected *rejection
	if !errors.As(err, &rejected) || status == 0 || status >= 500 {
		// No refusal was read: the answer was lost, unreadable, or a server
		// error that may have come after the debit.
		return failed(goods.FailureAmbiguous, errorCode("order", status))
	}
	message := strings.ToLower(rejected.message)
	insufficient := rejected.insufficient
	notCharged := func(class string) goods.Delivery {
		delivery := failed(class, "order_http_"+strconv.Itoa(status))
		delivery.NotCharged = true
		return delivery
	}
	switch {
	case insufficient || strings.Contains(message, "balance") || strings.Contains(message, "средств") ||
		strings.Contains(message, "баланс"):
		return notCharged(goods.FailureProviderBalance)
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return notCharged(goods.FailureProviderUnavailable)
	case status == http.StatusTooManyRequests:
		return notCharged(goods.FailureRetryable)
	case strings.Contains(message, "login") || strings.Contains(message, "логин") || strings.Contains(message, "steam"):
		return notCharged(goods.FailureRecipientInvalid)
	default:
		return notCharged(goods.FailurePermanent)
	}
}

// Poll reads an order Nervixy accepted.
func (provider *Provider) Poll(ctx context.Context, reference string) (goods.Delivery, error) {
	var current struct {
		Status string `json:"status"`
	}
	status, err := provider.call(ctx, http.MethodPost, "api_status", map[string]any{"order_id": reference}, &current)
	if err != nil {
		// Unknown is not a failure: the order exists, and the next attempt
		// asks again. One still unresolved after every attempt is parked.
		return goods.Delivery{Reference: reference, Status: "submitted", ErrorCode: errorCode("status", status)}, nil
	}
	switch current.Status {
	case statusDelivered:
		return goods.Delivery{Reference: reference, Status: "delivered"}, nil
	case statusRejected:
		// The top-up did not reach Steam. Whether Nervixy returned the money
		// to the operator's balance is between the operator and Nervixy; the
		// customer did not get what they paid for.
		return goods.Delivery{Reference: reference, Status: "failed",
			FailureClass: goods.FailurePermanent, ErrorCode: "provider_rejected"}, nil
	default:
		return goods.Delivery{Reference: reference, Status: "submitted"}, nil
	}
}

// Balance is the operator's Nervixy balance in US cents.
func (provider *Provider) Balance(ctx context.Context) (int64, string, error) {
	var me struct {
		Balance json.Number `json:"balance"`
	}
	if _, err := provider.call(ctx, http.MethodPost, "api_me", map[string]any{}, &me); err != nil {
		return 0, "", err
	}
	value, err := me.Balance.Float64()
	if err != nil {
		return 0, "", errors.New("nervixy returned an unreadable balance")
	}
	return int64(math.Round(value * 100)), Currency, nil
}

// partnerID is the adapter's own identifier for one order: the Omniflow
// order without its dashes, then the moment it was placed, in the Latin
// letters and digits Nervixy accepts. An operator searches Nervixy's order
// list by it when a delivery is parked.
func partnerID(orderID string, now time.Time) string {
	compact := strings.ReplaceAll(orderID, "-", "")
	if len(compact) > 20 {
		compact = compact[:20]
	}
	return compact + strconv.FormatInt(now.UnixMilli(), 36)
}

func failed(class, code string) goods.Delivery {
	return goods.Delivery{Status: "failed", FailureClass: class, ErrorCode: code}
}

func errorCode(step string, status int) string {
	if status == 0 {
		return step + "_unreachable"
	}
	return step + "_http_" + strconv.Itoa(status)
}

type rejection struct {
	status       int
	message      string
	insufficient bool
}

func (err *rejection) Error() string {
	return "nervixy refused the request with HTTP " + strconv.Itoa(err.status)
}

// call performs one request. Nervixy routes by the `action` query parameter
// and answers `{"ok": true, …}` with the fields at the top level. The status
// is zero when no HTTP answer arrived. The error message is kept for
// classification only and never logged: it may repeat the customer's login.
func (provider *Provider) call(ctx context.Context, method, action string, body any, out any) (int, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		reader = bytes.NewReader(encoded)
	}
	separator := "?"
	if strings.Contains(provider.baseURL, "?") {
		separator = "&"
	}
	request, err := http.NewRequestWithContext(ctx, method, provider.baseURL+separator+"action="+action, reader)
	if err != nil {
		return 0, err
	}
	request.Header.Set("X-API-Key", provider.apiKey)
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := provider.http.Do(request)
	if err != nil {
		return 0, err
	}
	defer func() { _ = response.Body.Close() }()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return 0, err
	}
	var envelope struct {
		OK       bool            `json:"ok"`
		Error    string          `json:"error"`
		Required json.RawMessage `json:"required"`
	}
	decodeErr := json.Unmarshal(payload, &envelope)
	if response.StatusCode < 200 || response.StatusCode > 299 || decodeErr != nil || !envelope.OK {
		status := response.StatusCode
		if status >= 200 && status <= 299 {
			if decodeErr != nil {
				// A success code around something unreadable is not a
				// refusal anybody can rely on.
				return status, errors.New("nervixy returned an unreadable answer")
			}
			status = http.StatusBadRequest
		}
		return status, &rejection{
			status: status, message: envelope.Error, insufficient: len(envelope.Required) > 0,
		}
	}
	if out != nil {
		decoder := json.NewDecoder(bytes.NewReader(payload))
		decoder.UseNumber()
		if err := decoder.Decode(out); err != nil {
			return response.StatusCode, errors.New("nervixy returned an unreadable answer")
		}
	}
	return response.StatusCode, nil
}
