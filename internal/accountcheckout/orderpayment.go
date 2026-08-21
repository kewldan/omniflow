package accountcheckout

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omniflow/omniflow/internal/commerce"
)

// OrderPayable reports whether an order can still take a provider payment.
//
// It is the check the payment service performs under its own lock, made
// before the service is asked, so the refusal arrives as the conflict it is
// rather than as a failure nobody can act on. The clock matters: the sweep
// that marks an order `expired` runs on a schedule, and an order whose hour
// has passed is unpayable from the moment it passes, not from the moment the
// sweep notices.
func OrderPayable(order OrderSummary, now time.Time) error {
	if order.ExternalMinor <= 0 {
		return ErrPaymentNotRequired
	}
	if order.State != commerce.OrderPending {
		return ErrOrderNotPayable
	}
	if !order.ExpiresAt.IsZero() && !now.Before(order.ExpiresAt) {
		return ErrOrderNotPayable
	}
	return nil
}

// OrderPaymentChoices lists the methods that can settle one pending order, in
// the order's own currency and for what it still owes.
//
// It differs from PaymentChoices in one respect: the currency is fixed. A
// checkout may still choose its currency, but an order has been priced, and a
// method that cannot settle that currency is not offered rather than offered
// at a different price. The Telegram Stars method is offered only to a
// customer who can actually receive the invoice — see stars.go.
func (service *Service) OrderPaymentChoices(ctx context.Context, customerID string, order OrderSummary) ([]PaymentChoice, error) {
	choices := make([]PaymentChoice, 0)
	if service.payments == nil || OrderPayable(order, service.clock()) != nil {
		return choices, nil
	}
	for _, option := range service.payments.Options() {
		if !option.Enabled || !option.Supports(order.Currency) {
			continue
		}
		choices = append(choices, PaymentChoice{
			Provider: option.Provider, Currency: order.Currency,
			AmountMinor: order.ExternalMinor, Recurring: option.Recurring,
		})
	}
	return service.forCustomer(ctx, customerID, choices)
}

// ProviderSettles reports whether a configured method can take this order's
// currency, distinguishing "no such method here" from "not in this currency"
// because the customer's remedy differs: pick another method, or accept that
// this order cannot be paid that way at all.
func (service *Service) ProviderSettles(provider, currency string) error {
	if service.payments == nil || !service.payments.Enabled(provider) {
		return ErrProviderUnavailable
	}
	for _, option := range service.payments.Options() {
		if option.Provider != provider {
			continue
		}
		if !option.Enabled {
			return ErrProviderUnavailable
		}
		if !option.Supports(currency) {
			return ErrProviderCurrency
		}
		return nil
	}
	return ErrProviderUnavailable
}

// RecordedOrderProvider is the method the checkout that became this order had
// chosen, when that checkout still exists.
//
// A freshly confirmed order has no payment intent and so no provider of its
// own; the only record of what the customer picked is the checkout session
// that was attached to the order on confirmation. Reading it here is what lets
// the order page preselect the same method after a reload, a second tab, or
// an arrival from the order list — none of which carry the URL parameter the
// checkout used to pass along.
func (store *Store) RecordedOrderProvider(ctx context.Context, customerID, orderID string) (string, error) {
	var provider string
	err := store.pool.QueryRow(ctx, `SELECT COALESCE(provider, '')
		FROM bot_checkout_sessions WHERE user_id = $1::uuid AND order_id = $2::uuid`,
		customerID, orderID).Scan(&provider)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return provider, err
}
