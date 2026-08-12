package accountshop

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/accountpg"
	"github.com/omniflow/omniflow/internal/goods"
)

// DeliveryState is the single customer-visible state of one shop purchase.
//
// It describes the goods rather than the payment, because "paid" tells a
// customer nothing about whether their Stars arrived. The vocabulary is closed
// and every value has a distinct next move for the person reading it, which is
// why "delayed" and "polling" are not folded together and why the ambiguous
// outcome has a state of its own instead of being reported as a failure.
type DeliveryState string

const (
	// StateAwaitingPayment is an order that exists but has not been paid, so
	// nothing has been submitted to any provider.
	StateAwaitingPayment DeliveryState = "awaiting_payment"
	// StateQueued is paid and waiting for the delivery worker's first attempt.
	StateQueued DeliveryState = "queued"
	// StateSubmitted means the gateway has been given the purchase and has not
	// answered yet.
	StateSubmitted DeliveryState = "submitted"
	// StatePolling means the gateway took the purchase and returned a reference,
	// and Omniflow is asking what became of it. It is never a second submission:
	// asking again for goods already submitted would be asking to buy twice.
	StatePolling DeliveryState = "polling"
	// StateDelayed is a transient provider failure being retried on the domain's
	// backoff schedule. The purchase is still expected to complete.
	StateDelayed DeliveryState = "delayed"
	// StateDelivered is the end of a successful purchase.
	StateDelivered DeliveryState = "delivered"
	// StateNeedsReview is a delivery parked for an operator because nobody can
	// safely say whether it happened.
	//
	// The gateway that fronts Fragment honours no idempotency key, so a lost
	// answer is a genuine unknown: retrying could buy twice and refunding could
	// give money back for goods the recipient already has. This state therefore
	// carries a support handoff and never a retry control, because a retry
	// button here is an offer to pay twice.
	StateNeedsReview DeliveryState = "needs_review"
	// StateRefunded is a permanent failure that has been made good: the money is
	// back in the customer's wallet through the ordinary ledger, which is the
	// only refund path there is.
	StateRefunded DeliveryState = "refunded"
	// StateFailed is a terminal failure whose refund has not landed. It is a
	// support case rather than a resting state, and it is reported honestly
	// instead of being shown as refunded before the credit exists.
	StateFailed DeliveryState = "failed"
	// StateCancelled is a purchase that will not be delivered and was never
	// charged — an abandoned order, or a delivery an operator stopped.
	StateCancelled DeliveryState = "cancelled"
)

// Refund is the wallet credit that made a failed delivery good.
//
// The amount is read from the ledger entry that actually landed rather than
// recomputed from the order, so what the customer is told they got back is what
// their balance actually received.
type Refund struct {
	Refunded    bool
	AmountMinor int64
	Currency    string
}

// Delivery is the honest state of the goods for one order.
type Delivery struct {
	State DeliveryState
	// FailureReason is the domain's failure classification for a terminal state,
	// empty otherwise. It is the class, never the provider's message: the class
	// is what decided the outcome, and a customer reading "recipient_invalid"
	// learns something they can act on where a gateway error string would tell
	// them nothing.
	FailureReason string
	Attempts      int
	// SupportHandoff marks a state a customer cannot resolve alone. The panel
	// offers a route to support there, and nothing else.
	SupportHandoff bool
	// SupportReference names this order in a form a ticket can carry without
	// repeating the recipient.
	SupportReference string
	Refund           Refund
	DeliveredAt      time.Time
	UpdatedAt        time.Time
}

