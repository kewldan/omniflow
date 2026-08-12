package accountcheckout

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/commerce"
)

// Session is one resumable purchase intent.
//
// Its idempotency key is generated once and reused for every order attempt, so
// a duplicate confirmation — a second tap in the bot, a double-submitted form in
// the browser, a retried request after a dropped connection — resolves to the
// order that already exists instead of creating a second one.
//
// A customer has at most one open session, enforced by a partial unique index on
// the table rather than by either surface remembering to check. That is what
// makes the checkout genuinely shared: opening one in the browser supersedes one
// left open in the chat, because there is only ever one row to open.
type Session struct {
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
	// SubscriptionID names the subscription this checkout changes. It is empty
	// when the customer is opening a new one.
	SubscriptionID string
	// NewSubscription is set when the customer chose to open an additional
	// subscription rather than change an existing one.
	NewSubscription bool
	// SelectedSquadIDs are the squads chosen in the configurator.
	SelectedSquadIDs []string
}

// CheckoutAddon is one add-on attached to an open checkout.
type CheckoutAddon struct {
	AddonVersionID string
	Quantity       int
}

// Store is the checkout's own persistence: the shared session, the customer's
// order projection, and the wallet ledger they can read back.
//
// It is separate from Service because the Telegram surface needs exactly these
// reads and writes without the pricing orchestration around them, and because
// nothing here needs a payment provider to answer.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore builds the persistence half against an existing pool.
func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// The table keeps its original name. Renaming it to drop the `bot_` prefix would
// be honest but would need a migration, and the rows it holds are already shared
// between both surfaces regardless of what the table is called.
const checkoutColumns = `id::text, user_id::text, plan_version_id::text, operation, currency, provider,
	promo_code, promo_rejection, apply_wallet, order_id::text, idempotency_key, expires_at,
	subscription_id::text, selected_squad_ids`

// OpenCheckout replaces any unfinished checkout for the customer with a new one.
// Only one checkout may be open at a time, which keeps the confirmation screen
// and the order that follows it unambiguous — and means a customer who wandered
// away from the bot and came back on the web resumes one purchase, not two.
func (store *Store) OpenCheckout(
	ctx context.Context, customerID, planVersionID, operation, currency, subscriptionID string,
	defaultSquads []string,
) (Session, error) {
	if _, err := commerce.NormalizeOperation(operation); err != nil {
		return Session{}, err
	}
	key, err := NewIdempotencyKey("checkout")
	if err != nil {
		return Session{}, err
	}
	if defaultSquads == nil {
		defaultSquads = []string{}
	}
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return Session{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `DELETE FROM bot_checkout_sessions
		WHERE user_id = $1::uuid AND order_id IS NULL`, customerID); err != nil {
		return Session{}, err
	}
	session, err := scanCheckout(tx.QueryRow(ctx, `INSERT INTO bot_checkout_sessions
		(user_id, plan_version_id, operation, currency, idempotency_key, subscription_id, selected_squad_ids)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, NULLIF($6, '')::uuid, $7::uuid[])
		RETURNING `+checkoutColumns, customerID, planVersionID, operation, currency, key, subscriptionID, defaultSquads))
	if err != nil {
		return Session{}, err
	}
	return session, tx.Commit(ctx)
}

