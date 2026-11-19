// Package goods is the provider-neutral contract for selling digital goods
// that are not VPN access.
//
// A digital good is bought, delivered, and refunded entirely inside its own
// tables plus the shared order, payment, and ledger pipeline. Nothing here
// creates, modifies, reads, or depends on a Remnawave entitlement, and a
// delivery failure in this package can never disturb a subscription.
//
// The package holds three things a provider adapter must not decide for itself:
// what the customer is charged, whether a recipient is addressable, and what a
// failure means. Keeping those out of the adapter is what lets a second
// provider be added without re-litigating pricing or retry policy.
package goods

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"
)

// Product kinds match the database check constraint on `goods_products.kind`.
const (
	KindTelegramPremium = "telegram_premium"
	KindTelegramStars   = "telegram_stars"
)

// Rounding modes match the check constraint on `goods_pricing.rounding`.
//
// Every mode rounds up. A shop that rounds a quote down sells below the cost it
// was quoted whenever the provider's rate moves against it, which is a slow
// leak rather than a visible failure.
const (
	RoundNone         = "none"
	RoundMinor        = "up_minor"
	RoundUnit         = "up_unit"
	RoundTenUnits     = "up_ten_units"
	RoundHundredUnits = "up_hundred_units"
)

// Failure classes match the check constraint on
// `goods_deliveries.failure_class`. The class, not the message, decides what
// happens next.
const (
	// FailureRetryable is a transient fault: retry on the backoff schedule.
	FailureRetryable = "retryable"
	// FailurePermanent cannot succeed however many times it is tried, so the
	// order is refunded to the customer's wallet.
	FailurePermanent = "permanent"
	// FailureRecipientInvalid is the customer's to fix. It is refunded rather
	// than retried, because nothing the worker does will make the username
	// resolve.
	FailureRecipientInvalid = "recipient_invalid"
	// FailureProviderBalance means the operator's funding source is exhausted.
	// It is retryable — the operator can top it up — and it raises an alert.
	FailureProviderBalance = "provider_balance"
	// FailureProviderUnavailable is an outage: retry, and stop quoting.
	FailureProviderUnavailable = "provider_unavailable"
	// FailureAmbiguous is an outcome nobody can safely act on automatically:
	// the purchase may or may not have completed.
	//
	// It exists because a provider that honours no idempotency key turns a lost
	// answer into a genuine unknown. Retrying could deliver — and charge the
	// operator — twice; refunding could give money back for goods the recipient
	// received. It is therefore neither retried nor refunded, and the delivery
	// stops and waits for a person.
	FailureAmbiguous = "ambiguous"
)

var (
	// ErrInvalidRecipient reports a username that cannot be a Telegram handle.
	ErrInvalidRecipient = errors.New("recipient username is not valid")
	// ErrQuoteExpired reports a quote that is no longer honourable.
	ErrQuoteExpired = errors.New("price quote has expired")
	// ErrUnsupported reports a capability an adapter does not implement.
	ErrUnsupported = errors.New("digital goods provider capability is not supported")
	// ErrSpendLimit reports a delivery refused by the operator's spend ceiling.
	ErrSpendLimit = errors.New("digital goods spend limit reached")
)

