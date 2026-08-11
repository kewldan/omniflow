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
// that shapes everything: a shop price is a provider quote that expires, and a
// plan price is not.
//
// That is why a goods cart never auto-purchases. The plan cart charges
// unattended when a wallet top-up covers it, which is safe because the price it
// re-quotes against is the operator's own and changes rarely. Doing the same for
// goods would charge a number the customer last saw days ago, against a rate
// that moves. So a goods cart is saved, and the customer comes back and confirms
// at whatever the price is then.
//
// What is shared is the property that matters: the price is re-quoted before any
// charge, and a rise is refused rather than applied.

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
		ExpiresAt: pgtype.Timestamptz{Time: store.clock().Add(ttl), Valid: true},
	})
	if err != nil {
		return GoodsCart{}, err
	}
	if err = queries.SetCartGoods(ctx, dbgen.SetCartGoodsParams{
		CartID: cart.ID, ProductID: productID, Quantity: int32(input.Quantity),
		RecipientUsername: recipient, RecipientIsSelf: input.IsSelf,
		SavedPriceMinor: input.SavedPriceMinor, Currency: input.Currency,
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
