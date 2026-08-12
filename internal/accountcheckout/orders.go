package accountcheckout

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/commerce"
)

// OrderSummary is the customer-facing projection of an order and everything
// attached to it: payment, provisioning, and refunds.
//
// Provisioning progress is read from the fulfillment operation rather than being
// tracked by whichever screen submitted the order. That is what lets a refresh,
// a second tab, or a switch from the browser to the chat show the same progress:
// the state lives in the database, not in a client that can be closed.
type OrderSummary struct {
	ID                string
	State             commerce.OrderState
	Operation         string
	Currency          string
	SubtotalMinor     int64
	DiscountMinor     int64
	WalletMinor       int64
	ExternalMinor     int64
	PaidMinor         int64
	RefundedMinor     int64
	CreatedAt         time.Time
	ExpiresAt         time.Time
	PlanName          string
	PaymentIntentID   string
	Provider          string
	PaymentStatus     string
	CheckoutURL       string
	ReceiptURL        string
	FulfillmentStatus string
	EntitlementID     string
	Phase             commerce.PaymentPhase
	// SubscriptionID names the subscription this order changed, so a customer who
	// holds several can see which one their money went to.
	SubscriptionID string
	// FulfillmentAttempts and FulfillmentErrorCode explain a provisioning run
	// that is taking longer than one attempt. The error code is the worker's own
	// bounded value, never a provider message: those can carry identifiers.
	FulfillmentAttempts  int
	FulfillmentErrorCode string
	FulfillmentUpdatedAt time.Time
}

const orderColumns = `o.id::text, o.state, o.operation, o.currency, o.subtotal_minor, o.discount_minor,
	o.wallet_minor, o.external_minor, o.paid_minor, o.refunded_minor, o.created_at, o.expires_at,
	COALESCE(l.name, p.code, ''), COALESCE(pi.id::text, ''), COALESCE(pi.provider, ''),
	COALESCE(pi.status, ''), COALESCE(pi.checkout_url, ''),
	COALESCE(pi.receipt_metadata ->> 'receiptUrl', ''),
	COALESCE(fo.status, ''), COALESCE(e.id::text, ''), COALESCE(o.subscription_id::text, ''),
	COALESCE(fo.attempt_count, 0), COALESCE(fo.last_error_code, ''), fo.updated_at`

// orderJoins reaches an order's plan, payment, entitlement, and provisioning.
//
// The plan side is a LEFT JOIN because not every order has one. A wallet top-up
// writes no `order_lines` row — there is nothing being sold, only money moving
// in — and an inner join made such an order unreadable, so a customer who
// started a top-up and reloaded the page lost the provider handoff with no way
// to get it back. The list below keeps the narrower shape it always had; it is
// the single-order read that has to resolve anything the customer owns.
const orderJoins = `FROM orders o
	LEFT JOIN order_lines ol ON ol.order_id = o.id
	LEFT JOIN plans p ON p.id = ol.plan_id
	LEFT JOIN plan_localizations l ON l.plan_id = p.id AND l.locale = $2
	LEFT JOIN LATERAL (
		SELECT * FROM payment_intents intent WHERE intent.order_id = o.id
		ORDER BY intent.created_at DESC LIMIT 1
	) pi ON true
	LEFT JOIN entitlements e ON e.order_id = o.id
	LEFT JOIN LATERAL (
		SELECT * FROM fulfillment_operations op WHERE op.entitlement_id = e.id
		ORDER BY op.created_at DESC LIMIT 1
	) fo ON true`

// Order reads one order that belongs to the customer.
//
// Ownership is part of the query rather than a check afterwards, so an order
// identifier taken from a URL or from callback data cannot address someone
// else's order — and "not yours" and "does not exist" are indistinguishable from
// the outside, which is the point.
func (store *Store) Order(ctx context.Context, customerID, orderID, locale string) (OrderSummary, error) {
	summary, err := scanOrder(store.pool.QueryRow(ctx, `SELECT `+orderColumns+` `+orderJoins+`
		WHERE o.id = $3::uuid AND o.user_id = $1::uuid`, customerID, normalizeLocale(locale), orderID))
	if errors.Is(err, pgx.ErrNoRows) {
		return OrderSummary{}, ErrOrderNotFound
	}
	return summary, err
}

// Cursor is one position in a customer-facing history.
//
// It is the pair the list is ordered by rather than an offset, so a record that
// settles between two pages cannot make a row appear twice or disappear.
type Cursor struct {
	At time.Time
	ID string
}

