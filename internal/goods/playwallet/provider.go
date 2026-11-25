// Package playwallet tops up Steam wallets through PlayWallet's merchant API
// (https://playwallet.bot/ru/api).
//
// PlayWallet sells from the operator's prepaid balance, in US dollars. A
// purchase is two calls: create-order opens an order that waits for payment,
// and pay-order — signed with a token derived from the order — spends the
// balance and queues the top-up. The order is then polled until PlayWallet
// reports it completed or failed; the API has no callbacks.
//
// The split is what makes the adapter safe to retry. Only pay-order moves
// money, and it pays one specific order: paying the same order twice cannot
// top an account up twice. So the adapter commits the PlayWallet order
// identifier as the delivery's settlement marker immediately before paying,
// and from then on a retry never opens a new order — it asks what became of
// that one, and pays it again only while PlayWallet still says it is unpaid.
// No outcome is therefore genuinely ambiguous, and nothing here parks a
// delivery for review on its own.
package playwallet

import (
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/omniflow/omniflow/internal/goods"
)

// Slug is the provider's identifier in `goods_providers`.
const Slug = "playwallet"

// Currency is what PlayWallet prices and charges in.
const Currency = "USD"

// Order states PlayWallet reports.
const (
	statusActive    = "active"
	statusQueued    = "queued"
	statusCompleted = "completed"
	statusError     = "error"
)

// balanceTTL is how long the fee and the service limits are reused. They
// change when the operator's monthly turnover crosses a tier, not between two
// screens of one purchase.
const balanceTTL = 5 * time.Minute

var (
	// ErrAmountOutOfRange reports a top-up outside the service's limits.
	ErrAmountOutOfRange = errors.New("steam top-up amount is outside the provider's limits")
	// ErrServiceUnknown reports a merchant whose Steam service cannot be
	// identified: several services, none named after Steam, and none
	// configured.
	ErrServiceUnknown = errors.New("playwallet steam service is not identified; set APP_PLAYWALLET_SERVICE_ID")
	// ErrRateUnavailable reports a shop currency the cost cannot be converted
	// into.
	ErrRateUnavailable = goods.ErrRateUnavailable
)

// RateFunc returns how many minor units of `currency` one US dollar costs.
type RateFunc = goods.DollarRate

// Config configures the adapter.
type Config struct {
	BaseURL   string
	APIKey    string
	ServiceID string
	HTTP      *http.Client
	Clock     func() time.Time
}

// Provider is the PlayWallet adapter.
type Provider struct {
	baseURL   string
	apiKey    string
	serviceID string
	http      *http.Client
	clock     func() time.Time
	rates     RateFunc

	mu       sync.Mutex
	snapshot balance
	readAt   time.Time
}

// New builds the adapter. `rates` converts the dollar cost into the shop
// currency; without it only a shop priced in dollars can be quoted.
func New(config Config, rates RateFunc) (*Provider, error) {
	if strings.TrimSpace(config.APIKey) == "" {
		return nil, errors.New("APP_PLAYWALLET_API_KEY is not set")
	}
	base := strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	if !strings.HasPrefix(base, "https://") && !strings.HasPrefix(base, "http://") {
		return nil, errors.New("APP_PLAYWALLET_BASE_URL is not an http(s) address")
	}
	client := config.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	clock := config.Clock
	if clock == nil {
		clock = time.Now
	}
	return &Provider{
		baseURL: base, apiKey: strings.TrimSpace(config.APIKey),
		serviceID: strings.TrimSpace(config.ServiceID), http: client, clock: clock, rates: rates,
	}, nil
}

// Name implements goods.Provider.
func (provider *Provider) Name() string { return Slug }

// Supports implements goods.Provider: PlayWallet sells Steam top-ups only.
func (provider *Provider) Supports(kind string) bool { return kind == goods.KindSteamTopUp }

// Quote is the operator's cost of topping up `StarQuantity` dollars, the
// provider's fee included, in the shop currency. It never opens an order.
func (provider *Provider) Quote(ctx context.Context, request goods.Request) (goods.Quote, error) {
	if request.Kind != goods.KindSteamTopUp {
		return goods.Quote{}, goods.ErrUnsupported
	}
	state, err := provider.balance(ctx)
	if err != nil {
		return goods.Quote{}, err
	}
	service, err := provider.service(state)
	if err != nil {
		return goods.Quote{}, err
	}
	cents := int64(request.StarQuantity) * 100
	if !service.allows(cents) {
		return goods.Quote{}, ErrAmountOutOfRange
	}
	return goods.QuoteDollars(ctx, withFee(cents, state.FeeRatio), request.Currency, provider.rates)
}

