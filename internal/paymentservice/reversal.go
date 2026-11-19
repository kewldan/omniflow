package paymentservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/payments"
)

// Classifications HandleWebhook reports for a provider-side reversal.
const (
	// ClassificationReversed is a refund or chargeback that was recorded and
	// whose purchase was taken back.
	ClassificationReversed = "reversed"
	// ClassificationReversalKnown is a reversal already accounted for: a
	// replayed notification, or a refund Omniflow itself asked for.
	ClassificationReversalKnown = "reversal_known"
)

// applyReversal records money a provider returned to the payer after the
// payment had settled: a refund made from the provider's dashboard, by the
// payer's bank, or a chargeback.
//
// It is the provider-initiated twin of Refund. The provider has already moved
// the money, so nothing is asked of it; what is left is Omniflow's own record.
// A refund row is written as succeeded, the ledger is reversed, and what the
// payment bought is taken back in the same commit — the wallet credit of a
// top-up, the period of a subscription — exactly as an operator refund does.
//
// `cumulative` is what the provider says has gone back in total. Only the part
// Omniflow has not already recorded is applied, which is what makes a replayed
// notification, a later poll, and an operator refund that the provider also
// reports all converge on one reversal. Zero means the whole payment.
func (service *Service) applyReversal(
	ctx context.Context, intent dbgen.PaymentIntent, status string, cumulative int64, eventID string,
) (string, error) {
	connection, err := service.pool.Acquire(ctx)
	if err != nil {
		return "", err
	}
	defer connection.Release()
	queries := dbgen.New(connection)
	intentID := uuidString(intent.ID)
	lockKey := "refund:" + intentID
	if err = queries.AcquirePaymentMutationLock(ctx, lockKey); err != nil {
		return "", err
	}
	defer func() { _ = queries.ReleasePaymentMutationLock(context.WithoutCancel(ctx), lockKey) }()
	if intent, err = queries.GetPaymentIntent(ctx, intent.ID); err != nil {
		return "", err
	}
	if intent.Status != "succeeded" {
		// Nothing was applied for this payment, so nothing is taken back.
		return ClassificationReversalKnown, nil
	}
	if cumulative <= 0 || cumulative > intent.AmountMinor {
		cumulative = intent.AmountMinor
	}
	reserved, err := queries.GetReservedRefundAmount(ctx, intent.ID)
	if err != nil {
		return "", err
	}
	delta := cumulative - reserved
	if delta <= 0 {
		return ClassificationReversalKnown, nil
	}
	reason := "provider_refund"
	if status == payments.StatusChargeback {
		reason = "provider_chargeback"
	}
	// The key names the cumulative level the provider reported, so a replay
	// of the same notification lands on the same row and a later, larger
	// refund is a new one.
	key := fmt.Sprintf("%s:%d", reason, cumulative)
	receipt, _ := json.Marshal(map[string]any{"source": "provider", "providerEventId": eventID})
	refund, err := queries.CreateRefund(ctx, dbgen.CreateRefundParams{
		PaymentIntentID: intent.ID, Status: "succeeded", AmountMinor: delta, Currency: intent.Currency,
		ProviderReference: optionalText(eventID), Reason: reason, IdempotencyKey: key, ReceiptMetadata: receipt,
	})
	if err != nil {
		return "", err
	}
	if refund.Status != "succeeded" {
		return "", errors.New("a reversal key was already used for a refund that did not succeed")
	}
	amount := commerce.Money{Amount: refund.AmountMinor, Currency: refund.Currency}
	if _, err = service.commerce.RecordRefund(ctx, intentID, uuidString(refund.ID), "refund:"+intentID+":"+key, amount); err != nil {
		return "", err
	}
	if status == payments.StatusChargeback {
		// A disputed card is not one to charge again without the customer.
		_ = queries.FailPaymentMethodsFromIntent(ctx, intent.ID)
		payload, _ := json.Marshal(map[string]any{
			"orderId": uuidString(intent.OrderID), "provider": intent.Provider, "currency": intent.Currency,
			"amountMinor": delta, "status": "payment_chargeback",
		})
		_, _ = queries.EnqueueOperatorNotification(ctx, dbgen.EnqueueOperatorNotificationParams{
			Kind: "incident", DedupeKey: "chargeback:" + intentID + ":" + key, Payload: payload,
		})
	}
	return ClassificationReversed, nil
}
