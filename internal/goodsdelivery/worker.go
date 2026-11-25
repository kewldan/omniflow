package goodsdelivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/goods"
	"github.com/omniflow/omniflow/internal/tonrate"
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

// RateSource prices the settlement asset in the shop currency.
type RateSource interface {
	Current(ctx context.Context) (tonrate.Rate, error)
}

// Options are the worker's optional collaborators.
type Options struct {
	// Rates fixes a TON-paid delivery's cost in the shop currency at the
	// moment it is delivered. Without it the cost stays in TON only.
	Rates RateSource
	// LowWalletStars is the operator's warning threshold: a sale notice
	// asks for a top-up when the wallet covers fewer Stars than this.
	LowWalletStars int
}

// Worker delivers one paid shop order.
type Worker struct {
	river.WorkerDefaults[JobArgs]

	pool      *pgxpool.Pool
	providers Providers
	logger    *slog.Logger
	clock     func() time.Time
	options   Options
}

// NewWorker builds the delivery worker.
func NewWorker(pool *pgxpool.Pool, providers Providers, logger *slog.Logger) *Worker {
	if logger == nil {
		logger = slog.Default()
	}
	return &Worker{pool: pool, providers: providers, logger: logger, clock: time.Now}
}

// Timeout bounds one delivery attempt.
//
// It is far above River's one-minute default on purpose. A direct Fragment
// purchase waits up to two minutes for the wallet to be seen spending, and the
// gateway may take ninety seconds to answer. A job cancelled while it waits
// does not undo the payment — it only turns a delivery that is about to
// confirm into one parked for review.
func (worker *Worker) Timeout(*river.Job[JobArgs]) time.Duration { return 6 * time.Minute }

// WithOptions attaches the optional collaborators.
func (worker *Worker) WithOptions(options Options) *Worker {
	worker.options = options
	return worker
}

// Work delivers an order, or records why it could not be.
//
// The shape is deliberate and is the whole double-delivery guard:
//
//  1. A short transaction takes the delivery row's lock, refuses anything that
//     is already resolved, and claims the next attempt — incrementing the
//     counter and pushing the retry deadline out *before* the provider is
//     called. That transaction commits.
//  2. The provider call happens outside any transaction. An adapter that pays
//     the provider itself commits a settlement marker, in a transaction of its
//     own, immediately before money leaves. A worker that dies after that
//     leaves a marked row, and the next claim parks it for review instead of
//     paying again.
//  3. A second transaction records the outcome.
//
// Doing the provider call inside the first transaction would hold a row lock
// across a network request that can take minutes; doing it before claiming the
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

	marked := &atomic.Bool{}
	claim.request.BeforeSettle = worker.beforeSettle(claim, marked)

	submission, err := worker.submit(ctx, claim)
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
	if marked.Load() && !claim.resumable && submission.Status != "delivered" &&
		!goods.NeedsReview(submission.FailureClass) && !submission.NotCharged &&
		(submission.Status != "submitted" || submission.Reference == "") {
		// Belt and braces: once the marker committed, money may have left, so
		// no outcome short of "delivered" may be retried or refunded, whatever
		// an adapter says. Three answers are not guesses and pass: an order
		// the provider accepted and gave a reference for, which is polled; a
		// refusal the provider made before taking any money, which lifts the
		// marker; and anything from an adapter whose marker names a provider
		// order it can read back.
		submission.FailureClass = goods.FailureAmbiguous
		submission.ErrorCode = "after_settlement_marker"
		submission.Status = "failed"
	}

	var rate *tonrate.Rate
	if submission.Status == "delivered" && submission.Settlement != nil &&
		submission.Settlement.CostNano > 0 && worker.options.Rates != nil {
		rateCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		if current, rateErr := worker.options.Rates.Current(rateCtx); rateErr == nil &&
			strings.EqualFold(current.Currency, claim.order.Currency) {
			rate = &current
		}
		cancel()
	}

	// The outcome is written even if the job's context ended: the provider has
	// answered, and losing that answer would turn a known result into an
	// ambiguous one at the next claim.
	return worker.record(context.WithoutCancel(ctx), claim, submission, rate)
}