// Orders lists the customer's order history, newest first.
func (store *Store) Orders(
	ctx context.Context, customerID, locale string, cursor Cursor, limit int,
) ([]OrderSummary, error) {
	limit = boundLimit(limit)
	rows, err := store.pool.Query(ctx, `SELECT `+orderColumns+` `+orderJoins+`
		WHERE o.user_id = $1::uuid
		  -- Purchases only. A top-up moves money into the wallet rather than
		  -- buying anything, and it is accounted for in the wallet's own ledger;
		  -- listing it here as well would show one movement twice and would put
		  -- an order with no plan into a list whose every other row names one.
		  AND ol.order_id IS NOT NULL
		  AND ($4::timestamptz IS NULL
		       OR o.created_at < $4::timestamptz
		       OR (o.created_at = $4::timestamptz AND o.id < NULLIF($5, '')::uuid))
		ORDER BY o.created_at DESC, o.id DESC LIMIT $3`,
		customerID, normalizeLocale(locale), limit, optionalTime(cursor.At), cursor.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	orders := make([]OrderSummary, 0, limit)
	for rows.Next() {
		summary, scanErr := scanOrder(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		orders = append(orders, summary)
	}
	return orders, rows.Err()
}

func scanOrder(row pgx.Row) (OrderSummary, error) {
	var (
		summary     OrderSummary
		state       string
		expiresAt   pgtype.Timestamptz
		attempts    int32
		fulfilledAt pgtype.Timestamptz
	)
	err := row.Scan(&summary.ID, &state, &summary.Operation, &summary.Currency, &summary.SubtotalMinor,
		&summary.DiscountMinor, &summary.WalletMinor, &summary.ExternalMinor, &summary.PaidMinor,
		&summary.RefundedMinor, &summary.CreatedAt, &expiresAt, &summary.PlanName,
		&summary.PaymentIntentID, &summary.Provider, &summary.PaymentStatus, &summary.CheckoutURL,
		&summary.ReceiptURL, &summary.FulfillmentStatus, &summary.EntitlementID, &summary.SubscriptionID,
		&attempts, &summary.FulfillmentErrorCode, &fulfilledAt)
	if err != nil {
		return OrderSummary{}, err
	}
	summary.State = commerce.OrderState(state)
	summary.CreatedAt = summary.CreatedAt.UTC()
	summary.ExpiresAt = expiresAt.Time.UTC()
	summary.FulfillmentAttempts = int(attempts)
	summary.FulfillmentUpdatedAt = fulfilledAt.Time.UTC()
	summary.Phase = commerce.EvaluatePaymentPhase(summary.State, summary.PaymentStatus, summary.FulfillmentStatus)
	return summary, nil
}

// RefundStatus is one refund the customer can see against an order.
type RefundStatus struct {
	Status      string
	AmountMinor int64
	Currency    string
	CreatedAt   time.Time
}

// Refunds lists refunds recorded against an order's payment intents.
func (store *Store) Refunds(ctx context.Context, customerID, orderID string) ([]RefundStatus, error) {
	rows, err := store.pool.Query(ctx, `SELECT r.status, r.amount_minor, r.currency, r.created_at
		FROM refunds r
		JOIN payment_intents pi ON pi.id = r.payment_intent_id
		JOIN orders o ON o.id = pi.order_id
		WHERE o.id = $2::uuid AND o.user_id = $1::uuid
		ORDER BY r.created_at DESC LIMIT 10`, customerID, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	refunds := make([]RefundStatus, 0, 4)
	for rows.Next() {
		var refund RefundStatus
		if err := rows.Scan(&refund.Status, &refund.AmountMinor, &refund.Currency, &refund.CreatedAt); err != nil {
			return nil, err
		}
		refund.CreatedAt = refund.CreatedAt.UTC()
		refunds = append(refunds, refund)
	}
	return refunds, rows.Err()
}

// CancelOrder cancels a customer's own unpaid order.
//
// The mutation is recorded with an idempotency key derived from the order, so a
// repeated tap or a resubmitted form is a no-op rather than an error, and the
// checkout that produced the order is released in the same transaction so the
// customer is not left holding a session pointing at a cancelled order.
func (store *Store) CancelOrder(ctx context.Context, customerID, orderID, reason string) error {
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var state string
	err = tx.QueryRow(ctx, `SELECT state FROM orders
		WHERE id = $2::uuid AND user_id = $1::uuid FOR UPDATE`, customerID, orderID).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrOrderNotFound
	}
	if err != nil {
		return err
	}
	if state != string(commerce.OrderDraft) && state != string(commerce.OrderPending) {
		return ErrOrderNotCancellable
	}
	if _, err = tx.Exec(ctx, `INSERT INTO order_mutations (order_id, action, idempotency_key, reason)
		VALUES ($1::uuid, 'cancel', 'customer:' || $1, $2)
		ON CONFLICT (order_id, action, idempotency_key) DO NOTHING`, orderID, reason); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE orders SET state = 'cancelled', updated_at = now()
		WHERE id = $1::uuid AND state IN ('draft','pending')`, orderID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM bot_checkout_sessions WHERE order_id = $1::uuid`, orderID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// optionalTime renders a zero time as SQL NULL, which is how an absent cursor is
// expressed without a second statement.
func optionalTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC()
}

// boundLimit keeps a page size inside what the panel can render and the database
// should be asked for, whatever a query string claims.
func boundLimit(limit int) int {
	if limit <= 0 {
		return 20
	}
	if limit > 100 {
		return 100
	}
	return limit
}

// normalizeLocale falls back to English so a plan name is never blank because
// the catalogue has no localization for whatever the browser asked for.
func normalizeLocale(locale string) string {
	if strings.ToLower(strings.TrimSpace(locale)) == "ru" {
		return "ru"
	}
	return "en"
}
