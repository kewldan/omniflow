package botapp

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/commerce"
)

// CheckoutSession is one resumable purchase intent. Its idempotency key is
// generated once and reused for every order attempt, so a duplicate tap resolves
// to the same order instead of creating a second one.
type CheckoutSession struct {
	ID             string
	CustomerID     string
	PlanVersionID  string
	Operation      string
	Currency       string
	Provider       string
	PromoCode      string
	PromoRejection string
	ApplyWallet    bool
	OrderID        string
	IdempotencyKey string
	ExpiresAt      time.Time
}

// OpenCheckout replaces any unfinished checkout for the customer with a new one.
// Only one checkout may be open at a time, which keeps the confirmation screen
// and the order that follows it unambiguous.
func (store *PostgresStore) OpenCheckout(ctx context.Context, customerID, planVersionID, operation, currency string) (CheckoutSession, error) {
	if _, err := commerce.NormalizeOperation(operation); err != nil {
		return CheckoutSession{}, err
	}
	key, err := newIdempotencyKey()
	if err != nil {
		return CheckoutSession{}, err
	}
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return CheckoutSession{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `DELETE FROM bot_checkout_sessions WHERE user_id = $1::uuid AND order_id IS NULL`, customerID); err != nil {
		return CheckoutSession{}, err
	}
	session, err := scanCheckout(tx.QueryRow(ctx, `INSERT INTO bot_checkout_sessions
		(user_id, plan_version_id, operation, currency, idempotency_key)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5)
		RETURNING `+checkoutColumns, customerID, planVersionID, operation, currency, key))
	if err != nil {
		return CheckoutSession{}, err
	}
	return session, tx.Commit(ctx)
}

const checkoutColumns = `id::text, user_id::text, plan_version_id::text, operation, currency, provider,
	promo_code, promo_rejection, apply_wallet, order_id::text, idempotency_key, expires_at`

// Checkout reads the customer's open checkout, if any. An expired checkout is
// removed so the customer restarts from a fresh, correctly priced quote.
func (store *PostgresStore) Checkout(ctx context.Context, customerID string) (CheckoutSession, bool, error) {
	if _, err := store.pool.Exec(ctx, `DELETE FROM bot_checkout_sessions
		WHERE user_id = $1::uuid AND order_id IS NULL AND expires_at <= now()`, customerID); err != nil {
		return CheckoutSession{}, false, err
	}
	session, err := scanCheckout(store.pool.QueryRow(ctx, `SELECT `+checkoutColumns+`
		FROM bot_checkout_sessions WHERE user_id = $1::uuid AND order_id IS NULL`, customerID))
	if errors.Is(err, pgx.ErrNoRows) {
		return CheckoutSession{}, false, nil
	}
	if err != nil {
		return CheckoutSession{}, false, err
	}
	return session, true, nil
}

// SetCheckoutPromo records the promo code the customer entered together with the
// rejection reason when it was refused, so the screen can explain the outcome.
func (store *PostgresStore) SetCheckoutPromo(ctx context.Context, sessionID, code, rejection string) (CheckoutSession, error) {
	return scanCheckout(store.pool.QueryRow(ctx, `UPDATE bot_checkout_sessions
		SET promo_code = NULLIF($2, ''), promo_rejection = NULLIF($3, ''), updated_at = now()
		WHERE id = $1::uuid AND order_id IS NULL
		RETURNING `+checkoutColumns, sessionID, code, rejection))
}

// SetCheckoutProvider records the payment method the customer chose together
// with the currency that method settles in. Choosing a method is what fixes the
// order currency, so both are written at once.
func (store *PostgresStore) SetCheckoutProvider(ctx context.Context, sessionID, provider, currency string) (CheckoutSession, error) {
	return scanCheckout(store.pool.QueryRow(ctx, `UPDATE bot_checkout_sessions
		SET provider = NULLIF($2, ''), currency = $3, updated_at = now()
		WHERE id = $1::uuid AND order_id IS NULL
		RETURNING `+checkoutColumns, sessionID, provider, currency))
}

