// Package fragmentdirect sells Telegram Stars by buying them on fragment.com
// with the operator's own Fragment session and paying from the operator's own
// TON wallet.
//
// It is the alternative to the HTTP gateway in internal/goods: where the
// gateway owns the session, the wallet, and the settlement, this adapter does
// all three inside Omniflow, the way the operator's Bedolaga fork does in
// production. The secrets it needs — the Fragment cookies, the wallet's
// mnemonic, and the TON API key — come only from the environment. They are
// never stored, never returned by an API, and never logged.
//
// Every failure is classified by one question: could money have left the
// wallet? Before the payment is broadcast, the answer is no and the delivery
// may be retried. From the moment the caller's settlement marker is committed,
// the answer is "maybe", and the delivery parks for a person.
package fragmentdirect

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/omniflow/omniflow/internal/goods"
	"github.com/omniflow/omniflow/internal/tonrate"
	"github.com/omniflow/omniflow/internal/tonwallet"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// Slug is the provider's identifier in goods_providers and goods_products.
const Slug = "fragment_direct"

// dryRunPrefix marks a request identifier and transaction hash that no real
// purchase produced, exactly as the fork marks them.
const dryRunPrefix = "dry-run"

// Config is the operator's environment.
type Config struct {
	// Cookies are the Fragment session cookies, `k=v; …` or JSON.
	Cookies string
	// Mnemonic is the paying wallet's 24-word seed phrase.
	Mnemonic string
	// WalletVersion is V5R1 (W5) or V4R2.
	WalletVersion string
	// APIProvider is toncenter or tonapi, and APIKey its key.
	APIProvider string
	APIKey      string
	// APIRPS bounds TON API requests per second in this process.
	APIRPS float64
	// ProxyURL routes Fragment requests through a proxy, when set.
	ProxyURL string
	// ShowSender asks Fragment to show the operator's account as the sender.
	ShowSender bool
	// DryRun walks every delivery through its states without contacting
	// Fragment or the wallet.
	DryRun bool
	// Currency is the shop currency the cost is converted into.
	Currency string

	// FragmentURL and APIURL override the endpoints, for tests.
	FragmentURL string
	APIURL      string
	// Pay overrides the confirmation timing, for tests.
	Pay tonwallet.PayOptions
}

// Configured reports whether every secret a real purchase needs is present.
// It does not validate them; New does.
func (config Config) Configured() bool {
	return strings.TrimSpace(config.Cookies) != "" &&
		strings.TrimSpace(config.Mnemonic) != "" &&
		strings.TrimSpace(config.APIKey) != ""
}

// Provider is the goods adapter.
type Provider struct {
	config   Config
	fragment *fragmentClient
	wallet   *tonwallet.Wallet
	api      tonwallet.API
	rates    *tonrate.Service
	logger   *slog.Logger
	clock    func() time.Time
}

var (
	_ goods.Provider           = (*Provider)(nil)
	_ goods.RecipientValidator = (*Provider)(nil)
	_ goods.WalletReporter     = (*Provider)(nil)
)

