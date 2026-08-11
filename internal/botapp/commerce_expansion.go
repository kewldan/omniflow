package botapp

import (
	"context"
	"sort"
	"time"

	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// TopUpLimits reports the operator-configured wallet top-up policy.
func (service *Commerce) TopUpLimits() commerce.TopUpLimits { return service.orders.TopUpLimits() }

// SubscriptionPolicy reports the concurrency policy this installation enforces.
func (service *Commerce) SubscriptionPolicy() commerce.SubscriptionPolicy {
	return service.orders.SubscriptionPolicy()
}

// TopUpAllowance reports how much the customer already credited inside the
// rolling window, which is what the presets are filtered against.
func (service *Commerce) TopUpAllowance(ctx context.Context, customerID, currency string) (int64, error) {
	return service.orders.TopUpAllowance(ctx, customerID, currency)
}

// ExternalPaymentChoices lists the payment methods that can settle an amount
// in one currency, regardless of a plan. A top-up and a mid-period add-on both
// use it, because neither is tied to a plan's own price list.
func (service *Commerce) ExternalPaymentChoices(currency string) []PaymentChoice {
	choices := make([]PaymentChoice, 0, 4)
	for _, option := range service.payments.Options() {
		if !option.Enabled || !option.Supports(currency) {
			continue
		}
		choices = append(choices, PaymentChoice{Provider: option.Provider, Currency: currency, Recurring: option.Recurring})
	}
	sort.SliceStable(choices, func(left, right int) bool { return choices[left].Provider < choices[right].Provider })
	return choices
}

// StartTopUp creates the top-up order and its provider payment in one step. The
// idempotency key is minted once per attempt, so a duplicate tap that reaches
// this method twice creates one order and one payment.
func (service *Commerce) StartTopUp(ctx context.Context, customerID, currency string, amountMinor int64, provider string, telegramID int64) (OrderSummary, error) {
	key, err := newIdempotencyKey()
	if err != nil {
		return OrderSummary{}, err
	}
	order, err := service.orders.CreateTopUpOrder(ctx, commercepg.TopUpInput{
		CustomerID: customerID, Currency: currency, AmountMinor: amountMinor, IdempotencyKey: key,
	})
	if err != nil {
		return OrderSummary{}, err
	}
	summary, err := service.store.Order(ctx, customerID, uuidText(order.ID), LocaleEnglish)
	if err != nil {
		return OrderSummary{}, err
	}
	if _, err = service.StartPayment(ctx, summary, provider, telegramID, "Wallet top-up"); err != nil {
		return summary, err
	}
	return service.store.Order(ctx, customerID, summary.ID, LocaleEnglish)
}

// TopUpHistory lists the customer's top-ups with their payment state attached.
func (service *Commerce) TopUpHistory(ctx context.Context, customerID string, limit int) ([]dbgen.ListWalletTopupsRow, error) {
	return service.orders.TopUpHistory(ctx, customerID, limit)
}

// TopUpRejectionReason recovers the stable machine reason from a wrapped top-up
// rejection so the bot can show localized copy for it.
func TopUpRejectionReason(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	for _, reason := range []string{
		commerce.TopUpDisabled, commerce.TopUpBelowMinimum, commerce.TopUpAboveMaximum,
		commerce.TopUpWindowExceeded, commerce.TopUpInvalidAmount,
	} {
		if len(message) >= len(reason) && message[len(message)-len(reason):] == reason {
			return reason
		}
	}
	return ""
}

// SaveCart stores the open checkout as a saved cart so the customer can top up
// and have it bought automatically once the balance covers it.
func (service *Commerce) SaveCart(ctx context.Context, session CheckoutSession, addons []CheckoutAddon, autoPurchase bool) (commercepg.Cart, error) {
	selections := make([]commercepg.AddonSelection, 0, len(addons))
	for _, addon := range addons {
		selections = append(selections, commercepg.AddonSelection{AddonVersionID: addon.AddonVersionID, Quantity: addon.Quantity})
	}
	return service.orders.SaveCart(ctx, commercepg.CartInput{
		CustomerID: session.CustomerID, SubscriptionID: session.SubscriptionID,
		PlanVersionID: session.PlanVersionID, Operation: session.Operation, Currency: session.Currency,
		PromoCode: session.PromoCode, SelectedSquadIDs: session.SelectedSquadIDs,
		Addons: selections, AutoPurchase: autoPurchase, TTL: service.settings.CartTTL,
	})
}

// Cart reads the customer's saved cart together with its freshly re-validated
// price. The saved price is never reused: a cart is always quoted against the
// catalog as it is right now.
func (service *Commerce) Cart(ctx context.Context, customerID string) (commercepg.Cart, commerce.CartQuote, bool, error) {
	cart, found, err := service.orders.OpenCart(ctx, customerID)
	if err != nil || !found {
		return commercepg.Cart{}, commerce.CartQuote{}, false, err
	}
	quote, err := service.orders.QuoteCart(ctx, cart)
	if err != nil {
		return cart, commerce.CartQuote{}, true, err
	}
	return cart, quote, true, nil
}

// DiscardCart clears the saved cart, which also cancels the pending automatic
// purchase.
func (service *Commerce) DiscardCart(ctx context.Context, customerID string) error {
	return service.orders.DiscardCart(ctx, customerID)
}

// SetCartAutoPurchase turns automatic purchase on or off while keeping the cart.
func (service *Commerce) SetCartAutoPurchase(ctx context.Context, customerID string, enabled bool) error {
	return service.orders.SetCartAutoPurchase(ctx, customerID, enabled)
}

// PurchaseCart charges a saved cart now if the balance covers it.
func (service *Commerce) PurchaseCart(ctx context.Context, customerID string) (commercepg.CartPurchase, error) {
	return service.orders.TryPurchaseCart(ctx, customerID)
}

// BuyAddon purchases one mid-period add-on for a subscription. Pricing, the
// proration rule, and the entitlement change all come from the add-on version
// that is current at the moment of purchase.
func (service *Commerce) BuyAddon(ctx context.Context, customerID, subscriptionID, currency, addonVersionID string, quantity int, provider string, telegramID int64) (OrderSummary, error) {
	key, err := newIdempotencyKey()
	if err != nil {
		return OrderSummary{}, err
	}
	order, err := service.orders.CreateAddonOrder(ctx, commercepg.AddonOrderInput{
		CustomerID: customerID, SubscriptionID: subscriptionID, Currency: currency,
		Addons:         []commercepg.AddonSelection{{AddonVersionID: addonVersionID, Quantity: quantity}},
		IdempotencyKey: key,
	})
	if err != nil {
		return OrderSummary{}, err
	}
	summary, err := service.store.Order(ctx, customerID, uuidText(order.ID), LocaleEnglish)
	if err != nil {
		return OrderSummary{}, err
	}
	if summary.ExternalMinor == 0 {
		return summary, nil
	}
	if _, err = service.StartPayment(ctx, summary, provider, telegramID, "Subscription add-on"); err != nil {
		return summary, err
	}
	return service.store.Order(ctx, customerID, summary.ID, LocaleEnglish)
}

// Maintenance reads the installation-wide maintenance record.
func (service *Commerce) Maintenance(ctx context.Context) (commerce.Maintenance, error) {
	return service.orders.Maintenance(ctx)
}

// cartExpiry renders how long a saved cart still has.
func cartExpiry(cart commercepg.Cart, now time.Time) time.Duration {
	if !cart.ExpiresAt.Valid {
		return 0
	}
	remaining := cart.ExpiresAt.Time.Sub(now)
	if remaining < 0 {
		return 0
	}
	return remaining
}
