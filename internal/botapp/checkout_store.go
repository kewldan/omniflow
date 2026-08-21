package botapp

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/omniflow/omniflow/internal/accountcheckout"
)

// The customer-side checkout lives in internal/accountcheckout, which the web
// panel calls too. The bot keeps its own method names and signatures — every
// screen and test here still reads the same way — but the tables, the SQL, and
// the rules behind them are now one implementation rather than two.
//
// These are aliases rather than conversions on purpose: a screen that builds a
// CheckoutSession literal, or reads OrderSummary.Phase, keeps compiling and
// keeps meaning exactly what the shared type means.
type (
	// CheckoutSession is one resumable purchase intent, shared with the web panel.
	CheckoutSession = accountcheckout.Session
	// CheckoutAddon is one add-on attached to an open checkout.
	CheckoutAddon = accountcheckout.CheckoutAddon
	// OrderSummary is the customer-facing projection of an order.
	OrderSummary = accountcheckout.OrderSummary
	// RefundStatus is one refund the customer can see against an order.
	RefundStatus = accountcheckout.RefundStatus
)

// ErrOrderNotFound is returned when an order does not exist for this customer.
var ErrOrderNotFound = accountcheckout.ErrOrderNotFound

// checkout is the shared persistence half, bound to this store's pool.
//
// It is constructed per call rather than held as a field because the value is a
// single pointer around the pool the store already owns, and because the store's
// own constructor lives elsewhere and has no reason to know about checkouts.
func (store *PostgresStore) checkout() *accountcheckout.Store {
	return accountcheckout.NewStore(store.pool)
}

// OpenCheckout replaces any unfinished checkout for the customer with a new one.
func (store *PostgresStore) OpenCheckout(ctx context.Context, customerID, planVersionID, operation, currency, subscriptionID string, defaultSquads []string) (CheckoutSession, error) {
	return store.checkout().OpenCheckout(ctx, customerID, planVersionID, operation, currency, subscriptionID, defaultSquads)
}

// Checkout reads the customer's open checkout, if any.
func (store *PostgresStore) Checkout(ctx context.Context, customerID string) (CheckoutSession, bool, error) {
	return store.checkout().Checkout(ctx, customerID)
}

// SetCheckoutPromo records the promo code the customer entered together with the
// rejection reason when it was refused.
func (store *PostgresStore) SetCheckoutPromo(ctx context.Context, sessionID, code, rejection string) (CheckoutSession, error) {
	return store.checkout().SetCheckoutPromo(ctx, sessionID, code, rejection)
}

// SetCheckoutProvider records the payment method and the currency it settles in.
func (store *PostgresStore) SetCheckoutProvider(ctx context.Context, sessionID, provider, currency string) (CheckoutSession, error) {
	return store.checkout().SetCheckoutProvider(ctx, sessionID, provider, currency)
}

// SetCheckoutWallet toggles whether the customer's wallet funds this order.
func (store *PostgresStore) SetCheckoutWallet(ctx context.Context, sessionID string, apply bool) (CheckoutSession, error) {
	return store.checkout().SetCheckoutWallet(ctx, sessionID, apply)
}

// SetCheckoutSubscription targets an existing subscription, or clears the target
// so the checkout opens a new one.
func (store *PostgresStore) SetCheckoutSubscription(ctx context.Context, sessionID, subscriptionID string) (CheckoutSession, error) {
	return store.checkout().SetCheckoutSubscription(ctx, sessionID, subscriptionID)
}

// SetCheckoutSquads stores the customer's squad selection for this checkout.
func (store *PostgresStore) SetCheckoutSquads(ctx context.Context, sessionID string, squadIDs []string) (CheckoutSession, error) {
	return store.checkout().SetCheckoutSquads(ctx, sessionID, squadIDs)
}

// ToggleCheckoutSquad adds or removes one squad from the checkout selection.
func (store *PostgresStore) ToggleCheckoutSquad(ctx context.Context, session CheckoutSession, squadID string) (CheckoutSession, error) {
	return store.checkout().ToggleCheckoutSquad(ctx, session, squadID)
}

// AttachCheckoutOrder binds the created order to the checkout so a repeated
// confirmation resolves to the same order.
func (store *PostgresStore) AttachCheckoutOrder(ctx context.Context, sessionID, orderID string) error {
	return store.checkout().AttachCheckoutOrder(ctx, sessionID, orderID)
}

// OrderCheckoutProvider reads the payment method recorded on the checkout that
// created an order. It is what "try again" resumes with after a payment could
// not be started: the checkout is attached to the order at confirmation and is
// no longer the customer's open checkout, so the ordinary lookup cannot see it.
// An empty string means no checkout, or one that never chose a method.
func (store *PostgresStore) OrderCheckoutProvider(ctx context.Context, customerID, orderID string) (string, error) {
	var provider string
	err := store.pool.QueryRow(ctx, `SELECT COALESCE(provider, '') FROM bot_checkout_sessions
		WHERE user_id = $1::uuid AND order_id = $2::uuid
		ORDER BY updated_at DESC LIMIT 1`, customerID, orderID).Scan(&provider)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return provider, err
}

// CancelCheckout discards the customer's unfinished checkout.
func (store *PostgresStore) CancelCheckout(ctx context.Context, customerID string) error {
	return store.checkout().CancelCheckout(ctx, customerID)
}

// CheckoutAddons lists the add-ons attached to a checkout.
func (store *PostgresStore) CheckoutAddons(ctx context.Context, sessionID string) ([]CheckoutAddon, error) {
	return store.checkout().CheckoutAddons(ctx, sessionID)
}

// ToggleCheckoutAddon adds an add-on to a checkout at quantity one, or removes
// it when it is already there.
func (store *PostgresStore) ToggleCheckoutAddon(ctx context.Context, sessionID, addonVersionID string) error {
	return store.checkout().ToggleCheckoutAddon(ctx, sessionID, addonVersionID)
}

// Order reads one order that belongs to the customer.
func (store *PostgresStore) Order(ctx context.Context, customerID, orderID string, locale Locale) (OrderSummary, error) {
	return store.checkout().Order(ctx, customerID, orderID, string(locale))
}

// Orders lists the customer's order history, newest first. The bot renders a
// fixed number of recent orders, so it asks for a first page and no cursor.
func (store *PostgresStore) Orders(ctx context.Context, customerID string, locale Locale, limit int) ([]OrderSummary, error) {
	return store.checkout().Orders(ctx, customerID, string(locale), accountcheckout.Cursor{}, limit)
}

// Refunds lists refunds recorded against an order's payment intents.
func (store *PostgresStore) Refunds(ctx context.Context, customerID, orderID string) ([]RefundStatus, error) {
	return store.checkout().Refunds(ctx, customerID, orderID)
}

// CancelOrder cancels a customer's own unpaid order.
func (store *PostgresStore) CancelOrder(ctx context.Context, customerID, orderID string) error {
	return store.checkout().CancelOrder(ctx, customerID, orderID, "cancelled by the customer in Telegram")
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

// newIdempotencyKey generates the stable key a bot-initiated order reuses for
// every attempt. The prefix records where the key was minted; the checkout's own
// key is minted by the shared package and is deliberately surface-neutral,
// because the checkout it belongs to is shared.
func newIdempotencyKey() (string, error) {
	return accountcheckout.NewIdempotencyKey("bot")
}