// New builds the adapter. In dry-run mode the secrets are optional; otherwise
// each one is validated here, so a misconfiguration surfaces when the provider
// is resolved rather than halfway through a purchase. An error never quotes a
// secret.
func New(config Config, rates *tonrate.Service, logger *slog.Logger) (*Provider, error) {
	if logger == nil {
		logger = slog.Default()
	}
	provider := &Provider{config: config, rates: rates, logger: logger, clock: time.Now}
	if config.DryRun && !config.Configured() {
		return provider, nil
	}
	if !config.Configured() {
		return nil, fmt.Errorf("%w: cookies, wallet mnemonic, and TON API key are all required", ErrConfiguration)
	}

	cookies, err := ParseCookies(config.Cookies)
	if err != nil {
		return nil, err
	}
	wallet, err := tonwallet.FromMnemonic(config.Mnemonic, config.WalletVersion)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrConfiguration, err)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if strings.TrimSpace(config.ProxyURL) != "" {
		proxy, parseErr := url.Parse(strings.TrimSpace(config.ProxyURL))
		if parseErr != nil || proxy.Host == "" {
			return nil, fmt.Errorf("%w: proxy URL is not usable", ErrConfiguration)
		}
		transport.Proxy = http.ProxyURL(proxy)
	}
	fragmentHTTP := &http.Client{Timeout: 30 * time.Second, Transport: otelhttp.NewTransport(transport)}
	api, err := tonwallet.NewAPI(tonwallet.APIOptions{
		Provider: config.APIProvider, Key: config.APIKey, RPS: config.APIRPS, BaseURL: config.APIURL,
		HTTP: &http.Client{Timeout: 30 * time.Second, Transport: otelhttp.NewTransport(http.DefaultTransport)},
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrConfiguration, err)
	}
	provider.fragment = newFragmentClient(cookies, fragmentHTTP, config.FragmentURL)
	provider.wallet = wallet
	provider.api = api
	return provider, nil
}

// Name matches goods_products.provider_slug.
func (provider *Provider) Name() string { return Slug }

// DryRun reports whether deliveries are simulated.
func (provider *Provider) DryRun() bool { return provider.config.DryRun }

// Supports reports Stars only: the fork's direct path never bought Premium,
// and a Premium purchase is a different Fragment flow.
func (provider *Provider) Supports(kind string) bool { return kind == goods.KindTelegramStars }

// Quote prices a purchase at Fragment's current TON price and today's rate.
// A product with a fixed per-Star price never calls it.
func (provider *Provider) Quote(ctx context.Context, request goods.Request) (goods.Quote, error) {
	if request.Kind != goods.KindTelegramStars {
		return goods.Quote{}, goods.ErrUnsupported
	}
	if provider.fragment == nil {
		return goods.Quote{}, goods.ErrCostUnavailable
	}
	stars := max(request.StarQuantity, goods.FragmentMinStars)
	nano, err := provider.StarsPriceNano(ctx, stars)
	if err != nil {
		return goods.Quote{}, err
	}
	if provider.rates == nil {
		return goods.Quote{}, goods.ErrCostUnavailable
	}
	rate, err := provider.rates.Current(ctx)
	if err != nil {
		return goods.Quote{}, goods.ErrCostUnavailable
	}
	// Per Star, rounded up, then multiplied, so a markup applied per unit
	// never undershoots what Fragment charges.
	perStar := (rate.MinorFromNano(nano) + int64(stars) - 1) / int64(stars)
	return goods.Quote{CostMinor: perStar * int64(stars), Currency: rate.Currency}, nil
}

// StarsPriceNano is Fragment's price for `stars` Stars in nanoton.
func (provider *Provider) StarsPriceNano(ctx context.Context, stars int) (int64, error) {
	if provider.fragment == nil {
		return 0, goods.ErrUnsupported
	}
	if stars < goods.FragmentMinStars || stars > goods.FragmentMaxStars {
		return 0, goods.ErrStarAmount
	}
	price, err := provider.fragment.starsPriceTON(ctx, stars)
	if err != nil {
		return 0, err
	}
	return parseTON(price)
}

// ValidateRecipient asks Fragment whether a username can receive Stars before
// anybody pays, which is the check the fork runs at the start of a purchase.
func (provider *Provider) ValidateRecipient(ctx context.Context, username string) error {
	if provider.fragment == nil {
		return nil
	}
	_, err := provider.fragment.searchRecipient(ctx, username)
	if errors.Is(err, ErrRecipientNotFound) {
		return goods.ErrInvalidRecipient
	}
	return err
}

// Poll is not supported: a Fragment purchase is resolved synchronously, and
// one whose answer was lost is ambiguous rather than pollable.
func (provider *Provider) Poll(context.Context, string) (goods.Delivery, error) {
	return goods.Delivery{}, goods.ErrUnsupported
}

