package paymentservice

import (
	"context"
	"errors"

	"github.com/omniflow/omniflow/internal/commercepg"
)

// CreditAsPayment puts money on a customer's wallet the way a paid top-up
// does, for an operator who received it outside every provider (Bedolaga's
// manual top-up).
//
// It is a real top-up order paid through the manual adapter and approved by
// the operator, so it goes through the one settlement path every payment
// takes: the top-up limits apply, the credit is recorded as a payment rather
// than a correction, a saved cart is bought from it, and it counts as the
// customer's first top-up for the payment buttons that care. The key makes a
// retried click the same credit.
func (service *Service) CreditAsPayment(
	ctx context.Context, customerID, currency string, amountMinor int64, key, operatorID, reason string,
) (string, error) {
	if amountMinor <= 0 || operatorID == "" || reason == "" {
		return "", errors.New("a credit as payment needs a positive amount, an operator, and a reason")
	}
	if _, ok := service.providers["manual"]; !ok {
		return "", errors.New("the manual payment adapter is not registered")
	}
	idempotency := "operator-credit:" + key
	order, err := service.commerce.CreateTopUpOrder(ctx, commercepg.TopUpInput{
		CustomerID: customerID, Currency: currency, AmountMinor: amountMinor,
		IdempotencyKey: idempotency, Provider: "manual",
	})
	if err != nil {
		return "", err
	}
	orderID := uuidString(order.ID)
	if order.State == "paid" || order.State == "fulfilled" {
		// The same click again: the credit already settled.
		return orderID, nil
	}
	intent, err := service.CreateIntent(ctx, CreateIntentInput{
		OrderID: orderID, Provider: "manual", IdempotencyKey: idempotency, Description: "Operator credit",
	})
	if err != nil {
		return orderID, err
	}
	if _, err = service.ApproveManual(ctx, uuidString(intent.ID), operatorID, reason, idempotency, "", true); err != nil {
		return orderID, err
	}
	return orderID, nil
}