// usernamePattern is Telegram's own rule: five to thirty-two characters of
// letters, digits, and underscores, beginning with a letter. It is stricter
// than the column constraint on purpose — the column has to accept anything
// already stored, and this is the gate new input passes through.
var usernamePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{4,31}$`)

// NormalizeRecipient turns what a customer typed into a bare username.
//
// People paste profile links, copy handles with the leading @, and add stray
// whitespace. Accepting all three and refusing everything else means the
// confirmation step shows the customer exactly the handle the provider will be
// given, which is the point of having a confirmation step at all.
func NormalizeRecipient(input string) (string, error) {
	value := strings.TrimSpace(input)
	value = strings.TrimPrefix(value, "https://")
	value = strings.TrimPrefix(value, "http://")
	value = strings.TrimPrefix(value, "www.")
	value = strings.TrimPrefix(value, "t.me/")
	value = strings.TrimPrefix(value, "telegram.me/")
	value = strings.TrimPrefix(value, "@")
	// A pasted link may carry a query string or a trailing slash.
	if index := strings.IndexAny(value, "/?#"); index >= 0 {
		value = value[:index]
	}
	if !usernamePattern.MatchString(value) {
		return "", ErrInvalidRecipient
	}
	return value, nil
}

// PricingRule is the operator's configuration for one product.
type PricingRule struct {
	Currency string
	// MarkupBPS is added to the provider's cost in basis points: 1500 is a
	// fifteen per cent markup.
	MarkupBPS int
	Rounding  string
	// FixedAmountMinor opts out of derivation entirely. When set, the customer
	// pays it regardless of what the provider quoted, and the operator absorbs
	// the variance.
	FixedAmountMinor *int64
	// UnitPriceMinor is a fixed price per Star. It is the fork's
	// STARS_SHOP_PRICE_PER_STAR_KOPEKS, and like a fixed amount it opts out of
	// derivation: the customer pays it times the Stars bought, whatever the
	// provider charges that minute.
	UnitPriceMinor *int64
	QuoteTTL       time.Duration
}

// OperatorPriced reports a price the operator chose rather than one derived
// from a provider quote. Such a price does not move with the provider's rate,
// which is what makes it safe to charge unattended after a top-up.
func (rule PricingRule) OperatorPriced() bool {
	return rule.FixedAmountMinor != nil || rule.UnitPriceMinor != nil
}

// OperatorTotal is the operator's price for an order of `quantity` units that
// delivers `stars` Stars in total: the per-Star price times the Stars, or the
// fixed amount times the units. The second result is false when the rule
// derives its price from the provider instead. Every surface — the bot, the
// web shop, the saved-cart sweep, and the order transaction that re-checks it
// under lock — asks this one function, so they cannot disagree.
func (rule PricingRule) OperatorTotal(stars, quantity int) (int64, bool) {
	switch {
	case rule.UnitPriceMinor != nil:
		return max(*rule.UnitPriceMinor, 0) * int64(max(stars, 0)), true
	case rule.FixedAmountMinor != nil:
		return max(*rule.FixedAmountMinor, 0) * int64(max(quantity, 1)), true
	default:
		return 0, false
	}
}

// Fragment's own bounds on one Stars purchase.
const (
	FragmentMinStars = 50
	FragmentMaxStars = 10_000_000
)

// StarAmount describes how many Stars a product delivers: a fixed pack, or a
// customer-chosen amount between two bounds.
type StarAmount struct {
	Pack    int
	Min     int
	Max     int
	Presets []int
}

// Custom reports a product whose customer picks the amount.
func (amount StarAmount) Custom() bool { return amount.Pack == 0 && amount.Max > 0 }

// ErrStarAmount reports an amount outside the product's bounds.
var ErrStarAmount = errors.New("star amount is outside the allowed range")

// Resolve turns what the customer asked for into the Stars one line delivers.
// A pack ignores the request; a custom amount must fall inside its bounds.
func (amount StarAmount) Resolve(requested int) (int, error) {
	if !amount.Custom() {
		return amount.Pack, nil
	}
	if requested < amount.Min || requested > amount.Max {
		return 0, ErrStarAmount
	}
	return requested, nil
}

// Price converts a provider cost into what the customer is charged.
//
// `exponent` is how many minor units make up one major unit of the currency, so
// the unit-rounding modes mean the same thing whether the shop prices in
// roubles or in Stars.
//
// The markup is rounded up rather than truncated. Truncating loses a minor unit
// on most quotes, and a shop that loses a minor unit on every sale is a shop
// whose configured margin is not the margin it earns.
func Price(costMinor int64, rule PricingRule, exponent int) int64 {
	if rule.FixedAmountMinor != nil {
		return max(*rule.FixedAmountMinor, 0)
	}
	if costMinor <= 0 {
		return 0
	}

	markup := int64(0)
	if rule.MarkupBPS > 0 {
		// Ceiling division: (a + b - 1) / b, on values that cannot overflow for
		// any cost a real provider quotes.
		markup = (costMinor*int64(rule.MarkupBPS) + 9_999) / 10_000
	}
	return roundUp(costMinor+markup, rule.Rounding, exponent)
}

// roundUp applies the configured rounding step. An unrecognised mode is treated
// as `none`, so a row written by a future version cannot make a running
// installation charge a number nobody chose.
func roundUp(amount int64, rounding string, exponent int) int64 {
	step := int64(1)
	for range exponent {
		step *= 10
	}

	switch rounding {
	case RoundUnit:
		// step is already one major unit.
	case RoundTenUnits:
		step *= 10
	case RoundHundredUnits:
		step *= 100
	default:
		// `none` and `up_minor` both land on a whole minor unit, which an
		// integer amount already is.
		return amount
	}

	if step <= 1 {
		return amount
	}
	if remainder := amount % step; remainder != 0 {
		return amount + (step - remainder)
	}
	return amount
}

// Quote is what a provider says a purchase will cost the operator, plus how
// long that number may be relied on.
type Quote struct {
	CostMinor int64
	Currency  string
	// ExpiresAt is when the provider's number stops being honourable. An
	// adapter whose rate does not move may return the zero value, and the
	// service applies the configured TTL instead.
	ExpiresAt time.Time
}

// Fresh reports whether a quote may still be charged against.
func (quote Quote) Fresh(now time.Time) bool {
	return quote.ExpiresAt.IsZero() || quote.ExpiresAt.After(now)
}

// Request describes what is being bought and for whom.
type Request struct {
	Kind           string
	DurationMonths int
	StarQuantity   int
	Quantity       int
	Recipient      string
	Currency       string
}

// DeliveryRequest is one submission.
//
// `IdempotencyKey` is derived from the order, so a retried worker, a duplicated
// job, and a replayed webhook all present the same one. An adapter whose
// provider honours it gets exactly-once for free; one whose provider does not
// must report a lost answer as FailureAmbiguous rather than guessing, because
// the database primary key on `goods_deliveries.order_id` can stop a second
// *attempt* but cannot undo a purchase that already happened.
//
// `BuyerTelegramID` is bookkeeping for a gateway that keys its own customer
// records by Telegram identifier. It is the buyer's, never the recipient's:
// delivery is addressed by username, and Omniflow does not know the numeric
// identifier behind an arbitrary handle.
type DeliveryRequest struct {
	Request
	OrderID         string
	IdempotencyKey  string
	BuyerTelegramID int64
	// BeforeSettle is called by an adapter that pays the provider itself,
	// immediately before money leaves the operator's wallet. The caller
	// persists the marker in a transaction of its own and commits it before
	// returning, so a crash from then on is recognisable as "money may have
	// left" rather than "nothing happened".
	//
	// An adapter must not send anything if it returns an error, and must
	// report that failure as one that happened before payment. A nil hook
	// means the caller does not need the marker.
	BeforeSettle func(context.Context, SettlementMarker) error
}

// SettlementMarker is what an on-chain adapter records just before paying.
type SettlementMarker struct {
	// ProviderRequestID is the provider's own identifier for the purchase,
	// which an operator quotes when checking with the provider.
	ProviderRequestID string
	// Asset is the currency the provider is paid in, such as "TON".
	Asset string
	// CostNano is the amount about to be paid, in the asset's smallest unit.
	CostNano int64
}

// Delivery is the provider's view of one submission.
type Delivery struct {
	Reference string
	// Status is one of "submitted", "delivered", or "failed".
	Status       string
	FailureClass string
	ErrorCode    string
	// Settlement describes what the operator actually paid, for an adapter
	// that pays the provider itself. It is nil for a gateway that settles on
	// its own side.
	Settlement *Settlement
}

// Settlement is the on-chain payment behind a delivery.
type Settlement struct {
	ProviderRequestID string
	Asset             string
	CostNano          int64
	// TxHash identifies the payment on chain (the normalized external
	// message hash for TON), so an operator can open it in an explorer.
	TxHash string
	// ProviderConfirmed is false when the payment went through but the
	// provider was not told about it. The goods are still owed by the
	// provider; nothing on Omniflow's side is retried because of it.
	ProviderConfirmed bool
	// DryRun marks a delivery that went through every state without money
	// or a provider being involved.
	DryRun bool
	// BalanceAfterNano is the wallet balance once the payment confirmed,
	// when known.
	BalanceAfterNano *int64
}

// WalletInfo is the operator's own funding wallet, for an adapter that holds
// one.
type WalletInfo struct {
	Address     string
	Version     string
	Asset       string
	BalanceNano int64
}

// WalletReporter is implemented by an adapter that pays from the operator's
// own wallet. The panel's wallet card and the sale notification read it.
type WalletReporter interface {
	Wallet(context.Context) (WalletInfo, error)
	// StarsPriceNano is the provider's current price for `stars` Stars, in
	// the wallet asset's smallest unit.
	StarsPriceNano(ctx context.Context, stars int) (int64, error)
}

// Provider is the contract an adapter implements.
//
// It is deliberately small. Pricing, retry policy, refunds, and the customer
// record are all outside it, so adding a second provider means implementing
// five methods rather than re-deciding how the shop works.
type Provider interface {
	Name() string
	// Supports reports whether the adapter sells a product kind at all.
	Supports(kind string) bool
	// Quote returns the operator's cost. It must not reserve, charge, or
	// deliver anything.
	Quote(context.Context, Request) (Quote, error)
	// Deliver submits a purchase. It must be safe to call more than once with
	// the same idempotency key.
	Deliver(context.Context, DeliveryRequest) (Delivery, error)
	// Poll re-reads a submission that did not complete synchronously.
	Poll(context.Context, string) (Delivery, error)
	// Balance reports the operator's remaining funds with the provider.
	Balance(context.Context) (int64, string, error)
}

// RecipientValidator is implemented by adapters that can check a username
// before payment. An adapter that cannot simply does not implement it, and the
// purchase falls back to the confirmation step plus the recipient-invalid
// refund path.
type RecipientValidator interface {
	ValidateRecipient(context.Context, string) error
}

// Retryable reports whether a failure class should be attempted again.
func Retryable(failureClass string) bool {
	switch failureClass {
	case FailureRetryable, FailureProviderBalance, FailureProviderUnavailable:
		return true
	default:
		return false
	}
}

// Refundable reports whether a failure class ends the delivery and returns the
// customer's money.
//
// No class is both retried and refunded. FailureAmbiguous is neither: it is the
// one outcome an automated rule must not resolve, because both answers can be
// wrong in a way that costs somebody money.
func Refundable(failureClass string) bool {
	switch failureClass {
	case FailurePermanent, FailureRecipientInvalid:
		return true
	default:
		return false
	}
}

// NeedsReview reports whether a failure class parks the delivery for an
// operator instead of resolving it.
func NeedsReview(failureClass string) bool {
	return failureClass == FailureAmbiguous
}

// Backoff is the delay before attempt number `attempt` is retried.
//
// It doubles from a minute and stops at an hour. Beyond MaxAttempts the caller
// stops retrying and refunds, so the schedule never needs to describe a delay
// longer than that.
func Backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := time.Minute
	for range attempt - 1 {
		delay *= 2
		if delay >= time.Hour {
			return time.Hour
		}
	}
	return delay
}

// MaxAttempts is where a retryable failure becomes a refund.
//
// Six attempts on the schedule above span a little over two hours, which is
// long enough to ride out a provider restart and short enough that a customer
// is not left waiting on a purchase that is not going to arrive.
const MaxAttempts = 6
