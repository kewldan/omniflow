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
	"github.com/omniflow/omniflow/internal/goods"
)

// Saving a shop purchase for later.
//
// A goods cart is the same object as a plan cart — one open cart per customer,
// the same expiry sweep, the same rejection vocabulary — with one difference
// that shapes everything: most shop prices are provider quotes that expire, and
// a plan price is not.
//
// So a goods cart buys itself only when its price is the operator's own: a
// fixed amount or a fixed price per Star. That number does not move with a
// provider rate, so charging it after a top-up charges what the customer was
// shown — the same reasoning that makes a saved plan safe to charge unattended.
// A quote-priced product is saved for the customer to confirm, because charging
// a quote unattended would charge a number last seen days ago against a rate
// that has moved.
//
// What is shared is the property that matters: the price is re-checked before
// any charge — for an automatic purchase, under the order transaction's own
// lock — and a rise is refused rather than applied.

// GoodsCartInput is one shop purchase saved for later.
type GoodsCartInput struct {
	CustomerID string
	ProductID  string
	Quantity   int
	Recipient  string
	IsSelf     bool
	// SavedPriceMinor is what the customer was shown. It is not what they will
	// be charged; keeping it is what makes the comparison at purchase possible.
	SavedPriceMinor int64
	Currency        string
	TTL             time.Duration
	// StarQuantity is the Stars a custom-amount product delivers.
	StarQuantity int
	// AutoPurchase buys the cart when a wallet credit covers it. The caller
	// sets it only for an operator-priced product; the sweep re-checks that
	// before charging regardless.
	AutoPurchase bool
}

// GoodsCart is a saved shop purchase.
type GoodsCart struct {
	dbgen.Cart
	Line dbgen.GetCartGoodsRow
}