// SetCheckoutWallet toggles whether the customer's wallet funds this order.
func (store *PostgresStore) SetCheckoutWallet(ctx context.Context, sessionID string, apply bool) (CheckoutSession, error) {
	return scanCheckout(store.pool.QueryRow(ctx, `UPDATE bot_checkout_sessions
		SET apply_wallet = $2, updated_at = now()
		WHERE id = $1::uuid AND order_id IS NULL
		RETURNING `+checkoutColumns, sessionID, apply))
}

// AttachCheckoutOrder binds the created order to the checkout so a repeated
// confirmation resolves to the same order.
func (store *PostgresStore) AttachCheckoutOrder(ctx context.Context, sessionID, orderID string) error {
	_, err := store.pool.Exec(ctx, `UPDATE bot_checkout_sessions
		SET order_id = $2::uuid, updated_at = now() WHERE id = $1::uuid`, sessionID, orderID)
	return err
}

// CancelCheckout discards the customer's unfinished checkout.
func (store *PostgresStore) CancelCheckout(ctx context.Context, customerID string) error {
	_, err := store.pool.Exec(ctx, `DELETE FROM bot_checkout_sessions
		WHERE user_id = $1::uuid AND order_id IS NULL`, customerID)
	return err
}

func scanCheckout(row pgx.Row) (CheckoutSession, error) {
	var (
		session   CheckoutSession
		provider  pgtype.Text
		promo     pgtype.Text
		rejection pgtype.Text
		orderID   pgtype.Text
	)
	err := row.Scan(&session.ID, &session.CustomerID, &session.PlanVersionID, &session.Operation,
		&session.Currency, &provider, &promo, &rejection, &session.ApplyWallet, &orderID,
		&session.IdempotencyKey, &session.ExpiresAt)
	if err != nil {
		return CheckoutSession{}, err
	}
	session.Provider, session.PromoCode, session.PromoRejection, session.OrderID = provider.String, promo.String, rejection.String, orderID.String
	return session, nil
}

// OrderSummary is the customer-facing projection of an order and everything
// attached to it: payment, provisioning, and refunds.
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
}

const orderColumns = `o.id::text, o.state, o.operation, o.currency, o.subtotal_minor, o.discount_minor,
	o.wallet_minor, o.external_minor, o.paid_minor, o.refunded_minor, o.created_at, o.expires_at,
	COALESCE(l.name, p.code), COALESCE(pi.id::text, ''), COALESCE(pi.provider, ''),
	COALESCE(pi.status, ''), COALESCE(pi.checkout_url, ''),
	COALESCE(pi.receipt_metadata ->> 'receiptUrl', ''),
	COALESCE(fo.status, ''), COALESCE(e.id::text, '')`

