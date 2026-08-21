package accountcheckout

import (
	"errors"
	"fmt"

	"github.com/omniflow/omniflow/internal/accountpg"
)

// The checkout's own failure states.
//
// Three of them wrap accountpg.ErrNotFound on purpose. "You have no open
// checkout", "that order is not yours", and "that plan no longer exists" are all
// answered with the same 404, so an identifier taken from a URL cannot be probed
// for the existence of somebody else's record. Wrapping rather than reusing the
// sentinel keeps the transport able to choose wording that fits what was asked
// for, which a shared sentinel alone could not do.
var (
	// ErrNoCheckout reports that the customer has nothing in progress. It is a
	// normal state, not a failure: the panel renders the plan catalogue instead.
	ErrNoCheckout = fmt.Errorf("no checkout is open: %w", accountpg.ErrNotFound)
	// ErrCheckoutSettled reports an edit to a checkout that already became an
	// order. The customer's next move is the order, not this checkout, so it is a
	// conflict rather than a validation error.
	ErrCheckoutSettled = errors.New("this checkout has already become an order")
	// ErrOrderNotFound reports an order that does not belong to the caller.
	ErrOrderNotFound = fmt.Errorf("order not found: %w", accountpg.ErrNotFound)
	// ErrPlanUnavailable reports a plan version that was archived, retired, or
	// lost its price between the catalogue being rendered and being acted on.
	ErrPlanUnavailable = fmt.Errorf("plan is no longer available: %w", accountpg.ErrNotFound)
	// ErrProviderUnavailable reports a payment method this installation has not
	// configured. It is deliberately not a 404: the provider exists as a concept,
	// it just cannot take this customer's money here.
	ErrProviderUnavailable = errors.New("that payment method is not available")
	// ErrPaymentNotRequired reports a payment started against an order that owes
	// nothing externally, which is what a fully wallet-funded order is.
	ErrPaymentNotRequired = errors.New("this order does not need a payment")
	// ErrOrderNotCancellable reports a cancellation of an order that money has
	// already moved against. Refunds are an operator action, not a customer one.
	ErrOrderNotCancellable = errors.New("only an unpaid order can be cancelled")
	// ErrOrderNotPayable reports a payment started against an order that is no
	// longer pending — cancelled, expired, or already settled. It is a conflict
	// rather than a failure: the order moved on between the page rendering and
	// the button being pressed, and the page has to re-read it.
	ErrOrderNotPayable = errors.New("this order can no longer be paid")
	// ErrProviderCurrency reports a configured payment method asked to settle
	// an order in a currency it does not take. The method exists and works; it
	// is the pairing that is wrong, so the remedy is another method.
	ErrProviderCurrency = errors.New("that payment method cannot settle this order's currency")
	// ErrSubscriptionTargetRequired reports a lifecycle flow that did not say
	// which subscription it acts on, in an installation where that is ambiguous.
	// It is its own state rather than a generic validation failure because the
	// panel's response to it is specific: show the picker, not a message.
	ErrSubscriptionTargetRequired = fmt.Errorf(
		"subscription_target_required: %w", accountpg.ErrInvalidInput,
	)
)

// Promo rejection reasons. They are stable machine values rather than sentences
// because both panels look up their own localized copy for them, and a reason
// that changed wording would silently become an untranslated string.
const (
	PromoUnknown    = "promo_unknown"
	PromoIneligible = "promo_ineligible"
	PromoExhausted  = "promo_exhausted"
	PromoInvalid    = "promo_invalid"
)

// invalidInput wraps a value the customer supplied that this package refused,
// carrying the reason so the panel can show why without restating the rule.
func invalidInput(format string, arguments ...any) error {
	return fmt.Errorf("%w: %s", accountpg.ErrInvalidInput, fmt.Sprintf(format, arguments...))
}
