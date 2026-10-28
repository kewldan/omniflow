package paymentservice

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/payments"
)

// A refund is written down before the provider is asked.
//
// The order used to be the other way round: the provider moved the money
// first and the `refunds` row was inserted afterwards, so a process that
// stopped between the two had returned money with no local record of it — no
// row, no ledger movement, no change of order state, and nothing that would
// ever notice. Now the row exists, `pending` and without a provider reference,
// before any request leaves; the provider's answer is written onto it; and a
// row still waiting on an answer is asked about again with the same
// idempotency key until one arrives.

// refundRevivalBatch bounds one revival pass.
const refundRevivalBatch = 50

// Refund returns money for a settled payment intent.
func (service *Service) Refund(ctx context.Context, paymentIntentID, idempotencyKey string, amount commerce.Money, reason string, receiptMetadata map[string]any) (dbgen.Refund, error) {
	if !validIdempotencyKey(idempotencyKey) || len(reason) == 0 {
		return dbgen.Refund{}, errors.New("refund requires a valid idempotency key and reason")
	}
	id, err := parseUUID(paymentIntentID)
	if err != nil {
		return dbgen.Refund{}, err
	}
	connection, err := service.pool.Acquire(ctx)
	if err != nil {
		return dbgen.Refund{}, err
	}
	defer connection.Release()
	queries := dbgen.New(connection)
	lockKey := "refund:" + paymentIntentID
	if err = queries.AcquirePaymentMutationLock(ctx, lockKey); err != nil {
		return dbgen.Refund{}, err
	}
	defer func() { _ = queries.ReleasePaymentMutationLock(context.WithoutCancel(ctx), lockKey) }()
	intent, err := queries.GetPaymentIntent(ctx, id)
	if err != nil {
		return dbgen.Refund{}, err
	}
	if intent.Status != "succeeded" || amount.Currency != intent.Currency || amount.Amount <= 0 || amount.Amount > intent.AmountMinor {
		return dbgen.Refund{}, errors.New("invalid refund")
	}
	provider, ok := service.providers[intent.Provider]
	if !ok || !provider.Capabilities().Refunds || !intent.ProviderReference.Valid {
		return dbgen.Refund{}, payments.ErrUnsupported
	}
	if existing, existingErr := queries.GetRefundByIdempotency(ctx, dbgen.GetRefundByIdempotencyParams{PaymentIntentID: intent.ID, IdempotencyKey: idempotencyKey}); existingErr == nil {
		if existing.AmountMinor != amount.Amount || existing.Currency != amount.Currency {
			return dbgen.Refund{}, errors.New("idempotency key was already used with different refund parameters")
		}
		if unanswered(existing) {
			// The same request again, for a refund whose provider answer was
			// never recorded: ask the provider again under the same key.
			return service.askProvider(ctx, queries, provider, intent, existing, true)
		}
		if existing.Status == "succeeded" {
			if _, err = service.commerce.RecordRefund(ctx, paymentIntentID, uuidString(existing.ID), "refund:"+idempotencyKey, commerce.Money{Amount: existing.AmountMinor, Currency: existing.Currency}); err != nil {
				return dbgen.Refund{}, err
			}
		}
		return existing, nil
	} else if !errors.Is(existingErr, pgx.ErrNoRows) {
		return dbgen.Refund{}, existingErr
	}
	reserved, err := queries.GetReservedRefundAmount(ctx, intent.ID)
	if err != nil {
		return dbgen.Refund{}, err
	}
	if reserved+amount.Amount > intent.AmountMinor {
		return dbgen.Refund{}, errors.New("refund exceeds the remaining provider-paid amount")
	}
	receipt, _ := json.Marshal(receiptMetadata)
	// The durable record comes first. It also reserves the amount, so a second
	// refund opened while the provider is answering cannot over-refund.
	refund, err := queries.CreateRefund(ctx, dbgen.CreateRefundParams{PaymentIntentID: intent.ID, Status: "pending", AmountMinor: amount.Amount, Currency: amount.Currency, Reason: reason, IdempotencyKey: idempotencyKey, ReceiptMetadata: receipt})
	if err != nil {
		return dbgen.Refund{}, err
	}
	if refund.AmountMinor != amount.Amount || refund.Currency != amount.Currency {
		return dbgen.Refund{}, errors.New("idempotency key was already used with different refund parameters")
	}
	return service.askProvider(ctx, queries, provider, intent, refund, false)
}

// unanswered reports whether a refund was written down but the provider's
// answer never was.
func unanswered(refund dbgen.Refund) bool {
	return refund.Status == "pending" && !refund.ProviderReference.Valid
}