const orderJoins = `FROM orders o
	JOIN order_lines ol ON ol.order_id = o.id
	JOIN plans p ON p.id = ol.plan_id
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

// Order reads one order that belongs to the customer. Ownership is part of the
// query so an order identifier from callback data cannot address someone else's
// order.
func (store *PostgresStore) Order(ctx context.Context, customerID, orderID string, locale Locale) (OrderSummary, error) {
	summary, err := scanOrder(store.pool.QueryRow(ctx, `SELECT `+orderColumns+` `+orderJoins+`
		WHERE o.id = $3::uuid AND o.user_id = $1::uuid`, customerID, string(locale), orderID))
	if errors.Is(err, pgx.ErrNoRows) {
		return OrderSummary{}, ErrOrderNotFound
	}
	return summary, err
}

// ErrOrderNotFound is returned when an order does not exist for this customer.
var ErrOrderNotFound = errors.New("order not found")

// Orders lists the customer's order history, newest first.
func (store *PostgresStore) Orders(ctx context.Context, customerID string, locale Locale, limit int) ([]OrderSummary, error) {
	rows, err := store.pool.Query(ctx, `SELECT `+orderColumns+` `+orderJoins+`
		WHERE o.user_id = $1::uuid ORDER BY o.created_at DESC LIMIT $3`, customerID, string(locale), limit)
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
		summary   OrderSummary
		state     string
		expiresAt pgtype.Timestamptz
	)
	err := row.Scan(&summary.ID, &state, &summary.Operation, &summary.Currency, &summary.SubtotalMinor,
		&summary.DiscountMinor, &summary.WalletMinor, &summary.ExternalMinor, &summary.PaidMinor,
		&summary.RefundedMinor, &summary.CreatedAt, &expiresAt, &summary.PlanName,
		&summary.PaymentIntentID, &summary.Provider, &summary.PaymentStatus, &summary.CheckoutURL,
		&summary.ReceiptURL, &summary.FulfillmentStatus, &summary.EntitlementID)
	if err != nil {
		return OrderSummary{}, err
	}
	summary.State = commerce.OrderState(state)
	summary.ExpiresAt = expiresAt.Time
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
func (store *PostgresStore) Refunds(ctx context.Context, customerID, orderID string) ([]RefundStatus, error) {
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
		refunds = append(refunds, refund)
	}
	return refunds, rows.Err()
}

// CancelOrder cancels a customer's own unpaid order. The mutation is recorded
// with an idempotency key so a repeated tap is a no-op rather than an error.
func (store *PostgresStore) CancelOrder(ctx context.Context, customerID, orderID string) error {
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var state string
	err = tx.QueryRow(ctx, `SELECT state FROM orders WHERE id = $2::uuid AND user_id = $1::uuid FOR UPDATE`, customerID, orderID).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrOrderNotFound
	}
	if err != nil {
		return err
	}
	if state != string(commerce.OrderDraft) && state != string(commerce.OrderPending) {
		return errors.New("only an unpaid order can be cancelled")
	}
	if _, err = tx.Exec(ctx, `INSERT INTO order_mutations (order_id, action, idempotency_key, reason)
		VALUES ($1::uuid, 'cancel', 'customer:' || $1, 'cancelled by the customer in Telegram')
		ON CONFLICT (order_id, action, idempotency_key) DO NOTHING`, orderID); err != nil {
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

// BeginSessionState opens a multi-step bot flow. The context never carries
// secrets: it holds only identifiers the next message needs.
func (store *PostgresStore) BeginSessionState(ctx context.Context, telegramID int64, state string, sessionContext map[string]any) error {
	payload := []byte(`{}`)
	if len(sessionContext) > 0 {
		encoded, err := json.Marshal(sessionContext)
		if err != nil {
			return err
		}
		payload = encoded
	}
	_, err := store.pool.Exec(ctx, `INSERT INTO bot_sessions (telegram_id, state, context)
		VALUES ($1, $2, $3)
		ON CONFLICT (telegram_id) DO UPDATE SET state = EXCLUDED.state, context = EXCLUDED.context,
			updated_at = now(), expires_at = now() + interval '30 minutes'`, telegramID, state, payload)
	return err
}

// SessionState reads the open flow for a Telegram account, discarding one that
// has expired.
func (store *PostgresStore) SessionState(ctx context.Context, telegramID int64) (string, map[string]any, error) {
	if _, err := store.pool.Exec(ctx, `DELETE FROM bot_sessions WHERE telegram_id = $1 AND expires_at <= now()`, telegramID); err != nil {
		return "", nil, err
	}
	var state string
	var payload []byte
	err := store.pool.QueryRow(ctx, `SELECT state, context FROM bot_sessions WHERE telegram_id = $1`, telegramID).Scan(&state, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	sessionContext := map[string]any{}
	if err := json.Unmarshal(payload, &sessionContext); err != nil {
		return state, nil, err
	}
	return state, sessionContext, nil
}

// newIdempotencyKey generates the stable key a checkout reuses for every order
// attempt. It is random rather than derived so two different purchases can never
// collide on the same key.
func newIdempotencyKey() (string, error) {
	buffer := make([]byte, 20)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return "bot-" + base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buffer), nil
}
