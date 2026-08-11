package goodsdelivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/goods"
	"github.com/riverqueue/river"
)

// Providers resolves a product's provider slug to an adapter.
//
// It is an interface rather than a map so the credential can be read and
// decrypted at call time: a gateway token an operator rotates in the panel
// takes effect on the next delivery rather than at the next restart.
type Providers interface {
	Provider(ctx context.Context, slug string) (goods.Provider, error)
}

// Worker delivers one paid shop order.
type Worker struct {
	river.WorkerDefaults[JobArgs]

	pool      *pgxpool.Pool
	providers Providers
	logger    *slog.Logger
	clock     func() time.Time
}

// NewWorker builds the delivery worker.
func NewWorker(pool *pgxpool.Pool, providers Providers, logger *slog.Logger) *Worker {
	return &Worker{pool: pool, providers: providers, logger: logger, clock: time.Now}
}

// Work delivers an order, or records why it could not be.
//
// The shape is deliberate and is the whole double-delivery guard:
//
//  1. A short transaction takes the delivery row's lock, refuses anything that
//     is already resolved, and claims the next attempt — incrementing the
//     counter and pushing the retry deadline out *before* the provider is
//     called. That transaction commits.
//  2. The provider call happens outside any transaction. A worker that dies
//     here leaves a row that is already marked submitted with its deadline
//     moved, so nothing picks it up immediately and re-buys.
//  3. A second transaction records the outcome.
//
// Doing the provider call inside the first transaction would hold a row lock
// across a network request that can take a minute; doing it before claiming the
// attempt would let two workers submit the same purchase.
func (worker *Worker) Work(ctx context.Context, job *river.Job[JobArgs]) error {
	claim, err := worker.claim(ctx, job.Args.OrderID)
	if err != nil {
		return err
	}
	if claim.done {
		// Already delivered, refunded, cancelled, or parked for review. A
		// replayed job is a no-op, which is what makes the enqueue safe to
		// repeat.
		return nil
	}

	submission, err := claim.provider.Deliver(ctx, claim.request)
	if err != nil {
		// An adapter that returns an error rather than a classified outcome has
		// told us nothing about whether the purchase happened. That is the same
		// unknown as a lost connection.
		worker.logger.Warn("digital goods adapter failed",
			"orderId", job.Args.OrderID, "provider", claim.providerSlug, "error", err)
		submission = goods.Delivery{
			Status: "failed", FailureClass: goods.FailureAmbiguous, ErrorCode: "adapter_error",
		}
	}

	return worker.record(ctx, claim, submission)
}

// claimed carries what the first transaction resolved.
type claimed struct {
	done         bool
	attempt      int32
	providerSlug string
	provider     goods.Provider
	request      goods.DeliveryRequest
	currency     string
	priceMinor   int64
	customerID   pgtype.UUID
	orderID      pgtype.UUID
}

