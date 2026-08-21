package commercepg

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/commerce"
	databaseutil "github.com/omniflow/omniflow/internal/database"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// CartInput is a saved purchase intent. It survives an insufficient balance, a
// lost session, and navigation away from checkout.
type CartInput struct {
	CustomerID       string
	SubscriptionID   string
	PlanVersionID    string
	Operation        string
	Currency         string
	PromoCode        string
	SelectedSquadIDs []string
	Addons           []AddonSelection
	AutoPurchase     bool
	TTL              time.Duration
}

// Cart is a saved cart together with the add-ons attached to it.
type Cart struct {
	dbgen.Cart
	Addons []AddonSelection
}

// SaveCart stores or replaces the customer's single open cart. Saving always
// mints a fresh idempotency key, so changing the plan can never resolve to the
// order the previous cart contents created.
func (store *Store) SaveCart(ctx context.Context, input CartInput) (Cart, error) {
	userID, err := parseUUID(input.CustomerID)
	if err != nil {
		return Cart{}, fmt.Errorf("customer ID: %w", err)
	}
	planVersionID, err := parseUUID(input.PlanVersionID)
	if err != nil {
		return Cart{}, fmt.Errorf("plan version ID: %w", err)
	}
	if _, err = commerce.NormalizeOperation(input.Operation); err != nil {
		return Cart{}, err
	}
	squadIDs, err := databaseutil.ParseUUIDs(input.SelectedSquadIDs)
	if err != nil {
		return Cart{}, err
	}
	key, err := newCartKey()
	if err != nil {
		return Cart{}, err
	}
	ttl := input.TTL
	if ttl <= 0 {
		ttl = 30 * 24 * time.Hour
	}
	subscriptionID := pgtype.UUID{}
	if input.SubscriptionID != "" {
		if subscriptionID, err = parseUUID(input.SubscriptionID); err != nil {
			return Cart{}, ErrSubscriptionUnknown
		}
	}
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return Cart{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := dbgen.New(tx)
	if subscriptionID.Valid {
		if _, err = queries.GetCustomerSubscription(ctx, dbgen.GetCustomerSubscriptionParams{SubscriptionID: subscriptionID, UserID: userID}); err != nil {
			return Cart{}, ErrSubscriptionUnknown
		}
	}
	cart, err := queries.UpsertCart(ctx, dbgen.UpsertCartParams{
		UserID: userID, SubscriptionID: subscriptionID, PlanVersionID: planVersionID,
		Operation: input.Operation, Currency: input.Currency, PromoCode: optionalText(input.PromoCode),
		SelectedSquadIds: squadIDs, AutoPurchase: input.AutoPurchase, IdempotencyKey: key,
		ExpiresAt: pgtype.Timestamptz{Time: store.clock().Add(ttl), Valid: true},
	})
	if err != nil {
		return Cart{}, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM cart_addons WHERE cart_id = $1`, cart.ID); err != nil {
		return Cart{}, err
	}
	for _, addon := range input.Addons {
		versionID, parseErr := parseUUID(addon.AddonVersionID)
		if parseErr != nil {
			return Cart{}, ErrAddonUnavailable
		}
		if addon.Quantity <= 0 {
			return Cart{}, ErrAddonUnavailable
		}
		offered, offerErr := queries.IsAddonOfferedForPlan(ctx, dbgen.IsAddonOfferedForPlanParams{PlanVersionID: planVersionID, AddonVersionID: versionID})
		if offerErr != nil {
			return Cart{}, offerErr
		}
		if !offered {
			return Cart{}, ErrAddonUnavailable
		}
		if err = queries.SetCartAddon(ctx, dbgen.SetCartAddonParams{CartID: cart.ID, AddonVersionID: versionID, Quantity: int32(addon.Quantity)}); err != nil {
			return Cart{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return Cart{}, err
	}
	return Cart{Cart: cart, Addons: input.Addons}, nil
}

// OpenCart reads the customer's saved cart, if any.
func (store *Store) OpenCart(ctx context.Context, customerID string) (Cart, bool, error) {
	userID, err := parseUUID(customerID)
	if err != nil {
		return Cart{}, false, err
	}
	queries := dbgen.New(store.pool)
	cart, err := queries.GetOpenCart(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Cart{}, false, nil
	}
	if err != nil {
		return Cart{}, false, err
	}
	addons, err := store.cartAddons(ctx, queries, cart.ID)
	if err != nil {
		return Cart{}, false, err
	}
	return Cart{Cart: cart, Addons: addons}, true, nil
}

func (store *Store) cartAddons(ctx context.Context, queries *dbgen.Queries, cartID pgtype.UUID) ([]AddonSelection, error) {
	rows, err := queries.ListCartAddons(ctx, cartID)
	if err != nil {
		return nil, err
	}
	addons := make([]AddonSelection, 0, len(rows))
	for _, row := range rows {
		addons = append(addons, AddonSelection{AddonVersionID: uuidString(row.AddonVersionID), Quantity: int(row.Quantity)})
	}
	return addons, nil
}

// DiscardCart clears the customer's saved cart, which also cancels any pending
// automatic purchase.
func (store *Store) DiscardCart(ctx context.Context, customerID string) error {
	userID, err := parseUUID(customerID)
	if err != nil {
		return err
	}
	_, err = dbgen.New(store.pool).CancelCart(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	return err
}

// SetCartAutoPurchase turns automatic purchase on or off without discarding the
// cart, so a customer can keep the selection but pay manually.
func (store *Store) SetCartAutoPurchase(ctx context.Context, customerID string, enabled bool) error {
	userID, err := parseUUID(customerID)
	if err != nil {
		return err
	}
	_, err = dbgen.New(store.pool).SetCartAutoPurchase(ctx, dbgen.SetCartAutoPurchaseParams{UserID: userID, AutoPurchase: enabled})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	return err
}

// QuoteCart re-prices a saved cart against the catalog as it is right now. It
// is the only price a cart is ever charged at: the price captured when the cart
// was saved is never reused.
func (store *Store) QuoteCart(ctx context.Context, cart Cart) (commerce.CartQuote, error) {
	preview, err := store.PreviewOrder(ctx, CreateOrderInput{
		CustomerID: uuidString(cart.UserID), PlanVersionID: uuidString(cart.PlanVersionID),
		Currency: cart.Currency, Operation: cart.Operation, PromoCode: cart.PromoCode.String,
		SelectedSquadIDs: databaseutil.UUIDStrings(cart.SelectedSquadIds),
	})
	rejection := ""
	if reason := promoRejectionReason(err); reason != "" {
		rejection = reason
		preview, err = store.PreviewOrder(ctx, CreateOrderInput{
			CustomerID: uuidString(cart.UserID), PlanVersionID: uuidString(cart.PlanVersionID),
			Currency: cart.Currency, Operation: cart.Operation,
			SelectedSquadIDs: databaseutil.UUIDStrings(cart.SelectedSquadIds),
		})
	}
	if err != nil {
		return commerce.CartQuote{}, err
	}
	addonMinor, err := store.quoteCartAddons(ctx, cart)
	if err != nil {
		return commerce.CartQuote{}, err
	}
	quote := commerce.CartQuote{
		Currency: preview.Plan.Currency, SubtotalMinor: preview.Plan.AmountMinor,
		DiscountMinor: preview.DiscountMinor, AddonMinor: addonMinor,
		WalletBalanceMinor: preview.WalletBalance, PromoRejection: rejection,
	}
	quote.TotalMinor = quote.SubtotalMinor - quote.DiscountMinor + quote.AddonMinor
	return quote, nil
}

// quoteCartAddons prices a cart's add-ons at their current catalog price. A
// cart add-on always covers a full period, so no proration applies yet.
func (store *Store) quoteCartAddons(ctx context.Context, cart Cart) (int64, error) {
	if len(cart.Addons) == 0 {
		return 0, nil
	}
	queries := dbgen.New(store.pool)
	total := int64(0)
	for _, selection := range cart.Addons {
		versionID, err := parseUUID(selection.AddonVersionID)
		if err != nil {
			return 0, ErrAddonUnavailable
		}
		row, err := queries.GetAddonVersionForOrder(ctx, dbgen.GetAddonVersionForOrderParams{AddonVersionID: versionID, Currency: cart.Currency})
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrAddonUnavailable
		}
		if err != nil {
			return 0, err
		}
		if row.ArchivedAt.Valid || row.RetiredAt.Valid {
			return 0, ErrAddonUnavailable
		}
		charge, err := commerce.PriceAddon(row.AmountMinor, selection.Quantity, int(row.MaxQuantity), commerce.ProrationFullPrice, store.clock(), time.Time{}, time.Time{})
		if err != nil {
			return 0, err
		}
		total += charge.ChargedMinor
	}
	return total, nil
}

// CartPurchase is the outcome of one automatic-purchase attempt.
type CartPurchase struct {
	Purchased bool
	Reason    string
	OrderID   string
	Quote     commerce.CartQuote
}

// TryPurchaseCart is the unattended sweep's attempt to charge a saved cart: it
// runs only when the customer turned automatic purchase on, and only when the
// wallet balance covers the cart.
//
// The order is created under the cart's own idempotency key, so a duplicated or
// replayed wallet credit that reaches this path twice resolves to the same
// order. The cart is marked purchased in the same step, which closes the window
// where a second attempt could observe it as still open.
func (store *Store) TryPurchaseCart(ctx context.Context, customerID string) (CartPurchase, error) {
	return store.purchaseCart(ctx, customerID, commerce.EvaluateAutoPurchase)
}

// PurchaseCartNow is the customer's own "Buy now". It shares the sweep's
// idempotency key, so a tap while the sweep is running cannot charge twice, and
// it shares every check but one: the automatic-purchase switch, which says
// whether the cart may be bought while the customer is away. A customer who is
// tapping the button is not away.
func (store *Store) PurchaseCartNow(ctx context.Context, customerID string) (CartPurchase, error) {
	return store.purchaseCart(ctx, customerID, commerce.EvaluateCartPurchase)
}

func (store *Store) purchaseCart(ctx context.Context, customerID string, evaluate func(commerce.AutoPurchaseRequest) (string, error)) (CartPurchase, error) {
	cart, found, err := store.OpenCart(ctx, customerID)
	if err != nil || !found {
		return CartPurchase{Reason: commerce.CartAutoPurchaseOff}, err
	}
	quote, err := store.QuoteCart(ctx, cart)
	if errors.Is(err, ErrPlanUnavailable) || errors.Is(err, ErrAddonUnavailable) {
		return store.failCart(ctx, cart, commerce.CartPlanUnavailable)
	}
	if err != nil {
		return CartPurchase{}, err
	}
	maintenance, err := store.Maintenance(ctx)
	if err != nil {
		return CartPurchase{}, err
	}
	reason, evaluateErr := evaluate(commerce.AutoPurchaseRequest{
		Enabled: cart.AutoPurchase, ExpiresAt: cart.ExpiresAt.Time, Now: store.clock(),
		Quote: quote, Maintenance: maintenance.Blocks(commerce.ActionPurchase),
	})
	if evaluateErr != nil {
		return CartPurchase{Reason: reason, Quote: quote}, nil
	}
	// The plan and its add-ons are one order under the cart's own idempotency
	// key, so a duplicated or replayed credit resolves to the same single order.
	order, err := store.CreateOrder(ctx, CreateOrderInput{
		CustomerID: customerID, PlanVersionID: uuidString(cart.PlanVersionID),
		Currency: cart.Currency, Operation: cart.Operation, PromoCode: cart.PromoCode.String,
		IdempotencyKey: cart.IdempotencyKey, SubscriptionID: uuidStringOrEmpty(cart.SubscriptionID),
		SelectedSquadIDs: databaseutil.UUIDStrings(cart.SelectedSquadIds), Addons: cart.Addons,
	})
	if err != nil {
		return store.failCart(ctx, cart, cartFailureReason(err))
	}
	if _, err = dbgen.New(store.pool).MarkCartPurchased(ctx, dbgen.MarkCartPurchasedParams{CartID: cart.ID, OrderID: order.ID}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return CartPurchase{}, err
	}
	return CartPurchase{Purchased: true, Reason: "purchased", OrderID: uuidString(order.ID), Quote: quote}, nil
}

// failCart records why an automatic purchase could not run. The cart stays
// saved so the customer can fix the cause without rebuilding it.
func (store *Store) failCart(ctx context.Context, cart Cart, reason string) (CartPurchase, error) {
	if _, err := dbgen.New(store.pool).RecordCartFailure(ctx, dbgen.RecordCartFailureParams{CartID: cart.ID, LastFailure: optionalText(reason)}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return CartPurchase{}, err
	}
	return CartPurchase{Reason: reason}, nil
}

func cartFailureReason(err error) string {
	switch {
	case errors.Is(err, ErrPlanUnavailable):
		return commerce.CartPlanUnavailable
	case errors.Is(err, ErrMaintenance):
		return commerce.CartMaintenance
	case errors.Is(err, commerce.ErrSubscriptionRejected):
		return commerce.SubscriptionRejectionReason(err)
	default:
		return commerce.CartPriceChanged
	}
}

// promoRejectionReason maps a store error onto the stable promo rejection
// reason, or an empty string when the failure was not about the promotion.
func promoRejectionReason(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrPromoUnknown):
		return "promo_unknown"
	case errors.Is(err, ErrPromoIneligible):
		return "promo_ineligible"
	case errors.Is(err, ErrPromoExhausted):
		return "promo_exhausted"
	case errors.Is(err, ErrPromoInvalid):
		return "promo_invalid"
	default:
		return ""
	}
}

// SweepCarts expires stale carts and retries every saved cart whose balance now
// covers it. It runs beside the existing order-expiry maintenance loop, so a
// wallet credit from any source — a top-up, a referral reward, an operator
// correction — eventually triggers the deferred purchase.
func (store *Store) SweepCarts(ctx context.Context, logger *slog.Logger) {
	queries := dbgen.New(store.pool)
	if _, err := queries.ExpireCarts(ctx); err != nil {
		logger.Warn("cart expiry sweep failed", "error", err)
		return
	}
	carts, err := queries.ListAutoPurchaseCarts(ctx, 200)
	if err != nil {
		logger.Warn("cart sweep listing failed", "error", err)
		return
	}
	for _, cart := range carts {
		purchase, purchaseErr := store.TryPurchaseCart(ctx, uuidString(cart.UserID))
		if purchaseErr != nil {
			logger.Warn("deferred cart purchase failed", "error", purchaseErr)
			continue
		}
		if purchase.Purchased {
			logger.Info("deferred cart purchased", "order_id", purchase.OrderID)
		}
	}
}

func uuidStringOrEmpty(value pgtype.UUID) string {
	if !value.Valid {
		return ""
	}
	return uuidString(value)
}

// newCartKey mints the stable idempotency key one cart uses for its purchase.
// It is random rather than derived so two different carts can never collide.
func newCartKey() (string, error) {
	buffer := make([]byte, 20)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return "cart-" + base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buffer), nil
}