// Deliver opens a PlayWallet order and pays it.
//
// Everything before the settlement marker is safe to retry from scratch: an
// order that was opened and never paid simply expires on PlayWallet's side.
// Everything after it is resolved by reading that same order back.
func (provider *Provider) Deliver(ctx context.Context, request goods.DeliveryRequest) (goods.Delivery, error) {
	if request.Kind != goods.KindSteamTopUp {
		return failed(goods.FailurePermanent, "unsellable_product"), nil
	}
	login, err := goods.NormalizeSteamLogin(request.Recipient)
	if err != nil {
		return failed(goods.FailureRecipientInvalid, "invalid_login"), nil
	}
	units := request.StarQuantity
	if units <= 0 {
		return failed(goods.FailurePermanent, "invalid_amount"), nil
	}
	state, err := provider.balance(ctx)
	if err != nil {
		return failed(goods.FailureProviderUnavailable, "balance_unavailable"), nil
	}
	service, err := provider.service(state)
	if err != nil {
		return failed(goods.FailureProviderUnavailable, "service_unknown"), nil
	}
	cents := int64(units) * 100
	if !service.allows(cents) {
		return failed(goods.FailurePermanent, "amount_out_of_range"), nil
	}

	// A fresh external identifier per opening: a create-order whose answer
	// was lost may have left an unpaid order behind under the previous one,
	// and PlayWallet does not say whether it refuses a repeated identifier.
	// The order identifier stays its prefix, so PlayWallet's list still reads
	// back to the Omniflow order.
	externalID := request.OrderID + "-" + strconv.FormatInt(provider.clock().UnixMilli(), 36)
	var created order
	status, err := provider.call(ctx, http.MethodPost, "/create-order/", map[string]string{
		"externalId": externalID,
		"serviceId":  service.ID,
		"amount":     formatCents(cents),
		"login":      login,
	}, &created)
	if err != nil {
		return failed(classifyRejection(status, err, true), errorCode("create", status)), nil
	}
	if created.ID == "" {
		return failed(goods.FailureRetryable, "create_without_id"), nil
	}

	if request.BeforeSettle != nil {
		if markErr := request.BeforeSettle(ctx, goods.SettlementMarker{
			ProviderRequestID: created.ID, Asset: Currency,
			CostNano: withFee(cents, state.FeeRatio),
		}); markErr != nil {
			// Nothing has been paid. The opened order expires unpaid.
			return failed(goods.FailureRetryable, "marker_not_committed"), nil
		}
	}
	return provider.pay(ctx, created, externalID), nil
}

// Poll resolves an order that was marked for payment: it reports what
// PlayWallet says, and pays the order again while PlayWallet still says it is
// waiting for payment.
func (provider *Provider) Poll(ctx context.Context, reference string) (goods.Delivery, error) {
	current, err := provider.order(ctx, reference)
	if err != nil {
		return submitted(reference, "", "status_unavailable"), nil
	}
	if current.Status == statusActive {
		return provider.pay(ctx, current, current.ExternalID), nil
	}
	return outcome(current), nil
}

// Balance is the merchant's remaining PlayWallet balance in US cents.
func (provider *Provider) Balance(ctx context.Context) (int64, string, error) {
	provider.mu.Lock()
	provider.readAt = time.Time{}
	provider.mu.Unlock()
	state, err := provider.balance(ctx)
	if err != nil {
		return 0, "", err
	}
	return state.BalanceCents, Currency, nil
}

// pay spends the balance on one order and reports where it stands.
//
// Whatever pay-order answers, the order is read back. A lost answer is then
// just a read; a refusal is believed only while the order is still unpaid.
func (provider *Provider) pay(ctx context.Context, target order, externalID string) goods.Delivery {
	status, payErr := provider.call(ctx, http.MethodPost, "/pay-order/", map[string]string{
		"id": target.ID, "externalId": externalID, "token": Token(target.ID, target.CreatedDateTime),
	}, nil)
	current, err := provider.order(ctx, target.ID)
	if err != nil {
		// Neither answer is known. The order may be paid; it is not
		// abandoned, and the next poll asks again.
		return submitted(target.ID, "", errorCode("pay_unread", status))
	}
	if current.Status != statusActive {
		return outcome(current)
	}
	if payErr == nil {
		// Accepted and not yet visible: asked again on the next poll.
		return submitted(target.ID, "", "pay_pending")
	}
	if status == 0 || status >= 500 || status == http.StatusTooManyRequests {
		return submitted(target.ID, goods.FailureProviderUnavailable, errorCode("pay", status))
	}
	// PlayWallet refused to pay an order it still holds unpaid. Only this
	// adapter can pay it, and it will not try again once the delivery ends,
	// so a refusal that is not about the balance ends it with a refund.
	if class := classifyRejection(status, payErr, false); class == goods.FailureProviderBalance ||
		class == goods.FailureProviderUnavailable {
		return submitted(target.ID, class, errorCode("pay", status))
	}
	return goods.Delivery{
		Reference: target.ID, Status: "failed",
		FailureClass: goods.FailurePermanent, ErrorCode: errorCode("pay", status),
	}
}