// Balance is the wallet balance in nanoton.
func (provider *Provider) Balance(ctx context.Context) (int64, string, error) {
	info, err := provider.Wallet(ctx)
	if err != nil {
		return 0, "", err
	}
	return info.BalanceNano, "TON", nil
}

// Wallet reads the paying wallet's address and balance.
func (provider *Provider) Wallet(ctx context.Context) (goods.WalletInfo, error) {
	if provider.wallet == nil {
		return goods.WalletInfo{}, goods.ErrUnsupported
	}
	account, err := provider.api.Account(ctx, provider.wallet.Address())
	if err != nil {
		return goods.WalletInfo{}, err
	}
	return goods.WalletInfo{
		Address: provider.wallet.Address(), Version: provider.wallet.Version(),
		Asset: "TON", BalanceNano: account.BalanceNano,
	}, nil
}

// Deliver buys the Stars.
//
// The order of operations is the double-spend guard:
//
//  1. Fragment resolves the recipient, opens a request, and prepares the
//     payment. Nothing is spent.
//  2. The wallet is read and the payment is signed. Nothing is spent.
//  3. BeforeSettle commits the caller's marker. If it fails, nothing is sent.
//  4. The signed message is broadcast. From here any failure is ambiguous.
//  5. Fragment is told which message paid (confirmReq). Its failure is logged
//     and recorded, but the purchase is delivered: the money has left.
func (provider *Provider) Deliver(ctx context.Context, request goods.DeliveryRequest) (goods.Delivery, error) {
	if request.Kind != goods.KindTelegramStars {
		return failed(goods.FailurePermanent, "unsellable_product"), nil
	}
	username, err := goods.NormalizeRecipient(request.Recipient)
	if err != nil {
		return failed(goods.FailureRecipientInvalid, "invalid_username"), nil
	}
	stars := request.StarQuantity
	if stars < goods.FragmentMinStars || stars > goods.FragmentMaxStars {
		return failed(goods.FailurePermanent, "quantity_out_of_range"), nil
	}
	if provider.config.DryRun {
		return provider.dryRun(ctx, request)
	}
	if provider.fragment == nil {
		return failed(goods.FailureProviderUnavailable, "not_configured"), nil
	}

	found, err := provider.fragment.searchRecipient(ctx, username)
	if err != nil {
		return classifyBeforePayment(err), nil
	}
	account, err := provider.wallet.Account()
	if err != nil {
		return failed(goods.FailureProviderUnavailable, "wallet_account"), nil
	}
	prepared, err := provider.fragment.preparePayment(ctx, found.ID, stars, account, provider.config.ShowSender)
	if err != nil {
		return classifyBeforePayment(err), nil
	}
	transfers := make([]tonwallet.Transfer, 0, len(prepared.Messages))
	for _, message := range prepared.Messages {
		amount, parseErr := strconv.ParseInt(message.Amount.String(), 10, 64)
		if parseErr != nil || amount <= 0 {
			return failed(goods.FailureRetryable, "payment_shape"), nil
		}
		transfers = append(transfers, tonwallet.Transfer{
			Destination: message.Address, AmountNano: amount, PayloadBOC: message.Payload,
		})
	}

	signed, err := tonwallet.Prepare(ctx, provider.api, provider.wallet, transfers)
	if err != nil {
		var low tonwallet.InsufficientBalanceError
		if errors.As(err, &low) {
			return failed(goods.FailureProviderBalance, "wallet_balance_low"), nil
		}
		return failed(goods.FailureRetryable, "wallet_unavailable"), nil
	}

	if request.BeforeSettle != nil {
		if err := request.BeforeSettle(ctx, goods.SettlementMarker{
			ProviderRequestID: prepared.RequestID, Asset: "TON", CostNano: signed.TotalNano,
		}); err != nil {
			// The marker did not commit, so the payment is not sent.
			return failed(goods.FailureRetryable, "settlement_marker"), nil
		}
	}

	result, err := tonwallet.Broadcast(ctx, provider.api, provider.wallet, signed, provider.config.Pay)
	if err != nil {
		provider.logger.Error("fragment payment outcome unknown",
			"provider", Slug, "orderId", request.OrderID, "fragmentRequest", prepared.RequestID,
			"txHash", signed.Hash, "error", err)
		return goods.Delivery{
			Status: "failed", FailureClass: goods.FailureAmbiguous, ErrorCode: "broadcast_uncertain",
			Reference: prepared.RequestID,
			Settlement: &goods.Settlement{
				ProviderRequestID: prepared.RequestID, Asset: "TON",
				CostNano: signed.TotalNano, TxHash: signed.Hash,
			},
		}, nil
	}

	confirmed := true
	if err := provider.fragment.confirm(context.WithoutCancel(ctx), prepared.RequestID, result.BOC); err != nil {
		// The Stars are paid for. A confirmReq that did not land is not a
		// reason to pay again; it is a note for the operator.
		confirmed = false
		provider.logger.Warn("fragment confirmReq failed after payment",
			"provider", Slug, "orderId", request.OrderID, "fragmentRequest", prepared.RequestID)
	}
	balance := result.BalanceAfterNano
	return goods.Delivery{
		Status: "delivered", Reference: prepared.RequestID,
		Settlement: &goods.Settlement{
			ProviderRequestID: prepared.RequestID, Asset: "TON", CostNano: signed.TotalNano,
			TxHash: result.Hash, ProviderConfirmed: confirmed, BalanceAfterNano: &balance,
		},
	}, nil
}