// SaveGoodsCart stores or replaces the customer's single open cart.
//
// It replaces a saved plan rather than sitting beside it. There is one open cart
// per customer, and a saved plan and a saved shop item are two different
// intentions — keeping both would mean the auto-purchase sweep charging for
// something the customer believed they had replaced.
func (store *Store) SaveGoodsCart(ctx context.Context, input GoodsCartInput) (GoodsCart, error) {
	userID, err := parseUUID(input.CustomerID)
	if err != nil {
		return GoodsCart{}, fmt.Errorf("customer ID: %w", err)
	}
	productID, err := parseUUID(input.ProductID)
	if err != nil {
		return GoodsCart{}, fmt.Errorf("product ID: %w", err)
	}
	recipient, err := goods.NormalizeRecipient(input.Recipient)
	if err != nil {
		return GoodsCart{}, err
	}
	if input.Quantity <= 0 {
		return GoodsCart{}, ErrPromoInvalid
	}
	key, err := newCartKey()
	if err != nil {
		return GoodsCart{}, err
	}
	ttl := input.TTL
	if ttl <= 0 {
		// The same thirty days a saved plan gets. A shop quote expires in
		// minutes, but the cart is a reminder of an intention rather than a held
		// price, so it outlives the quote deliberately.
		ttl = 30 * 24 * time.Hour
	}

	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return GoodsCart{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := dbgen.New(tx)

	cart, err := queries.UpsertGoodsCart(ctx, dbgen.UpsertGoodsCartParams{
		UserID: userID, Currency: input.Currency, IdempotencyKey: key,
		ExpiresAt:    pgtype.Timestamptz{Time: store.clock().Add(ttl), Valid: true},
		AutoPurchase: input.AutoPurchase,
	})
	if err != nil {
		return GoodsCart{}, err
	}
	if err = queries.SetCartGoods(ctx, dbgen.SetCartGoodsParams{
		CartID: cart.ID, ProductID: productID, Quantity: int32(input.Quantity),
		RecipientUsername: recipient, RecipientIsSelf: input.IsSelf,
		SavedPriceMinor: input.SavedPriceMinor, Currency: input.Currency,
		StarQuantity: optionalInt4(input.StarQuantity),
	}); err != nil {
		return GoodsCart{}, err
	}
	line, err := queries.GetCartGoods(ctx, cart.ID)
	if err != nil {
		return GoodsCart{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return GoodsCart{}, err
	}
	return GoodsCart{Cart: cart, Line: line}, nil
}

// OpenGoodsCart reads the customer's saved shop purchase, if the open cart is
// one. A customer with a saved plan gets no goods cart rather than an error:
// "you have something else saved" is the caller's question to ask.
func (store *Store) OpenGoodsCart(
	ctx context.Context, customerID string,
) (GoodsCart, bool, error) {
	userID, err := parseUUID(customerID)
	if err != nil {
		return GoodsCart{}, false, fmt.Errorf("customer ID: %w", err)
	}
	queries := dbgen.New(store.pool)

	cart, err := queries.GetOpenCart(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return GoodsCart{}, false, nil
	}
	if err != nil {
		return GoodsCart{}, false, err
	}
	if cart.Kind != "goods" {
		return GoodsCart{}, false, nil
	}
	line, err := queries.GetCartGoods(ctx, cart.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		// A goods cart with no line is a row that survived a partial write. It
		// is reported as absent rather than as a cart the customer cannot act
		// on.
		return GoodsCart{}, false, nil
	}
	if err != nil {
		return GoodsCart{}, false, err
	}
	return GoodsCart{Cart: cart, Line: line}, true, nil
}

// GoodsCartDecision is what a saved shop purchase can do right now.
type GoodsCartDecision struct {
	// Reason is empty when the cart is ready to buy, and otherwise one of the
	// cart rejection reasons the plan cart already uses.
	Reason string
	// CurrentPriceMinor is the re-quoted price. It is what the customer would
	// pay, which may differ from what they saved.
	CurrentPriceMinor int64
	SavedPriceMinor   int64
	Currency          string
}

// Ready reports a cart that can be purchased as it stands.
func (decision GoodsCartDecision) Ready() bool { return decision.Reason == "" }

// Increased reports a price that moved up since the customer saved it.
func (decision GoodsCartDecision) Increased() bool {
	return decision.CurrentPriceMinor > decision.SavedPriceMinor
}

// EvaluateGoodsCart decides whether a saved shop purchase may proceed at the
// price it is quoted at now.
//
// The current price is supplied by the caller, because quoting means asking a
// provider and that call has no business inside a database transaction. What
// this decides is what to do with the answer.
//
// A price that fell is applied silently — a customer who saved something and
// comes back to find it cheaper does not need a confirmation. A price that rose
// is refused, and the customer is shown both numbers. Charging more than
// somebody agreed to because time passed is the thing this exists to prevent.
func EvaluateGoodsCart(
	cart GoodsCart, currentPriceMinor int64, now time.Time, maintenance bool,
) GoodsCartDecision {
	decision := GoodsCartDecision{
		CurrentPriceMinor: currentPriceMinor,
		SavedPriceMinor:   cart.Line.SavedPriceMinor,
		Currency:          cart.Line.Currency,
	}
	switch {
	case cart.ExpiresAt.Valid && !now.Before(cart.ExpiresAt.Time):
		decision.Reason = commerce.CartExpired
	case maintenance:
		decision.Reason = commerce.CartMaintenance
	case !cart.Line.Visible || cart.Line.ArchivedAt.Valid:
		// The operator withdrew the product. The cart is not destroyed — the
		// customer may want to know what it was — but it cannot be bought.
		decision.Reason = commerce.CartPlanUnavailable
	case currentPriceMinor > cart.Line.SavedPriceMinor:
		decision.Reason = commerce.CartPriceChanged
	}
	return decision
}

// DiscardGoodsCart cancels the saved shop purchase.
func (store *Store) DiscardGoodsCart(ctx context.Context, customerID string) error {
	return store.DiscardCart(ctx, customerID)
}

// SetGoodsCartPromo attaches or clears the promo code on a saved shop purchase.
//
// It does not validate the code. Validation happens where the discount is
// computed, against the product and the quote of the moment, because a code that
// is valid when saved may not be when the customer comes back — the promotion
// may have ended, or the quote may have moved and left no headroom.
func (store *Store) SetGoodsCartPromo(
	ctx context.Context, customerID, code string,
) (dbgen.Cart, error) {
	userID, err := parseUUID(customerID)
	if err != nil {
		return dbgen.Cart{}, fmt.Errorf("customer ID: %w", err)
	}
	normalized := ""
	if code != "" {
		if normalized, err = commerce.NormalizePromoCode(code); err != nil {
			return dbgen.Cart{}, ErrPromoInvalid
		}
	}
	cart, err := dbgen.New(store.pool).SetGoodsCartPromo(ctx, dbgen.SetGoodsCartPromoParams{
		PromoCode: optionalText(normalized), UserID: userID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return dbgen.Cart{}, ErrCartMissing
	}
	return cart, err
}

// MarkGoodsCartPurchased closes the saved cart once its order exists.
func (store *Store) MarkGoodsCartPurchased(
	ctx context.Context, cartID, orderID string,
) error {
	cart, err := parseUUID(cartID)
	if err != nil {
		return err
	}
	order, err := parseUUID(orderID)
	if err != nil {
		return err
	}
	if _, err = dbgen.New(store.pool).MarkCartPurchased(ctx, dbgen.MarkCartPurchasedParams{
		OrderID: order, CartID: cart,
	}); errors.Is(err, pgx.ErrNoRows) {
		// The cart was already closed, which a double confirm produces. The
		// order exists either way, so this is not a failure.
		return nil
	}
	return err
}

// PreviewGoodsDiscount prices a promo code without redeeming it.
//
// It takes the same locks the purchase takes and releases them by rolling back,
// so a preview can never consume a redemption — the same guarantee PreviewOrder
// gives for a plan.
func (store *Store) PreviewGoodsDiscount(
	ctx context.Context, input GoodsOrderInput,
) (int64, error) {
	userID, err := parseUUID(input.CustomerID)
	if err != nil {
		return 0, fmt.Errorf("customer ID: %w", err)
	}
	productID, err := parseUUID(input.ProductID)
	if err != nil {
		return 0, fmt.Errorf("product ID: %w", err)
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	discount, _, err := store.goodsDiscount(ctx, dbgen.New(tx), userID, productID, input)
	return discount, err
}

// purchaseGoodsCart charges a saved shop purchase from the wallet.
//
// It runs only for an operator-priced product, and only when the wallet alone
// covers it: the order is created with RequireWalletCover, so an unattended
// purchase never opens a payment nobody is there to complete. The price is
// re-checked twice — here against the price the customer saved, and again
// inside the order transaction against the pricing row — and a rise is refused
// at either point. A refused cart stops buying itself, so a price that later
// drops back cannot charge the customer behind their back either; the customer
// is told, and confirms the new price themselves.
//
// The order uses the cart's own idempotency key, so a duplicated top-up, a
// sweep racing the settlement hook, or a replayed webhook all reach one order.
func (store *Store) purchaseGoodsCart(
	ctx context.Context, customerID string, cart dbgen.Cart, unattended bool,
) (CartPurchase, error) {
	queries := dbgen.New(store.pool)
	line, err := queries.GetCartGoods(ctx, cart.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return CartPurchase{Reason: commerce.CartPlanUnavailable}, nil
	}
	if err != nil {
		return CartPurchase{}, err
	}
	if unattended && !cart.AutoPurchase {
		return CartPurchase{Reason: commerce.CartAutoPurchaseOff}, nil
	}
	product, err := queries.GetGoodsProduct(ctx, line.ProductID)
	if err != nil {
		return CartPurchase{}, err
	}
	pricing, err := queries.GetGoodsPricing(ctx, line.ProductID)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.failGoodsCart(ctx, cart, commerce.CartPlanUnavailable, false)
	}
	if err != nil {
		return CartPurchase{}, err
	}
	stars := 0
	if line.StarQuantity.Valid {
		stars = int(line.StarQuantity.Int32)
	} else if product.StarQuantity.Valid {
		stars = int(product.StarQuantity.Int32) * int(line.Quantity)
	}
	price, operatorPriced := goods.PricingRule{
		FixedAmountMinor: int8Pointer(pricing.FixedAmountMinor),
		UnitPriceMinor:   int8Pointer(pricing.UnitPriceMinor),
	}.OperatorTotal(stars, int(line.Quantity))
	if !operatorPriced {
		// The operator switched the product to a provider quote since the
		// cart was saved. A quote is never charged unattended.
		return store.failGoodsCart(ctx, cart, commerce.CartAutoPurchaseOff, true)
	}
	maintenance, err := store.Maintenance(ctx)
	if err != nil {
		return CartPurchase{}, err
	}
	decision := EvaluateGoodsCart(GoodsCart{Cart: cart, Line: line}, price, store.clock(),
		maintenance.Blocks(commerce.ActionPurchase))
	if pricing.Currency != line.Currency {
		decision.Reason = commerce.CartPriceChanged
	}
	if !decision.Ready() {
		return store.failGoodsCart(ctx, cart, decision.Reason, decision.Reason == commerce.CartPriceChanged)
	}

	input := GoodsOrderInput{
		CustomerID: customerID, ProductID: uuidString(line.ProductID), Quantity: int(line.Quantity),
		Recipient: line.RecipientUsername, RecipientIsSelf: line.RecipientIsSelf,
		PriceMinor: price, CostKnown: false, Currency: line.Currency,
		QuoteExpiresAt: store.clock().Add(time.Hour), IdempotencyKey: cart.IdempotencyKey,
		PromoCode: cart.PromoCode.String, OperatorPriced: true, StarQuantity: stars,
		Source: "auto_cart", RequireWalletCover: true,
	}
	order, err := store.CreateGoodsOrder(ctx, input)
	if promoRejectionReason(err) != "" || errors.Is(err, ErrPromoBelowCost) {
		// A code that stopped applying does not cancel the purchase the
		// customer topped up for; it is bought at the undiscounted price.
		input.PromoCode = ""
		order, err = store.CreateGoodsOrder(ctx, input)
	}
	switch {
	case errors.Is(err, ErrWalletShort):
		return CartPurchase{Reason: commerce.CartInsufficientBalance}, nil
	case errors.Is(err, ErrPriceChanged):
		return store.failGoodsCart(ctx, cart, commerce.CartPriceChanged, true)
	case errors.Is(err, ErrMaintenance):
		return CartPurchase{Reason: commerce.CartMaintenance}, nil
	case err != nil:
		return CartPurchase{}, err
	}
	if _, err = dbgen.New(store.pool).MarkCartPurchased(ctx, dbgen.MarkCartPurchasedParams{
		CartID: cart.ID, OrderID: order.ID,
	}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return CartPurchase{}, err
	}
	return CartPurchase{Purchased: true, Reason: "purchased", OrderID: uuidString(order.ID)}, nil
}

// failGoodsCart records why a saved shop purchase could not be bought. A
// refusal the customer has to look at — a price rise, a product that is no
// longer operator-priced — also stops the cart buying itself, so it can never
// charge later at a price they were not shown.
func (store *Store) failGoodsCart(
	ctx context.Context, cart dbgen.Cart, reason string, stopAutoPurchase bool,
) (CartPurchase, error) {
	queries := dbgen.New(store.pool)
	if _, err := queries.RecordCartFailure(ctx, dbgen.RecordCartFailureParams{
		CartID: cart.ID, LastFailure: optionalText(reason),
	}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return CartPurchase{}, err
	}
	if stopAutoPurchase && cart.AutoPurchase {
		if _, err := queries.SetCartAutoPurchase(ctx, dbgen.SetCartAutoPurchaseParams{
			UserID: cart.UserID, AutoPurchase: false,
		}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return CartPurchase{}, err
		}
	}
	return CartPurchase{Reason: reason}, nil
}