// Checkout reads the customer's open checkout, if any. An expired checkout is
// removed so the customer restarts from a fresh, correctly priced quote rather
// than confirming a price the catalogue has since moved away from.
func (store *Store) Checkout(ctx context.Context, customerID string) (Session, bool, error) {
	if _, err := store.pool.Exec(ctx, `DELETE FROM bot_checkout_sessions
		WHERE user_id = $1::uuid AND order_id IS NULL AND expires_at <= now()`, customerID); err != nil {
		return Session{}, false, err
	}
	session, err := scanCheckout(store.pool.QueryRow(ctx, `SELECT `+checkoutColumns+`
		FROM bot_checkout_sessions WHERE user_id = $1::uuid AND order_id IS NULL`, customerID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, false, nil
	}
	if err != nil {
		return Session{}, false, err
	}
	return session, true, nil
}

// SetCheckoutPromo records the promo code the customer entered together with the
// rejection reason when it was refused, so the screen can explain the outcome.
func (store *Store) SetCheckoutPromo(ctx context.Context, sessionID, code, rejection string) (Session, error) {
	return store.update(ctx, `UPDATE bot_checkout_sessions
		SET promo_code = NULLIF($2, ''), promo_rejection = NULLIF($3, ''), updated_at = now()
		WHERE id = $1::uuid AND order_id IS NULL
		RETURNING `+checkoutColumns, sessionID, code, rejection)
}

// SetCheckoutProvider records the payment method the customer chose together
// with the currency that method settles in. Choosing a method is what fixes the
// order currency, so both are written at once.
func (store *Store) SetCheckoutProvider(ctx context.Context, sessionID, provider, currency string) (Session, error) {
	return store.update(ctx, `UPDATE bot_checkout_sessions
		SET provider = NULLIF($2, ''), currency = $3, updated_at = now()
		WHERE id = $1::uuid AND order_id IS NULL
		RETURNING `+checkoutColumns, sessionID, provider, currency)
}

// SetCheckoutWallet toggles whether the customer's wallet funds this order.
func (store *Store) SetCheckoutWallet(ctx context.Context, sessionID string, apply bool) (Session, error) {
	return store.update(ctx, `UPDATE bot_checkout_sessions
		SET apply_wallet = $2, updated_at = now()
		WHERE id = $1::uuid AND order_id IS NULL
		RETURNING `+checkoutColumns, sessionID, apply)
}

// SetCheckoutSubscription targets an existing subscription, or clears the target
// so the checkout opens a new one.
func (store *Store) SetCheckoutSubscription(ctx context.Context, sessionID, subscriptionID string) (Session, error) {
	return store.update(ctx, `UPDATE bot_checkout_sessions
		SET subscription_id = NULLIF($2, '')::uuid, updated_at = now()
		WHERE id = $1::uuid AND order_id IS NULL
		RETURNING `+checkoutColumns, sessionID, subscriptionID)
}

// SetCheckoutSquads stores the customer's squad selection for this checkout.
func (store *Store) SetCheckoutSquads(ctx context.Context, sessionID string, squadIDs []string) (Session, error) {
	if squadIDs == nil {
		squadIDs = []string{}
	}
	return store.update(ctx, `UPDATE bot_checkout_sessions
		SET selected_squad_ids = $2::uuid[], updated_at = now()
		WHERE id = $1::uuid AND order_id IS NULL
		RETURNING `+checkoutColumns, sessionID, squadIDs)
}

// ToggleCheckoutSquad adds or removes one squad from the checkout selection and
// returns the resulting set.
func (store *Store) ToggleCheckoutSquad(ctx context.Context, session Session, squadID string) (Session, error) {
	return store.SetCheckoutSquads(ctx, session.ID, ToggleSquad(session.SelectedSquadIDs, squadID))
}

// ToggleSquad adds a squad to a selection or removes it when it is already
// there. It is separated from the write so the rule can be tested without a
// database and so both surfaces produce the same set from the same tap.
func ToggleSquad(selected []string, squadID string) []string {
	result := make([]string, 0, len(selected)+1)
	removed := false
	for _, existing := range selected {
		if existing == squadID {
			removed = true
			continue
		}
		result = append(result, existing)
	}
	if !removed {
		result = append(result, squadID)
	}
	return result
}

// AttachCheckoutOrder binds the created order to the checkout so a repeated
// confirmation resolves to the same order.
func (store *Store) AttachCheckoutOrder(ctx context.Context, sessionID, orderID string) error {
	_, err := store.pool.Exec(ctx, `UPDATE bot_checkout_sessions
		SET order_id = $2::uuid, updated_at = now() WHERE id = $1::uuid`, sessionID, orderID)
	return err
}

// CancelCheckout discards the customer's unfinished checkout.
func (store *Store) CancelCheckout(ctx context.Context, customerID string) error {
	_, err := store.pool.Exec(ctx, `DELETE FROM bot_checkout_sessions
		WHERE user_id = $1::uuid AND order_id IS NULL`, customerID)
	return err
}

// CheckoutAddons lists the add-ons attached to a checkout.
func (store *Store) CheckoutAddons(ctx context.Context, sessionID string) ([]CheckoutAddon, error) {
	rows, err := store.pool.Query(ctx, `SELECT addon_version_id::text, quantity
		FROM bot_checkout_addons WHERE checkout_id = $1::uuid ORDER BY addon_version_id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	addons := make([]CheckoutAddon, 0, 4)
	for rows.Next() {
		var addon CheckoutAddon
		if err = rows.Scan(&addon.AddonVersionID, &addon.Quantity); err != nil {
			return nil, err
		}
		addons = append(addons, addon)
	}
	return addons, rows.Err()
}

// ToggleCheckoutAddon adds an add-on to a checkout at quantity one, or removes
// it when it is already there.
func (store *Store) ToggleCheckoutAddon(ctx context.Context, sessionID, addonVersionID string) error {
	tag, err := store.pool.Exec(ctx, `DELETE FROM bot_checkout_addons
		WHERE checkout_id = $1::uuid AND addon_version_id = $2::uuid`, sessionID, addonVersionID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	_, err = store.pool.Exec(ctx, `INSERT INTO bot_checkout_addons (checkout_id, addon_version_id, quantity)
		VALUES ($1::uuid, $2::uuid, 1)
		ON CONFLICT (checkout_id, addon_version_id) DO NOTHING`, sessionID, addonVersionID)
	return err
}

// update applies one edit to an open checkout.
//
// Every edit statement carries `order_id IS NULL`, so an edit that arrives after
// the checkout was confirmed matches nothing. That is reported as a conflict
// rather than as a missing row: the checkout did exist, and telling the customer
// it never did would hide the order they just created.
func (store *Store) update(ctx context.Context, statement string, arguments ...any) (Session, error) {
	session, err := scanCheckout(store.pool.QueryRow(ctx, statement, arguments...))
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrCheckoutSettled
	}
	return session, err
}

func scanCheckout(row pgx.Row) (Session, error) {
	var (
		session      Session
		provider     pgtype.Text
		promo        pgtype.Text
		rejection    pgtype.Text
		orderID      pgtype.Text
		subscription pgtype.Text
		squadIDs     []string
	)
	err := row.Scan(&session.ID, &session.CustomerID, &session.PlanVersionID, &session.Operation,
		&session.Currency, &provider, &promo, &rejection, &session.ApplyWallet, &orderID,
		&session.IdempotencyKey, &session.ExpiresAt, &subscription, &squadIDs)
	if err != nil {
		return Session{}, err
	}
	session.Provider, session.PromoCode = provider.String, promo.String
	session.PromoRejection, session.OrderID = rejection.String, orderID.String
	session.SubscriptionID, session.SelectedSquadIDs = subscription.String, squadIDs
	session.ExpiresAt = session.ExpiresAt.UTC()
	// A purchase with no named subscription opens a new one. A renewal, upgrade,
	// or downgrade always names the subscription it changes.
	session.NewSubscription = session.SubscriptionID == "" && session.Operation == "purchase"
	return session, nil
}

// NewIdempotencyKey generates the stable key a checkout reuses for every order
// attempt. It is random rather than derived from the intent, so two different
// purchases of the same plan can never collide on one key and silently become
// one order.
func NewIdempotencyKey(prefix string) (string, error) {
	buffer := make([]byte, 20)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return prefix + "-" + base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buffer), nil
}