// beforeSettle commits the settlement marker for an adapter that pays the
// provider itself. It only succeeds for the attempt this worker claimed and a
// delivery nobody has marked or cancelled since.
func (worker *Worker) beforeSettle(
	claim claimed, marked *atomic.Bool,
) func(context.Context, goods.SettlementMarker) error {
	return func(ctx context.Context, marker goods.SettlementMarker) error {
		tx, err := worker.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err = dbgen.New(tx).MarkGoodsDeliveryBroadcasting(ctx, dbgen.MarkGoodsDeliveryBroadcastingParams{
			OrderID: claim.orderID, Attempt: claim.attempt,
			ProviderRequestID:  optionalText(marker.ProviderRequestID),
			SettlementAsset:    optionalText(marker.Asset),
			SettlementCostNano: pgtype.Int8{Int64: marker.CostNano, Valid: true},
		}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return errors.New("the delivery is no longer this attempt's to pay for")
			}
			return err
		}
		if err = tx.Commit(ctx); err != nil {
			return err
		}
		marked.Store(true)
		return nil
	}
}

// submit either asks the provider what happened or asks it to deliver.
//
// A row that already carries a provider reference has been submitted: the
// provider took the request and has not finished. Asking again would be asking
// for a second purchase, so this polls instead — which is what "delayed
// delivery" means in practice.
//
// An adapter that cannot be polled leaves the outcome genuinely unknown, and
// unknown is not a failure to retry or a success to record. It becomes an
// ambiguous outcome, which parks the delivery for an operator rather than
// guessing on the customer's money.
func (worker *Worker) submit(ctx context.Context, claim claimed) (goods.Delivery, error) {
	if claim.reference == "" {
		return claim.provider.Deliver(ctx, claim.request)
	}
	polled, err := claim.provider.Poll(ctx, claim.reference)
	if errors.Is(err, goods.ErrUnsupported) {
		return goods.Delivery{
			Status: "failed", FailureClass: goods.FailureAmbiguous, ErrorCode: "poll_unsupported",
			Reference: claim.reference,
		}, nil
	}
	if err != nil {
		return goods.Delivery{}, err
	}
	if polled.Reference == "" {
		polled.Reference = claim.reference
	}
	return polled, nil
}