// outcome maps a PlayWallet order onto a delivery.
func outcome(current order) goods.Delivery {
	switch current.Status {
	case statusCompleted:
		return goods.Delivery{Reference: current.ID, Status: "delivered"}
	case statusError:
		// PlayWallet's own definition: the order failed, was not paid, and
		// nothing was taken from the balance.
		return goods.Delivery{
			Reference: current.ID, Status: "failed",
			FailureClass: goods.FailurePermanent, ErrorCode: "provider_order_error",
		}
	default:
		// Queued, or a state this adapter does not know yet: still running.
		return submitted(current.ID, "", "")
	}
}

func submitted(reference, class, code string) goods.Delivery {
	return goods.Delivery{Reference: reference, Status: "submitted", FailureClass: class, ErrorCode: code}
}

func failed(class, code string) goods.Delivery {
	return goods.Delivery{Status: "failed", FailureClass: class, ErrorCode: code}
}

// classifyRejection sorts a refused call.
//
// PlayWallet does not document its error shape, so the message is read for the
// two cases whose handling differs — a login Steam does not know, which is the
// customer's to fix, and an empty balance, which is the operator's — and
// everything else falls back on the HTTP status. The message itself is never
// stored or logged: it may repeat the customer's login.
func classifyRejection(status int, err error, beforePayment bool) string {
	var rejected *rejection
	message := ""
	if errors.As(err, &rejected) {
		message = strings.ToLower(rejected.message)
	}
	switch {
	case containsAny(message, "balance", "баланс", "insufficient", "недостаточно", "funds"):
		return goods.FailureProviderBalance
	case beforePayment && containsAny(message, "login", "логин", "account", "аккаунт", "user not found", "не найден"):
		return goods.FailureRecipientInvalid
	case status == 0 || status >= 500 || status == http.StatusTooManyRequests:
		return goods.FailureProviderUnavailable
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		// A rotated key or an address missing from the merchant's allow-list:
		// the operator's to fix, so it is retried rather than refunded.
		return goods.FailureProviderUnavailable
	default:
		return goods.FailurePermanent
	}
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

func errorCode(step string, status int) string {
	if status == 0 {
		return step + "_unreachable"
	}
	return step + "_http_" + strconv.Itoa(status)
}

// Token is the pay-order signature: the SHA-512 of the order identifier
// followed by its creation time exactly as PlayWallet wrote it, in lowercase
// hex.
func Token(orderID, createdDateTime string) string {
	sum := sha512.Sum512([]byte(orderID + createdDateTime))
	return hex.EncodeToString(sum[:])
}

// ---------------------------------------------------------------------------
// Wire format
// ---------------------------------------------------------------------------

type envelope struct {
	Status  string          `json:"status"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

type order struct {
	ID              string `json:"id"`
	Status          string `json:"status"`
	ExternalID      string `json:"externalId"`
	ServiceID       string `json:"serviceId"`
	Amount          string `json:"amount"`
	CreatedDateTime string `json:"createdDateTime"`
}

type service struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	MinAmount string `json:"minAmount"`
	MaxAmount string `json:"maxAmount"`
}

// allows reports whether a top-up of `cents` is inside the service's limits.
// A limit PlayWallet did not state is no limit.
func (item service) allows(cents int64) bool {
	if low, ok := parseCents(item.MinAmount); ok && cents < low {
		return false
	}
	if high, ok := parseCents(item.MaxAmount); ok && high > 0 && cents > high {
		return false
	}
	return cents > 0
}

type balance struct {
	BalanceCents int64
	FeeRatio     *big.Rat
	Services     []service
}

type rejection struct {
	status  int
	message string
}

func (err *rejection) Error() string {
	return "playwallet refused the request with HTTP " + strconv.Itoa(err.status)
}

// balance reads get-balance, reusing a recent answer.
func (provider *Provider) balance(ctx context.Context) (balance, error) {
	provider.mu.Lock()
	if !provider.readAt.IsZero() && provider.clock().Sub(provider.readAt) < balanceTTL {
		cached := provider.snapshot
		provider.mu.Unlock()
		return cached, nil
	}
	provider.mu.Unlock()

	var raw struct {
		Balance  string    `json:"balance"`
		FeeRatio string    `json:"feeRatio"`
		Services []service `json:"services"`
	}
	if _, err := provider.call(ctx, http.MethodGet, "/get-balance", nil, &raw); err != nil {
		return balance{}, err
	}
	fee, ok := new(big.Rat).SetString(strings.TrimSpace(raw.FeeRatio))
	if !ok || fee.Sign() < 0 {
		return balance{}, errors.New("playwallet returned an unreadable fee ratio")
	}
	cents, _ := parseCents(raw.Balance)
	state := balance{BalanceCents: cents, FeeRatio: fee, Services: raw.Services}
	provider.mu.Lock()
	provider.snapshot, provider.readAt = state, provider.clock()
	provider.mu.Unlock()
	return state, nil
}

// service picks the Steam service: the configured one, the only one, or the
// one named after Steam.
func (provider *Provider) service(state balance) (service, error) {
	if provider.serviceID != "" {
		for _, item := range state.Services {
			if item.ID == provider.serviceID {
				return item, nil
			}
		}
		// Configured but not listed: trust the operator and apply no limits
		// rather than refusing every sale.
		return service{ID: provider.serviceID}, nil
	}
	if len(state.Services) == 1 {
		return state.Services[0], nil
	}
	for _, item := range state.Services {
		if strings.Contains(strings.ToLower(item.Name), "steam") {
			return item, nil
		}
	}
	return service{}, ErrServiceUnknown
}

// order reads one order.
func (provider *Provider) order(ctx context.Context, id string) (order, error) {
	if strings.TrimSpace(id) == "" || strings.ContainsAny(id, "/?#") {
		return order{}, errors.New("malformed playwallet order identifier")
	}
	var current order
	if _, err := provider.call(ctx, http.MethodGet, "/get-order/"+id, nil, &current); err != nil {
		return order{}, err
	}
	if current.ID == "" {
		current.ID = id
	}
	return current, nil
}

// call performs one request and decodes the envelope's data into `out`. The
// returned status is zero when no HTTP answer arrived.
func (provider *Provider) call(ctx context.Context, method, path string, body any, out any) (int, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, provider.baseURL+path, reader)
	if err != nil {
		return 0, err
	}
	request.Header.Set("pw-api-key", provider.apiKey)
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
	var decoded envelope
	decodeErr := json.Unmarshal(payload, &decoded)
	if response.StatusCode < 200 || response.StatusCode > 299 ||
		decodeErr != nil || !strings.EqualFold(decoded.Status, "success") {
		status := response.StatusCode
		if status >= 200 && status <= 299 {
			// A success code around a refusal is still a refusal; it is
			// reported as a client error so it is not mistaken for an outage.
			status = http.StatusBadRequest
		}
		return status, &rejection{status: status, message: decoded.Message}
	}
	if out != nil && len(decoded.Data) > 0 {
		if err := json.Unmarshal(decoded.Data, out); err != nil {
			return response.StatusCode, fmt.Errorf("playwallet returned an unreadable answer: %w", err)
		}
	}
	return response.StatusCode, nil
}

// withFee adds PlayWallet's fee to an amount in cents, rounding up.
func withFee(cents int64, fee *big.Rat) int64 {
	if fee == nil || fee.Sign() <= 0 {
		return cents
	}
	total := new(big.Rat).Mul(new(big.Rat).SetInt64(cents), new(big.Rat).Add(big.NewRat(1, 1), fee))
	quotient, remainder := new(big.Int).QuoRem(total.Num(), total.Denom(), new(big.Int))
	if remainder.Sign() > 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	return quotient.Int64()
}

// parseCents reads a decimal dollar string such as "150.00".
func parseCents(value string) (int64, bool) {
	parsed, ok := new(big.Rat).SetString(strings.TrimSpace(value))
	if !ok {
		return 0, false
	}
	parsed.Mul(parsed, big.NewRat(100, 1))
	quotient := new(big.Int).Quo(parsed.Num(), parsed.Denom())
	return quotient.Int64(), true
}

func formatCents(cents int64) string {
	return fmt.Sprintf("%d.%02d", cents/100, cents%100)
}