// claim locks the delivery, decides whether it may be submitted, and takes the
// next attempt.
func (worker *Worker) claim(ctx context.Context, orderID string) (claimed, error) {
	id, err := parseUUID(orderID)
	if err != nil {
		return claimed{}, err
	}

	tx, err := worker.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return claimed{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := dbgen.New(tx)

	delivery, err := queries.LockGoodsDelivery(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		// No delivery row means the order was never settled. Nothing to do, and
		// certainly nothing to buy.
		return claimed{done: true}, nil
	}
	if err != nil {
		return claimed{}, err
	}

	switch delivery.Status {
	case "delivered", "failed", "cancelled", "needs_review":
		return claimed{done: true}, nil
	}
	// The provider-facing ceiling. River may retry the job more often than
	// this, so refusing here is what stops a generous job budget becoming extra
	// purchases.
	if int(delivery.AttemptCount) >= goods.MaxAttempts {
		return claimed{done: true}, worker.exhaust(ctx, queries, tx, id)
	}

	order, err := queries.GetGoodsOrder(ctx, id)
	if err != nil {
		return claimed{}, err
	}
	product, err := queries.GetGoodsProduct(ctx, order.ProductID)
	if err != nil {
		return claimed{}, err
	}
	buyer, err := queries.GetRemnawaveMappingByCustomer(ctx, order.UserID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return claimed{}, err
	}

	provider, err := worker.providers.Provider(ctx, product.ProviderSlug)
	if err != nil {
		// A provider that cannot be constructed — no credential, disabled — is
		// an operator configuration problem, not a customer one. Retry rather
		// than refund; a corrected credential lets the delivery through.
		return claimed{done: true}, worker.defer_(ctx, queries, tx, id, "provider_unconfigured")
	}

	attempt := delivery.AttemptCount + 1
	if _, err = queries.BeginGoodsDeliveryAttempt(ctx, dbgen.BeginGoodsDeliveryAttemptParams{
		OrderID: id, Backoff: interval(goods.Backoff(int(attempt))),
	}); err != nil {
		return claimed{}, err
	}
	if _, err = queries.SetGoodsOrderStatus(ctx, dbgen.SetGoodsOrderStatusParams{
		OrderID: id, Status: "delivering",
	}); err != nil {
		return claimed{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return claimed{}, err
	}

	request := goods.DeliveryRequest{
		Request: goods.Request{
			Kind:           product.Kind,
			DurationMonths: int(product.DurationMonths.Int16),
			StarQuantity:   int(product.StarQuantity.Int32),
			Quantity:       int(order.Quantity),
			Recipient:      order.RecipientUsername,
			Currency:       order.Currency,
		},
		OrderID: orderID,
		// Stable across retries, so a provider that does honour it delivers
		// once however many times this runs.
		IdempotencyKey:  "goods:" + orderID,
		BuyerTelegramID: buyer.TelegramID.Int64,
	}
	return claimed{
		attempt: attempt, providerSlug: product.ProviderSlug, provider: provider,
		request: request, currency: order.Currency, priceMinor: order.QuotedPriceMinor,
		customerID: order.UserID, orderID: id,
	}, nil
}

// defer_ pushes a delivery out without consuming a provider attempt, for a
// condition that is the operator's to fix rather than the provider's to fail.
func (worker *Worker) defer_(
	ctx context.Context, queries *dbgen.Queries, tx pgx.Tx, orderID pgtype.UUID, code string,
) error {
	if _, err := queries.FailGoodsDelivery(ctx, dbgen.FailGoodsDeliveryParams{
		OrderID: orderID, Terminal: false,
		FailureClass: optionalText(goods.FailureProviderUnavailable), LastErrorCode: optionalText(code),
		Backoff: interval(goods.Backoff(1)),
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// exhaust ends a delivery that has used every provider attempt, and refunds.
func (worker *Worker) exhaust(
	ctx context.Context, queries *dbgen.Queries, tx pgx.Tx, orderID pgtype.UUID,
) error {
	order, err := queries.GetGoodsOrder(ctx, orderID)
	if err != nil {
		return err
	}
	if _, err = queries.FailGoodsDelivery(ctx, dbgen.FailGoodsDeliveryParams{
		OrderID: orderID, Terminal: true,
		FailureClass: optionalText(goods.FailureRetryable), LastErrorCode: optionalText("attempts_exhausted"),
		Backoff: interval(time.Hour),
	}); err != nil {
		return err
	}
	if err = worker.refund(ctx, queries, order); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// record writes the outcome of one provider submission.
func (worker *Worker) record(ctx context.Context, claim claimed, submission goods.Delivery) error {
	tx, err := worker.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := dbgen.New(tx)

	outcome := "failed"
	if submission.Status == "delivered" {
		outcome = "delivered"
	} else if submission.Status == "submitted" {
		outcome = "submitted"
	}
	if _, err = queries.InsertGoodsDeliveryAttempt(ctx, dbgen.InsertGoodsDeliveryAttemptParams{
		OrderID: claim.orderID, Attempt: claim.attempt, Outcome: outcome,
		FailureClass: optionalText(submission.FailureClass),
		ErrorCode:    optionalText(submission.ErrorCode),
		// The correlation identifier ties the attempt to this exact submission
		// without carrying anything the provider returned.
		ProviderReference: optionalText(submission.Reference),
		CorrelationID:     fmt.Sprintf("goods:%s:%d", uuidString(claim.orderID), claim.attempt),
	}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}

	switch {
	case submission.Status == "delivered":
		if _, err = queries.CompleteGoodsDelivery(ctx, dbgen.CompleteGoodsDeliveryParams{
			OrderID: claim.orderID, ProviderReference: optionalText(submission.Reference),
		}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if _, err = queries.SetGoodsOrderStatus(ctx, dbgen.SetGoodsOrderStatusParams{
			OrderID: claim.orderID, Status: "delivered",
		}); err != nil {
			return err
		}

	case goods.NeedsReview(submission.FailureClass):
		// The purchase may already have spent the operator's funds. Retrying
		// could buy twice and refunding could give money back for goods the
		// recipient received, so it stops here for a person.
		if _, err = queries.ParkGoodsDelivery(ctx, dbgen.ParkGoodsDeliveryParams{
			OrderID: claim.orderID, LastErrorCode: optionalText(submission.ErrorCode),
		}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if _, err = queries.SetGoodsOrderStatus(ctx, dbgen.SetGoodsOrderStatusParams{
			OrderID: claim.orderID, Status: "needs_review",
		}); err != nil {
			return err
		}
		if err = worker.notify(ctx, queries, "fulfillment_failure",
			"goods-review:"+uuidString(claim.orderID), map[string]any{
				"reason": "ambiguous_delivery", "errorCode": submission.ErrorCode,
				"provider": claim.providerSlug,
			}); err != nil {
			return err
		}

	case goods.Refundable(submission.FailureClass), int(claim.attempt) >= goods.MaxAttempts:
		if _, err = queries.FailGoodsDelivery(ctx, dbgen.FailGoodsDeliveryParams{
			OrderID: claim.orderID, Terminal: true,
			FailureClass:  optionalText(submission.FailureClass),
			LastErrorCode: optionalText(submission.ErrorCode),
			Backoff:       interval(time.Hour),
		}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		order, orderErr := queries.GetGoodsOrder(ctx, claim.orderID)
		if orderErr != nil {
			return orderErr
		}
		if err = worker.refund(ctx, queries, order); err != nil {
			return err
		}

	default:
		// Retryable: leave it in the queue with the deadline the claim already
		// pushed out.
		if _, err = queries.FailGoodsDelivery(ctx, dbgen.FailGoodsDeliveryParams{
			OrderID: claim.orderID, Terminal: false,
			FailureClass:  optionalText(submission.FailureClass),
			LastErrorCode: optionalText(submission.ErrorCode),
			Backoff:       interval(goods.Backoff(int(claim.attempt) + 1)),
		}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}

	return tx.Commit(ctx)
}

// refund credits the customer's wallet for a delivery that will not happen.
//
// It is an ordinary ledger transaction, so the refund appears in the customer's
// wallet history exactly like any other, and the idempotency key is derived
// from the order so a replayed failure credits once.
func (worker *Worker) refund(ctx context.Context, queries *dbgen.Queries, order dbgen.GoodsOrder) error {
	if order.QuotedPriceMinor <= 0 {
		return nil
	}
	orderID := uuidString(order.OrderID)

	transaction, err := queries.CreateLedgerTransaction(ctx, dbgen.CreateLedgerTransactionParams{
		Type: "refund", ReferenceType: "goods_order", ReferenceID: orderID,
		IdempotencyKey: "goods-refund:" + orderID,
		Reason:         optionalText("digital goods delivery failed"),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// Already refunded. The unique idempotency key is what makes a replayed
		// failure credit exactly once.
		return nil
	}
	if err != nil {
		return err
	}

	if _, err = queries.InsertLedgerEntry(ctx, dbgen.InsertLedgerEntryParams{
		TransactionID: transaction.ID, AccountType: "customer_wallet",
		UserID: order.UserID, Currency: order.Currency, AmountMinor: order.QuotedPriceMinor,
	}); err != nil {
		return err
	}
	if _, err = queries.InsertLedgerEntry(ctx, dbgen.InsertLedgerEntryParams{
		TransactionID: transaction.ID, AccountType: "platform_clearing",
		Currency: order.Currency, AmountMinor: -order.QuotedPriceMinor,
	}); err != nil {
		return err
	}
	if _, err = queries.RecordGoodsDeliveryRefund(ctx, dbgen.RecordGoodsDeliveryRefundParams{
		OrderID: order.OrderID, LedgerTransactionID: transaction.ID,
	}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if _, err = queries.SetGoodsOrderStatus(ctx, dbgen.SetGoodsOrderStatusParams{
		OrderID: order.OrderID, Status: "refunded",
	}); err != nil {
		return err
	}
	return nil
}

// notify queues an operator notice. The payload names the condition and the
// order, never the recipient or the customer.
func (worker *Worker) notify(
	ctx context.Context, queries *dbgen.Queries, kind, dedupe string, payload map[string]any,
) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = queries.EnqueueOperatorNotification(ctx, dbgen.EnqueueOperatorNotificationParams{
		Kind: kind, DedupeKey: dedupe, Payload: encoded,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	return err
}

func parseUUID(value string) (pgtype.UUID, error) {
	var id pgtype.UUID
	if err := id.Scan(strings.TrimSpace(value)); err != nil {
		return pgtype.UUID{}, errors.New("malformed order identifier")
	}
	return id, nil
}

func uuidString(id pgtype.UUID) string {
	if !id.Valid {
		return ""
	}
	value, err := id.Value()
	if err != nil {
		return ""
	}
	text, _ := value.(string)
	return text
}

func optionalText(value string) pgtype.Text {
	if strings.TrimSpace(value) == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: value, Valid: true}
}

func interval(duration time.Duration) pgtype.Interval {
	return pgtype.Interval{Microseconds: duration.Microseconds(), Valid: true}
}