// askProvider sends the refund to the provider under its own idempotency key
// and records the answer. The caller holds the intent's refund lock.
//
// A definite refusal on the first request means nothing moved, and the refund
// is marked failed so its amount stops being reserved. Anything else — a
// timeout, an outage, or a refusal on a request that may be a repeat of one
// that already succeeded — leaves the refund waiting, to be asked about again;
// a revival that cannot get an answer tells an operator once.
func (service *Service) askProvider(ctx context.Context, queries *dbgen.Queries, provider payments.Provider, intent dbgen.PaymentIntent, refund dbgen.Refund, revival bool) (dbgen.Refund, error) {
	amount := commerce.Money{Amount: refund.AmountMinor, Currency: refund.Currency}
	remote, err := provider.Refund(ctx, payments.RefundRequest{PaymentReference: intent.ProviderReference.String, IdempotencyKey: refund.IdempotencyKey, Amount: amount, Reason: refund.Reason})
	if err != nil {
		if !revival && (payments.Refused(err) || errors.Is(err, commerce.ErrCurrencyMismatch)) {
			if _, recordErr := queries.RecordRefundOutcome(ctx, dbgen.RecordRefundOutcomeParams{RefundID: refund.ID, Status: "failed"}); recordErr != nil {
				return dbgen.Refund{}, errors.Join(err, recordErr)
			}
		} else if revival {
			service.notifyRefundUnanswered(ctx, queries, intent, refund)
		}
		return dbgen.Refund{}, err
	}
	recorded, err := queries.RecordRefundOutcome(ctx, dbgen.RecordRefundOutcomeParams{RefundID: refund.ID, Status: remote.Status, ProviderReference: optionalText(remote.ProviderReference)})
	if errors.Is(err, pgx.ErrNoRows) {
		// Another request recorded the answer first; read what it wrote.
		recorded, err = queries.GetRefundByIdempotency(ctx, dbgen.GetRefundByIdempotencyParams{PaymentIntentID: intent.ID, IdempotencyKey: refund.IdempotencyKey})
	}
	if err != nil {
		return dbgen.Refund{}, err
	}
	if recorded.Status == "succeeded" {
		if _, err = service.commerce.RecordRefund(ctx, uuidString(intent.ID), uuidString(recorded.ID), "refund:"+recorded.IdempotencyKey, amount); err != nil {
			return dbgen.Refund{}, err
		}
	}
	return recorded, nil
}

// notifyRefundUnanswered tells an operator that a refund's outcome at the
// provider is unknown and has to be checked there. It carries identifiers and
// the amount class only, and is deduplicated per refund.
func (service *Service) notifyRefundUnanswered(ctx context.Context, queries *dbgen.Queries, intent dbgen.PaymentIntent, refund dbgen.Refund) {
	payload, err := json.Marshal(map[string]any{
		"orderId": uuidString(intent.OrderID), "refundId": uuidString(refund.ID), "provider": intent.Provider,
		"currency": refund.Currency, "amountMinor": refund.AmountMinor, "status": "refund_outcome_unknown",
	})
	if err != nil {
		return
	}
	_, _ = queries.EnqueueOperatorNotification(ctx, dbgen.EnqueueOperatorNotificationParams{
		Kind: "incident", DedupeKey: "refund:" + uuidString(refund.ID) + ":unanswered", Payload: payload,
	})
}

// reviveRefunds asks the provider again about every refund whose answer was
// never recorded. It runs in the reconciler loop.
func (service *Service) reviveRefunds(ctx context.Context) {
	rows, err := dbgen.New(service.pool).ListUnansweredRefunds(ctx, refundRevivalBatch)
	if err != nil {
		return
	}
	for _, row := range rows {
		_, _ = service.ReviveRefund(ctx, uuidString(row.ID))
	}
}

// ReviveRefund resolves one refund recorded before its provider answered.
func (service *Service) ReviveRefund(ctx context.Context, refundID string) (dbgen.Refund, error) {
	id, err := parseUUID(refundID)
	if err != nil {
		return dbgen.Refund{}, err
	}
	connection, err := service.pool.Acquire(ctx)
	if err != nil {
		return dbgen.Refund{}, err
	}
	defer connection.Release()
	queries := dbgen.New(connection)
	refund, err := queries.GetRefund(ctx, id)
	if err != nil {
		return dbgen.Refund{}, err
	}
	lockKey := "refund:" + uuidString(refund.PaymentIntentID)
	if err = queries.AcquirePaymentMutationLock(ctx, lockKey); err != nil {
		return dbgen.Refund{}, err
	}
	defer func() { _ = queries.ReleasePaymentMutationLock(context.WithoutCancel(ctx), lockKey) }()
	// Read again under the lock: a request may have answered it meanwhile.
	if refund, err = queries.GetRefund(ctx, id); err != nil {
		return dbgen.Refund{}, err
	}
	if !unanswered(refund) {
		return refund, nil
	}
	intent, err := queries.GetPaymentIntent(ctx, refund.PaymentIntentID)
	if err != nil {
		return dbgen.Refund{}, err
	}
	provider, ok := service.providers[intent.Provider]
	if !ok || !intent.ProviderReference.Valid {
		service.notifyRefundUnanswered(ctx, queries, intent, refund)
		return dbgen.Refund{}, payments.ErrUnsupported
	}
	return service.askProvider(ctx, queries, provider, intent, refund, true)
}
