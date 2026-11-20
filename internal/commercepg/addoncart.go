package commercepg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// Saving a mid-period add-on for after a top-up.
//
// Bedolaga puts extra devices or traffic the balance cannot cover into a cart
// and buys them once the top-up settles. An add-on cart is the customer's one
// open cart, like a saved plan or shop item, holding one add-on line for one
// subscription and the price the customer was shown.
//
// What makes buying it unattended safe is the same as for a saved shop item:
// the price is checked again inside the order transaction and a rise is
// refused rather than charged, and the order is created only when the wallet
// covers all of it, under the cart's own idempotency key. A mid-period add-on
// is prorated, so its price only falls as the period runs on; a rise means
// the operator changed the catalog, and the customer confirms that themselves.

const cartKindAddon = "addon"

// AddonCartInput is one mid-period add-on saved until the wallet covers it.
type AddonCartInput struct {
	CustomerID     string
	SubscriptionID string
	Currency       string
	AddonVersionID string
	Quantity       int
	// SavedPriceMinor is the price the customer was shown. The purchase never
	// charges more.
	SavedPriceMinor int64
	TTL             time.Duration
}

// SaveAddonCart stores the add-on as the customer's one open cart, replacing a
// saved plan or shop item, with automatic purchase on. The add-on must be one
// the subscription's current plan offers, and it is priced now so a cart that
// could not be bought at all is refused when it is saved.
func (store *Store) SaveAddonCart(ctx context.Context, input AddonCartInput) (Cart, error) {
	userID, err := parseUUID(input.CustomerID)
	if err != nil {
		return Cart{}, fmt.Errorf("customer ID: %w", err)
	}
	subscriptionID, err := parseUUID(input.SubscriptionID)
	if err != nil {
		return Cart{}, ErrSubscriptionUnknown
	}
	if input.Quantity <= 0 || input.SavedPriceMinor < 0 {
		return Cart{}, ErrAddonUnavailable
	}
	key, err := newCartKey()
	if err != nil {
		return Cart{}, err
	}
	ttl := input.TTL
	if ttl <= 0 {
		ttl = 30 * 24 * time.Hour
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return Cart{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := dbgen.New(tx)
	if _, err = queries.GetCustomerSubscription(ctx, dbgen.GetCustomerSubscriptionParams{SubscriptionID: subscriptionID, UserID: userID}); err != nil {
		return Cart{}, ErrSubscriptionUnknown
	}
	entitlement, err := queries.GetLatestEntitlementForChange(ctx, dbgen.GetLatestEntitlementForChangeParams{UserID: userID, SubscriptionID: subscriptionID})
	if errors.Is(err, pgx.ErrNoRows) || err == nil && entitlement.Status == "paused" {
		return Cart{}, ErrNoActiveSubscription
	}
	if err != nil {
		return Cart{}, err
	}
	selection := []AddonSelection{{AddonVersionID: input.AddonVersionID, Quantity: input.Quantity}}
	if _, _, err = store.priceAddons(ctx, queries, entitlement, AddonOrderInput{
		CustomerID: input.CustomerID, SubscriptionID: input.SubscriptionID, Currency: input.Currency, Addons: selection,
	}); err != nil {
		return Cart{}, err
	}
	versionID, err := parseUUID(input.AddonVersionID)
	if err != nil {
		return Cart{}, ErrAddonUnavailable
	}
	var cartID pgtype.UUID
	if err = tx.QueryRow(ctx, `INSERT INTO carts (
			user_id, subscription_id, plan_version_id, operation, currency, auto_purchase,
			idempotency_key, expires_at, kind, saved_price_minor
		) VALUES ($1, $2, NULL, 'addon', $3, true, $4, $5, 'addon', $6)
		ON CONFLICT (user_id) WHERE status = 'open'
		DO UPDATE SET kind = 'addon', plan_version_id = NULL, subscription_id = EXCLUDED.subscription_id,
			operation = 'addon', currency = EXCLUDED.currency, promo_code = NULL, selected_squad_ids = '{}',
			auto_purchase = true, expires_at = EXCLUDED.expires_at, idempotency_key = EXCLUDED.idempotency_key,
			saved_price_minor = EXCLUDED.saved_price_minor, last_failure = NULL, attempt_count = 0,
			updated_at = now()
		RETURNING id`,
		userID, subscriptionID, input.Currency, key,
		pgtype.Timestamptz{Time: store.clock().Add(ttl), Valid: true}, input.SavedPriceMinor,
	).Scan(&cartID); err != nil {
		return Cart{}, err
	}
	// The cart may have held another kind's contents a moment ago.
	if _, err = tx.Exec(ctx, `DELETE FROM cart_goods WHERE cart_id = $1`, cartID); err != nil {
		return Cart{}, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM cart_addons WHERE cart_id = $1`, cartID); err != nil {
		return Cart{}, err
	}
	if err = queries.SetCartAddon(ctx, dbgen.SetCartAddonParams{CartID: cartID, AddonVersionID: versionID, Quantity: int32(input.Quantity)}); err != nil {
		return Cart{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Cart{}, err
	}
	cart, found, err := store.OpenCart(ctx, input.CustomerID)
	if err == nil && !found {
		err = ErrCartMissing
	}
	return cart, err
}

// OpenAddonCart reads the customer's saved add-on, if the open cart is one.
func (store *Store) OpenAddonCart(ctx context.Context, customerID string) (Cart, bool, error) {
	cart, found, err := store.OpenCart(ctx, customerID)
	if err != nil || !found || cart.Kind != cartKindAddon {
		return Cart{}, false, err
	}
	return cart, true, nil
}

// quoteAddonCart prices a saved add-on as it would be bought right now, in a
// transaction that is rolled back, so quoting never changes anything.
func (store *Store) quoteAddonCart(ctx context.Context, cart Cart) (commerce.CartQuote, error) {
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return commerce.CartQuote{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := dbgen.New(tx)
	entitlement, err := queries.GetLatestEntitlementForChange(ctx, dbgen.GetLatestEntitlementForChangeParams{UserID: cart.UserID, SubscriptionID: cart.SubscriptionID})
	if errors.Is(err, pgx.ErrNoRows) || err == nil && entitlement.Status == "paused" {
		return commerce.CartQuote{}, ErrAddonUnavailable
	}
	if err != nil {
		return commerce.CartQuote{}, err
	}
	_, subtotal, discount, _, err := store.priceAddonOrder(ctx, queries, cart.UserID, entitlement, AddonOrderInput{
		CustomerID: uuidString(cart.UserID), SubscriptionID: uuidString(cart.SubscriptionID),
		Currency: cart.Currency, Addons: cart.Addons,
	})
	if err != nil {
		return commerce.CartQuote{}, err
	}
	balance, err := availableWallet(ctx, queries, cart.UserID, cart.Currency)
	if err != nil {
		return commerce.CartQuote{}, err
	}
	return commerce.CartQuote{
		Currency: cart.Currency, SubtotalMinor: subtotal, DiscountMinor: discount,
		TotalMinor: subtotal - discount, WalletBalanceMinor: balance,
	}, nil
}

// purchaseAddonCart buys a saved add-on from the wallet. The order is opened
// under the cart's idempotency key only when the wallet covers all of it and
// it costs no more than the customer saw; a duplicated top-up, the sweep, and
// a "Buy now" tap all reach the same single order. A refusal the customer has
// to look at — a higher price, an add-on or subscription that is gone — also
// stops the cart buying itself.
func (store *Store) purchaseAddonCart(ctx context.Context, customerID string, cart Cart, unattended bool) (CartPurchase, error) {
	if unattended && !cart.AutoPurchase {
		return CartPurchase{Reason: commerce.CartAutoPurchaseOff}, nil
	}
	if cart.ExpiresAt.Valid && !store.clock().Before(cart.ExpiresAt.Time) {
		return CartPurchase{Reason: commerce.CartExpired}, nil
	}
	maintenance, err := store.Maintenance(ctx)
	if err != nil {
		return CartPurchase{}, err
	}
	if maintenance.Blocks(commerce.ActionPurchase) {
		return CartPurchase{Reason: commerce.CartMaintenance}, nil
	}
	limit := cart.SavedPriceMinor.Int64
	order, err := store.CreateAddonOrder(ctx, AddonOrderInput{
		CustomerID: customerID, SubscriptionID: uuidString(cart.SubscriptionID), Currency: cart.Currency,
		Addons: cart.Addons, IdempotencyKey: cart.IdempotencyKey,
		RequireWalletCover: true, MaxChargeMinor: &limit,
	})
	switch {
	case errors.Is(err, ErrWalletShort):
		return CartPurchase{Reason: commerce.CartInsufficientBalance}, nil
	case errors.Is(err, ErrMaintenance):
		return CartPurchase{Reason: commerce.CartMaintenance}, nil
	case errors.Is(err, ErrPriceChanged):
		return store.failGoodsCart(ctx, cart.Cart, commerce.CartPriceChanged, true)
	case errors.Is(err, ErrAddonUnavailable), errors.Is(err, ErrNoActiveSubscription),
		errors.Is(err, ErrSubscriptionUnknown), errors.Is(err, commerce.ErrAddonLimit), errors.Is(err, ErrRestricted):
		return store.failGoodsCart(ctx, cart.Cart, commerce.CartPlanUnavailable, true)
	case err != nil:
		return CartPurchase{}, err
	}
	if order.State != string(commerce.OrderPaid) && order.State != "fulfilled" {
		// The key already named an order that is not paid. Nothing is
		// charged twice; the customer finishes that order from its page.
		return CartPurchase{Reason: commerce.CartInsufficientBalance}, nil
	}
	if _, err = dbgen.New(store.pool).MarkCartPurchased(ctx, dbgen.MarkCartPurchasedParams{CartID: cart.ID, OrderID: order.ID}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return CartPurchase{}, err
	}
	return CartPurchase{Purchased: true, Reason: "purchased", OrderID: uuidString(order.ID)}, nil
}