// claimed carries what the first transaction resolved.
type claimed struct {
	done         bool
	attempt      int32
	providerSlug string
	provider     goods.Provider
	request      goods.DeliveryRequest
	order        dbgen.GoodsOrder
	orderID      pgtype.UUID
	stars        int
	kind         string
	// resumable is true for an adapter whose settlement marker names a
	// provider order that can be read back and paid again without buying
	// twice.
	resumable bool
	// reference is what the provider returned on an earlier attempt. Its
	// presence is what distinguishes "submit this" from "ask what happened to
	// the one already submitted".
	reference string
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
	// A settlement marker on a delivery nobody finished means a worker paid,
	// or was about to pay, and never recorded the answer. Paying again could
	// buy twice, so it parks for an operator who can check the wallet. This
	// runs before the attempt ceiling for the same reason: an exhausted
	// delivery that may have been paid for must not be refunded either.
	//
	// Two cases are not interrupted: a delivery the provider already accepted
	// and gave a reference for, which is polled like any submission, and one
	// from an adapter whose marker names the provider's own order, which it
	// can ask about and pay again without opening a second one.
	resumable := resumesAfterMarker(delivery.ProviderSlug)
	reference := ""
	if delivery.ProviderReference.Valid {
		reference = delivery.ProviderReference.String
	}
	if delivery.BroadcastingAt.Valid && reference == "" {
		if !resumable {
			return claimed{done: true}, worker.parkInterrupted(ctx, queries, tx, delivery)
		}
		resumed, resumeErr := resumeReference(delivery)
		if resumeErr != nil {
			return claimed{done: true}, worker.parkInterrupted(ctx, queries, tx, delivery)
		}
		reference = resumed
	}
	// The provider-facing ceiling. River may retry the job more often than
	// this, so refusing here is what stops a generous job budget becoming extra
	// purchases. A delivery the provider is still working on is parked rather
	// than refunded: the goods may yet arrive.
	if int(delivery.AttemptCount) >= goods.MaxAttempts {
		if reference != "" {
			return claimed{done: true}, worker.parkStillRunning(ctx, queries, tx, delivery)
		}
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

	stars := OrderStars(order, product)
	request := goods.DeliveryRequest{
		Request: goods.Request{
			Kind:           product.Kind,
			DurationMonths: int(product.DurationMonths.Int16),
			StarQuantity:   stars,
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
		request: request, order: order, orderID: id, stars: stars, reference: reference,
		kind: product.Kind, resumable: resumable,
	}, nil
}

// OrderStars is how many Stars an order delivers. New orders record it; an
// order written before the column existed falls back to the product's pack
// times the quantity.
func OrderStars(order dbgen.GoodsOrder, product dbgen.GoodsProduct) int {
	if order.StarQuantity.Valid {
		return int(order.StarQuantity.Int32)
	}
	if product.StarQuantity.Valid {
		return int(product.StarQuantity.Int32) * int(max(order.Quantity, 1))
	}
	return 0
}

// parkInterrupted parks a delivery whose payment marker was committed by an
// attempt that never recorded its outcome.
func (worker *Worker) parkInterrupted(
	ctx context.Context, queries *dbgen.Queries, tx pgx.Tx, delivery dbgen.GoodsDelivery,
) error {
	if _, err := queries.ParkGoodsDelivery(ctx, dbgen.ParkGoodsDeliveryParams{
		OrderID: delivery.OrderID, LastErrorCode: optionalText("interrupted_after_marker"),
	}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if _, err := queries.SetGoodsOrderStatus(ctx, dbgen.SetGoodsOrderStatusParams{
		OrderID: delivery.OrderID, Status: "needs_review",
	}); err != nil {
		return err
	}
	if err := worker.notify(ctx, queries, "fulfillment_failure",
		"goods-review:"+uuidString(delivery.OrderID), map[string]any{
			"orderId": uuidString(delivery.OrderID), "reason": "interrupted_after_marker",
			"provider": delivery.ProviderSlug, "status": "needs_review",
		}); err != nil {
		return err
	}
	worker.logger.Error("digital goods delivery interrupted after its payment marker; parked for review",
		"orderId", uuidString(delivery.OrderID), "provider", delivery.ProviderSlug)
	return tx.Commit(ctx)
}

// parkStillRunning parks a delivery the provider accepted and has not
// finished after every attempt. Refunding it could return money for goods
// that still arrive, so an operator checks the provider's order instead.
func (worker *Worker) parkStillRunning(
	ctx context.Context, queries *dbgen.Queries, tx pgx.Tx, delivery dbgen.GoodsDelivery,
) error {
	if _, err := queries.ParkGoodsDelivery(ctx, dbgen.ParkGoodsDeliveryParams{
		OrderID: delivery.OrderID, LastErrorCode: optionalText("provider_still_processing"),
	}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if _, err := queries.SetGoodsOrderStatus(ctx, dbgen.SetGoodsOrderStatusParams{
		OrderID: delivery.OrderID, Status: "needs_review",
	}); err != nil {
		return err
	}
	if err := worker.notify(ctx, queries, "fulfillment_failure",
		"goods-review:"+uuidString(delivery.OrderID), map[string]any{
			"orderId": uuidString(delivery.OrderID), "reason": "provider_still_processing",
			"provider": delivery.ProviderSlug, "status": "needs_review",
		}); err != nil {
		return err
	}
	return tx.Commit(ctx)
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
	if err = Refund(ctx, queries, order, "digital goods delivery failed"); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// record writes the outcome of one provider submission.
func (worker *Worker) record(
	ctx context.Context, claim claimed, submission goods.Delivery, rate *tonrate.Rate,
) error {
	tx, err := worker.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := dbgen.New(tx)

	current, err := queries.LockGoodsDelivery(ctx, claim.orderID)
	if err != nil {
		return err
	}
	if current.Status != "submitted" && current.Status != "pending" {
		// An operator resolved the delivery while the provider was being
		// asked — refunded it, most likely. Their decision stands; this
		// attempt's answer is kept in the attempt history and nothing else is
		// changed. A provider that delivered anyway is told to the operator,
		// because the customer now has both the goods and the refund.
		if _, err = queries.InsertGoodsDeliveryAttempt(ctx, dbgen.InsertGoodsDeliveryAttemptParams{
			OrderID: claim.orderID, Attempt: claim.attempt, Outcome: attemptOutcome(submission),
			FailureClass:      optionalText(submission.FailureClass),
			ErrorCode:         optionalText(submission.ErrorCode),
			ProviderReference: optionalText(submission.Reference),
			CorrelationID:     fmt.Sprintf("goods:%s:%d", uuidString(claim.orderID), claim.attempt),
		}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if submission.Status == "delivered" && current.Status != "delivered" {
			if err = worker.notify(ctx, queries, "incident", "goods-late-delivery:"+uuidString(claim.orderID),
				map[string]any{
					"orderId": uuidString(claim.orderID), "provider": claim.providerSlug,
					"status": "delivered_after_" + current.Status, "reason": "operator_resolved_first",
				}); err != nil {
				return err
			}
		}
		return tx.Commit(ctx)
	}

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
	if submission.Settlement != nil {
		if err = worker.recordSettlement(ctx, queries, claim, submission, rate); err != nil {
			return err
		}
	}
	if submission.NotCharged && submission.Status == "failed" {
		// The provider refused before taking any money, so the marker this
		// attempt committed no longer means "money may have left".
		if _, err = queries.LiftGoodsDeliveryMarker(ctx, claim.orderID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
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
		if err = worker.notify(ctx, queries, "purchase", "goods-sale:"+uuidString(claim.orderID),
			worker.salePayload(claim, submission, rate)); err != nil {
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
				"orderId": uuidString(claim.orderID), "status": "needs_review",
				"reason": "ambiguous_delivery", "errorCode": submission.ErrorCode,
				"provider": claim.providerSlug,
			}); err != nil {
			return err
		}

	case submission.Status == "submitted" && submission.Reference != "" &&
		int(claim.attempt) >= goods.MaxAttempts:
		// Accepted by the provider and still running after every attempt.
		// Refunding could pay the customer back for goods that still arrive,
		// so an operator looks at the provider's order instead.
		if _, err = queries.ParkGoodsDelivery(ctx, dbgen.ParkGoodsDeliveryParams{
			OrderID: claim.orderID, LastErrorCode: optionalText("provider_still_processing"),
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
				"orderId": uuidString(claim.orderID), "status": "needs_review",
				"reason": "provider_still_processing", "errorCode": submission.ErrorCode,
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
		if err = Refund(ctx, queries, order, "digital goods delivery failed"); err != nil {
			return err
		}
		if err = worker.notify(ctx, queries, "refund", "goods-refund:"+uuidString(claim.orderID),
			map[string]any{
				"orderId": uuidString(claim.orderID), "provider": claim.providerSlug,
				"reason": submission.FailureClass, "errorCode": submission.ErrorCode,
				"currency": order.Currency, "amountMinor": order.QuotedPriceMinor - order.DiscountMinor,
				"status": "refunded",
			}); err != nil {
			return err
		}

	default:
		// Retryable, or accepted and still running: leave it in the queue with
		// the deadline the claim already pushed out. A submission the provider
		// accepted keeps its reference, so the next attempt asks about it
		// rather than buying again.
		reference := ""
		if submission.Status == "submitted" {
			reference = submission.Reference
		}
		if _, err = queries.FailGoodsDelivery(ctx, dbgen.FailGoodsDeliveryParams{
			OrderID: claim.orderID, Terminal: false,
			FailureClass:      optionalText(submission.FailureClass),
			LastErrorCode:     optionalText(submission.ErrorCode),
			ProviderReference: optionalText(reference),
			Backoff:           interval(goods.Backoff(int(claim.attempt) + 1)),
		}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if submission.FailureClass == goods.FailureProviderBalance {
			if err = worker.notify(ctx, queries, "incident",
				fmt.Sprintf("goods-balance:%s:%d", claim.providerSlug, worker.clock().UTC().Unix()/3600),
				map[string]any{
					"provider": claim.providerSlug, "status": "provider_balance_low",
					"errorCode": submission.ErrorCode, "orderId": uuidString(claim.orderID),
				}); err != nil {
				return err
			}
		}
	}

	return tx.Commit(ctx)
}

// recordSettlement stores what the operator paid. A delivered settlement's
// cost is fixed in the shop currency at this minute's rate, so the margin on
// the order never moves with TON afterwards.
func (worker *Worker) recordSettlement(
	ctx context.Context, queries *dbgen.Queries, claim claimed, submission goods.Delivery, rate *tonrate.Rate,
) error {
	settlement := submission.Settlement
	params := dbgen.RecordGoodsSettlementParams{
		OrderID:            claim.orderID,
		ProviderRequestID:  optionalText(settlement.ProviderRequestID),
		SettlementAsset:    optionalText(settlement.Asset),
		SettlementCostNano: pgtype.Int8{Int64: settlement.CostNano, Valid: settlement.Asset != ""},
		SettlementTxHash:   optionalText(settlement.TxHash),
		DryRun:             settlement.DryRun,
	}
	if submission.Status == "delivered" {
		params.ProviderConfirmed = pgtype.Bool{Bool: settlement.ProviderConfirmed, Valid: true}
		switch {
		case settlement.CostNano == 0:
			// A dry run costs nothing, and saying so keeps its margin known.
			params.SettledCostMinor = pgtype.Int8{Int64: 0, Valid: true}
		case rate != nil:
			params.SettledCostMinor = pgtype.Int8{Int64: rate.MinorFromNano(settlement.CostNano), Valid: true}
			params.SettledRateMinor = pgtype.Int8{Int64: rate.MinorPerTON, Valid: true}
			params.SettledRateSource = optionalText(rate.Source)
		}
	}
	_, err := queries.RecordGoodsSettlement(ctx, params)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	return err
}

// salePayload is the operator's sale notice: who bought what for whom, what
// it earned, and what is left in the wallet. It carries identifiers and
// amounts, never customer text.
func (worker *Worker) salePayload(claim claimed, submission goods.Delivery, rate *tonrate.Rate) map[string]any {
	order := claim.order
	paid := order.QuotedPriceMinor - order.DiscountMinor
	payload := map[string]any{
		"event":           "goods_sale",
		"kind":            claim.kind,
		"orderId":         uuidString(claim.orderID),
		"customerId":      uuidString(order.UserID),
		"provider":        claim.providerSlug,
		"recipient":       order.RecipientUsername,
		"recipientIsSelf": order.RecipientIsSelf,
		"stars":           claim.stars,
		"amountMinor":     paid,
		"currency":        order.Currency,
		"source":          order.Source,
		"attempt":         claim.attempt,
	}
	if claim.request.BuyerTelegramID != 0 {
		payload["buyerTelegramId"] = claim.request.BuyerTelegramID
	}
	if claim.stars > 0 {
		payload["pricePerStarMinor"] = paid / int64(claim.stars)
	}
	if order.CreatedAt.Valid {
		payload["deliverySeconds"] = int64(worker.clock().Sub(order.CreatedAt.Time).Seconds())
	}
	settlement := submission.Settlement
	if settlement == nil {
		if order.CostKnown {
			payload["costMinor"] = order.QuotedCostMinor
			payload["marginMinor"] = paid - order.QuotedCostMinor
		}
		return payload
	}
	payload["dryRun"] = settlement.DryRun
	payload["providerConfirmed"] = settlement.ProviderConfirmed
	payload["txHash"] = settlement.TxHash
	payload["costNano"] = settlement.CostNano
	if rate != nil {
		cost := rate.MinorFromNano(settlement.CostNano)
		payload["costMinor"] = cost
		payload["marginMinor"] = paid - cost
		payload["rateMinor"] = rate.MinorPerTON
		payload["rateSource"] = rate.Source
	} else if settlement.CostNano == 0 {
		payload["costMinor"] = int64(0)
		payload["marginMinor"] = paid
	}
	if settlement.BalanceAfterNano != nil {
		balance := *settlement.BalanceAfterNano
		payload["walletBalanceNano"] = balance
		if rate != nil {
			payload["walletBalanceMinor"] = rate.MinorFromNano(balance)
		}
		if settlement.CostNano > 0 && claim.stars > 0 {
			covered := balance * int64(claim.stars) / settlement.CostNano
			payload["walletStarsCovered"] = covered
			payload["walletLowStars"] = worker.options.LowWalletStars
			payload["walletLow"] = covered < int64(worker.options.LowWalletStars)
		}
	}
	if reporter, ok := claim.provider.(goods.WalletReporter); ok && !settlement.DryRun {
		// The address is derived from the key, so reading it costs nothing
		// and needs no network; the balance came back with the payment.
		if addresser, ok := reporter.(interface{ WalletAddress() string }); ok {
			payload["walletAddress"] = addresser.WalletAddress()
		}
	}
	return payload
}

// Refund credits the customer's wallet for a delivery that will not happen.
//
// It is an ordinary ledger transaction, so the refund appears in the customer's
// wallet history exactly like any other, and the idempotency key is derived
// from the order so a replayed failure — or an operator's refund racing the
// worker's — credits once.
//
// The amount is what the customer paid: the quoted price less any discount a
// promotion gave. Refunding the undiscounted price would hand back money that
// was never taken.
func Refund(ctx context.Context, queries *dbgen.Queries, order dbgen.GoodsOrder, reason string) error {
	amount := order.QuotedPriceMinor - order.DiscountMinor
	orderID := uuidString(order.OrderID)
	if amount > 0 {
		transaction, err := queries.CreateLedgerTransaction(ctx, dbgen.CreateLedgerTransactionParams{
			Type: "refund", ReferenceType: "goods_order", ReferenceID: orderID,
			IdempotencyKey: "goods-refund:" + orderID,
			Reason:         optionalText(reason),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			// Already refunded. The unique idempotency key is what makes a
			// replayed failure credit exactly once.
			return nil
		}
		if err != nil {
			return err
		}
		if _, err = queries.InsertLedgerEntry(ctx, dbgen.InsertLedgerEntryParams{
			TransactionID: transaction.ID, AccountType: "customer_wallet",
			UserID: order.UserID, Currency: order.Currency, AmountMinor: amount,
		}); err != nil {
			return err
		}
		if _, err = queries.InsertLedgerEntry(ctx, dbgen.InsertLedgerEntryParams{
			TransactionID: transaction.ID, AccountType: "platform_clearing",
			Currency: order.Currency, AmountMinor: -amount,
		}); err != nil {
			return err
		}
		if _, err = queries.RecordGoodsDeliveryRefund(ctx, dbgen.RecordGoodsDeliveryRefundParams{
			OrderID: order.OrderID, LedgerTransactionID: transaction.ID,
		}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	if _, err := queries.SetGoodsOrderStatus(ctx, dbgen.SetGoodsOrderStatusParams{
		OrderID: order.OrderID, Status: "refunded",
	}); err != nil {
		return err
	}
	return nil
}

// notify queues an operator notice. The payload names the condition and the
// order; the sale notice adds the recipient handle, which is the one recipient
// detail Omniflow keeps.
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

func attemptOutcome(submission goods.Delivery) string {
	switch submission.Status {
	case "delivered", "submitted":
		return submission.Status
	default:
		return "failed"
	}
}
