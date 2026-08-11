package commerce

import (
	"sort"
	"strings"
)

// CheckoutQuote is the price breakdown a customer confirms before an order is
// created. It is derived from the same arithmetic as NewOrder so the confirmed
// figures and the persisted order can never disagree.
type CheckoutQuote struct {
	Subtotal           Money
	DiscountMinor      int64
	WalletBalanceMinor int64
	WalletAppliedMinor int64
	ExternalMinor      int64
	PromoCode          string
	PromoRejection     string
	// AddonMinor is what the selected add-ons add to this order. They are
	// charged on the same order as the plan, so one confirmation is one payment.
	AddonMinor int64
}

// Quote computes the payable breakdown for a plan price, an already-validated
// discount, and the customer's available wallet balance.
func Quote(price Money, discountMinor, walletBalanceMinor int64, applyWallet bool) (CheckoutQuote, error) {
	if walletBalanceMinor < 0 {
		return CheckoutQuote{}, ErrInvalidAmount
	}
	usable := walletBalanceMinor
	if !applyWallet {
		usable = 0
	}
	order, err := NewOrder("", "", price, discountMinor, usable)
	if err != nil {
		return CheckoutQuote{}, err
	}
	return CheckoutQuote{
		Subtotal:           price,
		DiscountMinor:      order.DiscountMinor,
		WalletBalanceMinor: walletBalanceMinor,
		WalletAppliedMinor: order.WalletMinor,
		ExternalMinor:      order.ExternalMinor,
	}, nil
}

// PaymentOption describes one configured payment adapter as far as customer
// selection is concerned.
type PaymentOption struct {
	Provider string
	// Enabled is false when the operator has not configured credentials.
	Enabled bool
	// Currencies lists the ISO codes the adapter settles. An empty list means
	// the adapter accepts whatever the order is denominated in.
	Currencies []string
	Recurring  bool
	// Order controls presentation only.
	Order int
}

// Supports reports whether the adapter can settle the given currency.
func (option PaymentOption) Supports(currency string) bool {
	if len(option.Currencies) == 0 {
		return true
	}
	currency = strings.ToUpper(strings.TrimSpace(currency))
	for _, supported := range option.Currencies {
		if strings.ToUpper(supported) == currency {
			return true
		}
	}
	return false
}

// EligibleProviders returns the adapters a customer may choose for one order.
// Selection is based only on enablement and currency compatibility: an adapter
// is never offered as a placeholder.
func EligibleProviders(options []PaymentOption, currency string, externalMinor int64) []PaymentOption {
	eligible := make([]PaymentOption, 0, len(options))
	if externalMinor <= 0 {
		return eligible
	}
	for _, option := range options {
		if !option.Enabled || !option.Supports(currency) {
			continue
		}
		eligible = append(eligible, option)
	}
	sort.SliceStable(eligible, func(left, right int) bool {
		if eligible[left].Order != eligible[right].Order {
			return eligible[left].Order < eligible[right].Order
		}
		return eligible[left].Provider < eligible[right].Provider
	})
	return eligible
}

// SupportsAutoRenew reports whether any eligible adapter can charge the customer
// again without a new confirmation. Auto-renew must never be advertised when no
// configured adapter can honour it.
func SupportsAutoRenew(options []PaymentOption, currency string) bool {
	for _, option := range options {
		if option.Enabled && option.Recurring && option.Supports(currency) {
			return true
		}
	}
	return false
}

// PaymentPhase is the customer-visible state of one checkout attempt.
type PaymentPhase string

const (
	PaymentPhaseAwaitingAction PaymentPhase = "awaiting_action"
	PaymentPhasePending        PaymentPhase = "pending"
	PaymentPhaseSucceeded      PaymentPhase = "succeeded"
	PaymentPhaseProvisioning   PaymentPhase = "provisioning"
	PaymentPhaseCompleted      PaymentPhase = "completed"
	PaymentPhaseFailed         PaymentPhase = "failed"
	PaymentPhaseCancelled      PaymentPhase = "cancelled"
	PaymentPhaseExpired        PaymentPhase = "expired"
	PaymentPhaseRefunded       PaymentPhase = "refunded"
)

// EvaluatePaymentPhase folds the order state, the payment-intent status, and the
// fulfillment-operation status into one screen state. The order is authoritative
// once it leaves the pending states, which keeps a late or duplicated provider
// webhook from moving a paid order backwards.
func EvaluatePaymentPhase(orderState OrderState, intentStatus, fulfillmentStatus string) PaymentPhase {
	switch orderState {
	case OrderRefunded, OrderPartiallyRefunded:
		return PaymentPhaseRefunded
	case OrderCancelled:
		return PaymentPhaseCancelled
	case OrderExpired:
		return PaymentPhaseExpired
	case OrderFulfilled:
		return PaymentPhaseCompleted
	case OrderPaid:
		switch fulfillmentStatus {
		case "succeeded":
			return PaymentPhaseCompleted
		case "failed", "cancelled":
			return PaymentPhaseFailed
		default:
			return PaymentPhaseProvisioning
		}
	}
	switch intentStatus {
	case "succeeded":
		return PaymentPhaseSucceeded
	case "failed":
		return PaymentPhaseFailed
	case "cancelled":
		return PaymentPhaseCancelled
	case "expired":
		return PaymentPhaseExpired
	case "requires_action", "":
		return PaymentPhaseAwaitingAction
	default:
		return PaymentPhasePending
	}
}