// dryRun walks a delivery through the marker and completion without money or
// Fragment, so a staging installation exercises every state the real path
// does.
func (provider *Provider) dryRun(ctx context.Context, request goods.DeliveryRequest) (goods.Delivery, error) {
	requestID := dryRunPrefix + "-" + request.OrderID
	if request.BeforeSettle != nil {
		if err := request.BeforeSettle(ctx, goods.SettlementMarker{
			ProviderRequestID: requestID, Asset: "TON",
		}); err != nil {
			return failed(goods.FailureRetryable, "settlement_marker"), nil
		}
	}
	return goods.Delivery{
		Status: "delivered", Reference: requestID,
		Settlement: &goods.Settlement{
			ProviderRequestID: requestID, Asset: "TON",
			TxHash: dryRunPrefix + "-tx-" + request.OrderID, ProviderConfirmed: true, DryRun: true,
		},
	}, nil
}

// IsDryRunHash reports a transaction hash a dry run produced, which has no
// explorer page to link to.
func IsDryRunHash(hash string) bool { return strings.HasPrefix(hash, dryRunPrefix) }

// classifyBeforePayment maps a failure that happened before any payment.
func classifyBeforePayment(err error) goods.Delivery {
	switch {
	case errors.Is(err, ErrRecipientNotFound):
		return failed(goods.FailureRecipientInvalid, "recipient_not_found")
	case errors.Is(err, ErrConfiguration):
		// A session, KYC, or key problem the operator fixes. It retries on
		// the schedule and refunds only once attempts run out — the fork's
		// retry-or-refund for a configuration error.
		return failed(goods.FailureProviderUnavailable, "fragment_configuration")
	default:
		return failed(goods.FailureRetryable, "fragment_unavailable")
	}
}

func failed(class, code string) goods.Delivery {
	return goods.Delivery{Status: "failed", FailureClass: class, ErrorCode: code}
}

// parseTON converts a decimal TON string into nanoton without floating point.
func parseTON(value string) (int64, error) {
	value = strings.TrimSpace(value)
	whole, fraction, _ := strings.Cut(value, ".")
	if len(fraction) > 9 {
		fraction = fraction[:9]
	}
	fraction += strings.Repeat("0", 9-len(fraction))
	parsed, ok := new(big.Int).SetString(whole+fraction, 10)
	if !ok || parsed.Sign() <= 0 || !parsed.IsInt64() {
		return 0, fmt.Errorf("%w: fragment price is not a number", ErrBeforePayment)
	}
	return parsed.Int64(), nil
}
