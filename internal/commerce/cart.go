package commerce

import (
	"errors"
	"time"
)

// ErrCartRejected wraps the reasons a saved cart cannot be turned into an order
// right now. A rejection is never destructive: the cart stays saved so the
// customer can top up, remove an add-on, or wait for the catalog to settle.
var ErrCartRejected = errors.New("cart cannot be purchased")

// Cart rejection reasons.
const (
	CartInsufficientBalance = "cart_insufficient_balance"
	CartPlanUnavailable     = "cart_plan_unavailable"
	CartPriceChanged        = "cart_price_changed"
	CartExpired             = "cart_expired"
	CartAutoPurchaseOff     = "cart_auto_purchase_off"
	CartMaintenance         = "cart_maintenance"
)

// CartQuote is the re-validated price of a saved cart. It is recomputed against
// the current plan version immediately before any charge, so a cart that was
// saved at an old price never charges the old price.
type CartQuote struct {
	Currency      string
	SubtotalMinor int64
	DiscountMinor int64
	AddonMinor    int64
	// TotalMinor is what the customer owes after the discount and add-ons.
	TotalMinor int64
	// WalletBalanceMinor is the spendable balance, excluding credit already
	// reserved by another draft or pending order.
	WalletBalanceMinor int64
	PromoRejection     string
}

// Covered reports whether the wallet alone can settle the cart.
func (quote CartQuote) Covered() bool {
	return quote.WalletBalanceMinor >= quote.TotalMinor
}

// MissingMinor is how much more credit the cart still needs.
func (quote CartQuote) MissingMinor() int64 {
	if quote.Covered() {
		return 0
	}
	return quote.TotalMinor - quote.WalletBalanceMinor
}

// AutoPurchaseRequest is everything the deferred-purchase decision needs.
type AutoPurchaseRequest struct {
	Enabled     bool
	ExpiresAt   time.Time
	Now         time.Time
	Quote       CartQuote
	Maintenance bool
}

// EvaluateAutoPurchase decides whether a saved cart may be charged now. It
// returns the stable rejection reason with ErrCartRejected, or "ready" and a
// nil error.
//
// The decision is deliberately conservative: the caller still creates the order
// under the cart's own idempotency key, so even a duplicated or replayed wallet
// credit that reaches this point twice resolves to the same single order.
func EvaluateAutoPurchase(request AutoPurchaseRequest) (string, error) {
	if !request.Enabled {
		return CartAutoPurchaseOff, ErrCartRejected
	}
	if !request.ExpiresAt.IsZero() && !request.Now.Before(request.ExpiresAt) {
		return CartExpired, ErrCartRejected
	}
	if request.Maintenance {
		return CartMaintenance, ErrCartRejected
	}
	if !request.Quote.Covered() {
		return CartInsufficientBalance, ErrCartRejected
	}
	return "ready", nil
}