// Order is one shop purchase as the customer sees it.
type Order struct {
	ID             string
	ProductName    string
	Kind           string
	DurationMonths int
	StarQuantity   int
	Quantity       int
	// Recipient is the handle the goods were addressed to. The customer's own
	// order is the one place it is shown, because "where did it go" is a
	// question only they and support need answered.
	Recipient       string
	RecipientIsSelf bool
	PriceMinor      int64
	DiscountMinor   int64
	WalletMinor     int64
	ExternalMinor   int64
	PaidMinor       int64
	Currency        string
	// PaymentState is the order's own state, kept separate from the delivery
	// state so a screen can say "paid, delivering" rather than having to choose
	// which of the two truths to show.
	PaymentState string
	// PaymentRequired reports an outstanding balance the customer still has to
	// settle through the checkout surface. The shop does not select a provider
	// or open an intent; that is one flow for every kind of order, and having a
	// second copy of it here is how two checkouts drift apart.
	PaymentRequired bool
	// PaymentPossible is false when money is owed and no configured provider can
	// settle this currency, so the panel says so instead of offering a payment
	// step that dead-ends.
	PaymentPossible bool
	Delivery        Delivery
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Cursor is a position in the customer's history.
//
// It is the created-at timestamp plus the order identifier, because two orders
// can share a millisecond and a cursor that cannot break that tie either repeats
// a row or skips one.
type Cursor struct {
	CreatedAt time.Time
	ID        string
}

// Page is one page of history.
type Page struct {
	Items []Order
	// Next is set only when another page exists, so the panel shows a "load
	// more" affordance exactly when there is more to load.
	Next    Cursor
	HasMore bool
}

// defaultPageSize and maxPageSize bound a history request.
const (
	defaultPageSize = 20
	maxPageSize     = 100
)

// orderQuery is one shop order with everything the panel renders.
//
// The refund amount comes from the ledger entry the refund actually wrote, not
// from the order total: a discounted order and a refunded one can differ, and
// the number worth showing is the one the customer's balance received.
const orderQuery = `
	SELECT g.order_id::text, COALESCE(l.name, p.code), p.kind,
	       COALESCE(p.duration_months, 0), COALESCE(p.star_quantity, 0),
	       g.quantity, g.recipient_username, g.recipient_is_self,
	       g.quoted_price_minor, g.discount_minor, g.currency, g.status,
	       g.created_at, g.updated_at,
	       o.state, o.wallet_minor, o.external_minor, o.paid_minor,
	       COALESCE(d.status, ''), COALESCE(d.attempt_count, 0),
	       COALESCE(d.failure_class, ''),
	       d.provider_reference IS NOT NULL,
	       d.refund_ledger_transaction_id IS NOT NULL,
	       d.delivered_at, d.updated_at,
	       COALESCE((SELECT e.amount_minor FROM ledger_entries e
	                 WHERE e.transaction_id = d.refund_ledger_transaction_id
	                   AND e.account_type = 'customer_wallet'
	                   AND e.user_id = g.user_id), 0)
	FROM goods_orders g
	JOIN orders o ON o.id = g.order_id
	JOIN goods_products p ON p.id = g.product_id
	LEFT JOIN goods_product_localizations l ON l.product_id = p.id AND l.locale = $2
	LEFT JOIN goods_deliveries d ON d.order_id = g.order_id
	WHERE g.user_id = $1::uuid`

// Orders reads the customer's shop history, newest first.
//
// The customer identifier is part of the predicate rather than checked after the
// fact, so a query that runs at all can only return their own rows.
func (service *Service) Orders(
	ctx context.Context, customerID, locale string, cursor Cursor, limit int,
) (Page, error) {
	if !service.Enabled() {
		return Page{}, ErrUnavailable
	}
	if limit <= 0 || limit > maxPageSize {
		limit = defaultPageSize
	}

	var (
		cursorTime *time.Time
		cursorID   *string
	)
	if !cursor.CreatedAt.IsZero() && strings.TrimSpace(cursor.ID) != "" {
		at := cursor.CreatedAt.UTC()
		id := strings.TrimSpace(cursor.ID)
		cursorTime, cursorID = &at, &id
	}

	// One row beyond the page is read to learn whether another page exists,
	// which is cheaper and more honest than a count over a growing history.
	rows, err := service.pool.Query(ctx, orderQuery+`
		  AND ($3::timestamptz IS NULL
		       OR g.created_at < $3::timestamptz
		       OR (g.created_at = $3::timestamptz AND g.order_id < $4::uuid))
		ORDER BY g.created_at DESC, g.order_id DESC
		LIMIT $5`,
		customerID, normalizeLocale(locale), cursorTime, cursorID, limit+1)
	if err != nil {
		return Page{}, err
	}
	defer rows.Close()

	page := Page{Items: make([]Order, 0, limit)}
	for rows.Next() {
		order, scanErr := service.scanOrder(rows)
		if scanErr != nil {
			return Page{}, scanErr
		}
		page.Items = append(page.Items, order)
	}
	if err = rows.Err(); err != nil {
		return Page{}, err
	}
	if len(page.Items) > limit {
		last := page.Items[limit-1]
		page.Items = page.Items[:limit]
		page.Next, page.HasMore = Cursor{CreatedAt: last.CreatedAt, ID: last.ID}, true
	}
	return page, nil
}

// Order reads one of the customer's own shop orders.
//
// "Not yours" and "does not exist" are the same answer, because the identifier
// arrives in a URL the customer controls and an ownership check that answers
// differently for the two turns the route into a way to probe for other
// people's orders.
func (service *Service) Order(ctx context.Context, customerID, orderID, locale string) (Order, error) {
	if !service.Enabled() {
		return Order{}, ErrUnavailable
	}
	if strings.TrimSpace(orderID) == "" {
		return Order{}, accountpg.ErrNotFound
	}
	rows, err := service.pool.Query(ctx, orderQuery+" AND g.order_id = $3::uuid",
		customerID, normalizeLocale(locale), strings.TrimSpace(orderID))
	if err != nil {
		return Order{}, accountpg.ErrNotFound
	}
	defer rows.Close()
	if !rows.Next() {
		return Order{}, accountpg.ErrNotFound
	}
	order, err := service.scanOrder(rows)
	if err != nil {
		return Order{}, err
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return Order{}, err
	}
	return order, nil
}

// scanOrder reads one row and classifies its delivery.
func (service *Service) scanOrder(rows pgx.Rows) (Order, error) {
	var (
		order        Order
		duration     int32
		stars        int32
		quantity     int32
		attempts     int32
		record       deliveryRecord
		delivered    pgtype.Timestamptz
		deliveryTime pgtype.Timestamptz
		refundMinor  int64
	)
	if err := rows.Scan(
		&order.ID, &order.ProductName, &order.Kind, &duration, &stars,
		&quantity, &order.Recipient, &order.RecipientIsSelf,
		&order.PriceMinor, &order.DiscountMinor, &order.Currency, &record.goodsStatus,
		&order.CreatedAt, &order.UpdatedAt,
		&order.PaymentState, &order.WalletMinor, &order.ExternalMinor, &order.PaidMinor,
		&record.deliveryStatus, &attempts, &record.failureClass,
		&record.submitted, &record.refunded, &delivered, &deliveryTime, &refundMinor,
	); err != nil {
		return Order{}, err
	}

	order.DurationMonths, order.StarQuantity = int(duration), int(stars)
	order.Quantity = int(quantity)
	order.CreatedAt, order.UpdatedAt = order.CreatedAt.UTC(), order.UpdatedAt.UTC()
	record.orderState = order.PaymentState
	record.attempts = int(attempts)

	order.PaymentRequired = order.ExternalMinor > order.PaidMinor &&
		(order.PaymentState == "pending" || order.PaymentState == "draft")
	order.PaymentPossible = !order.PaymentRequired || service.canSettle(order.Currency)

	order.Delivery = classifyDelivery(record)
	order.Delivery.SupportReference = "goods-order:" + order.ID
	order.Delivery.DeliveredAt = delivered.Time.UTC()
	order.Delivery.UpdatedAt = deliveryTime.Time.UTC()
	if record.refunded {
		order.Delivery.Refund = Refund{
			Refunded: true, AmountMinor: refundMinor, Currency: order.Currency,
		}
	}
	return order, nil
}

// canSettle reports whether any configured provider can take money in this
// currency. It never names one: choosing a provider is the checkout surface's
// job, and duplicating that choice here is how two checkouts start disagreeing
// about what is enabled.
func (service *Service) canSettle(currency string) bool {
	if service.payments == nil {
		return false
	}
	for _, option := range service.payments.Options() {
		if option.Enabled && option.Supports(currency) {
			return true
		}
	}
	return false
}

// deliveryRecord is the raw state of one purchase as the tables hold it.
//
// It exists so the classification below can be a pure function of the stored
// facts. Every one of these states is written by the delivery worker, and a
// customer surface that inferred them differently would eventually contradict
// the operator panel about the same row.
type deliveryRecord struct {
	// orderState is `orders.state`.
	orderState string
	// goodsStatus is `goods_orders.status`.
	goodsStatus string
	// deliveryStatus is `goods_deliveries.status`, empty when no delivery row
	// exists — which is what an unpaid order looks like, because a delivery is
	// only created when the order settles.
	deliveryStatus string
	// submitted reports a provider reference on the row: the gateway took the
	// purchase. Its presence is what turns a further attempt into a poll rather
	// than a second submission.
	submitted bool
	attempts  int
	// failureClass is the domain classification of the last failure.
	failureClass string
	refunded     bool
}

// classifyDelivery turns the stored state into the one state a customer reads.
//
// The order of the cases is the order of certainty. A delivered row is
// delivered whatever else is recorded; a parked one is parked and must never
// read as a failure the customer could retry; a terminal failure is only
// "refunded" once the credit actually exists, because telling somebody their
// money is back before it is would be a lie with a plausible deniability.
func classifyDelivery(record deliveryRecord) Delivery {
	delivery := Delivery{Attempts: record.attempts, FailureReason: record.failureClass}

	if record.deliveryStatus == "" {
		// No delivery row: the order has not settled, so nothing was ever
		// submitted to a provider.
		switch {
		case record.orderState == "cancelled" || record.orderState == "expired":
			delivery.State = StateCancelled
		case record.goodsStatus != "quoted":
			// Paid, with the delivery row not yet visible. Settlement writes the
			// order status and the delivery in one transaction, so this is a
			// moment rather than a state, and queued is what it is a moment of.
			delivery.State = StateQueued
		default:
			delivery.State = StateAwaitingPayment
		}
		delivery.FailureReason = ""
		return delivery
	}

	switch record.deliveryStatus {
	case "delivered":
		delivery.State, delivery.FailureReason = StateDelivered, ""
	case "needs_review":
		// Neither retried nor refunded. A person decides, and the panel offers a
		// way to reach one instead of a button that could buy the goods twice.
		delivery.State = StateNeedsReview
		delivery.SupportHandoff = true
		if delivery.FailureReason == "" {
			delivery.FailureReason = goods.FailureAmbiguous
		}
	case "cancelled":
		delivery.State = StateCancelled
	case "failed":
		if record.refunded {
			delivery.State = StateRefunded
		} else {
			delivery.State = StateFailed
			delivery.SupportHandoff = true
		}
	case "pending":
		delivery.State, delivery.FailureReason = StateQueued, ""
	case "submitted":
		switch {
		case record.failureClass != "" && goods.Retryable(record.failureClass):
			// A transient fault on the backoff schedule. The customer is waiting
			// rather than stuck, and saying so is the difference between a
			// screen that reassures and one that invites a support ticket.
			delivery.State = StateDelayed
		case record.submitted:
			delivery.State, delivery.FailureReason = StatePolling, ""
		default:
			delivery.State, delivery.FailureReason = StateSubmitted, ""
		}
	default:
		// A status written by a future version. Reporting it as queued keeps the
		// screen honest — something is in flight — without inventing a meaning
		// for a value this build does not know.
		delivery.State, delivery.FailureReason = StateQueued, ""
	}
	return delivery
}
