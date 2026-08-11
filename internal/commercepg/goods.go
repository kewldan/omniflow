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

// GoodsOrderInput describes one digital-goods purchase.
//
// The price is decided before this is called, by the shop service that holds
// the provider adapters. Passing the settled numbers in — rather than quoting
// here — is what keeps the provider call outside the order transaction: a
// gateway that takes ten seconds to answer must not hold a serializable
// transaction open for ten seconds.
type GoodsOrderInput struct {
	CustomerID string
	ProductID  string
	Quantity   int
	// Recipient is a bare Telegram username, already normalised and validated.
	Recipient       string
	RecipientIsSelf bool
	CostMinor       int64
	PriceMinor      int64
	// CostKnown is false when the provider cannot say what the goods cost it.
	// The order is still valid — the customer pays the operator's published
	// price — but the margin is unknown and the finance view says so rather
	// than reporting a fabricated one.
	CostKnown      bool
	Currency       string
	QuoteExpiresAt time.Time
	IdempotencyKey string
	// SkipWallet keeps the customer's balance out of this purchase.
	SkipWallet bool
	PromoCode  string
	// OperatorPriced marks a product sold at a price the operator configured
	// rather than derived from a provider quote. That number is theirs to
	// discount to zero, exactly like a plan price; a derived one is floored at
	// what the provider charges.
	OperatorPriced bool
}

// ErrQuoteExpired is returned when a customer confirms a price that is no
// longer honourable. It is a normal outcome with its own message, not a fault:
// a provider rate that moves is exactly why the quote carries an expiry.
var ErrQuoteExpired = errors.New("the quoted price has expired")

// CreateGoodsOrder opens an ordinary order for a shop purchase.
//
// It is an order like any other, which is the point: the customer's wallet,
// promo codes, payment providers, refunds, receipts, and history all work
// without a special case. What makes it a shop purchase is the `goods_orders`
// row hanging off it and the `goods` operation, and neither changes how the
// money moves.
//
// Nothing is delivered here. Delivery happens when the order is paid, from
// `settleGoodsOrder`, so an unpaid or abandoned order can never reach a
// provider.
func (store *Store) CreateGoodsOrder(
	ctx context.Context, input GoodsOrderInput,
) (dbgen.Order, error) {
	userID, err := parseUUID(input.CustomerID)
	if err != nil {
		return dbgen.Order{}, fmt.Errorf("customer ID: %w", err)
	}
	productID, err := parseUUID(input.ProductID)
	if err != nil {
		return dbgen.Order{}, fmt.Errorf("product ID: %w", err)
	}
	if !input.QuoteExpiresAt.IsZero() && !input.QuoteExpiresAt.After(store.clock().UTC()) {
		return dbgen.Order{}, ErrQuoteExpired
	}
	if _, err = goods.NormalizeRecipient(input.Recipient); err != nil {
		return dbgen.Order{}, err
	}

	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return dbgen.Order{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := dbgen.New(tx)

	if existing, existingErr := queries.GetOrderByIdempotency(ctx, dbgen.GetOrderByIdempotencyParams{
		UserID: userID, IdempotencyKey: input.IdempotencyKey,
	}); existingErr == nil {
		if existing.Operation != "goods" || existing.Currency != input.Currency {
			return dbgen.Order{}, errors.New("idempotency key was already used with different shop parameters")
		}
		return existing, tx.Commit(ctx)
	} else if !errors.Is(existingErr, pgx.ErrNoRows) {
		return dbgen.Order{}, existingErr
	}
	if err = store.assertOperational(ctx, queries); err != nil {
		return dbgen.Order{}, err
	}

	// A promotion is resolved and locked before the wallet is applied, because
	// the wallet covers what is left after the discount rather than the other
	// way round. Getting that order wrong would spend a customer's balance on a
	// discount they were also given.
	discountMinor, promo, err := store.goodsDiscount(ctx, queries, userID, productID, input)
	if err != nil {
		return dbgen.Order{}, err
	}
	payable := input.PriceMinor - discountMinor

	// The wallet is applied the same way a plan purchase applies it, so a
	// customer with a balance sees the same behaviour in the shop as everywhere
	// else.
	walletMinor := int64(0)
	if !input.SkipWallet {
		if walletMinor, err = store.walletContribution(
			ctx, queries, userID, input.Currency, payable,
		); err != nil {
			return dbgen.Order{}, err
		}
	}
	externalMinor := payable - walletMinor
	state := string(commerce.OrderPending)
	if externalMinor == 0 {
		state = string(commerce.OrderPaid)
	}
	expiresAt := input.QuoteExpiresAt
	if expiresAt.IsZero() {
		expiresAt = store.clock().Add(time.Hour)
	}

	order, err := queries.CreateOrder(ctx, dbgen.CreateOrderParams{
		UserID: userID, State: state, Operation: "goods", Currency: input.Currency,
		SubtotalMinor: input.PriceMinor, DiscountMinor: discountMinor,
		WalletMinor: walletMinor, ExternalMinor: externalMinor,
		IdempotencyKey:   input.IdempotencyKey,
		ExpiresAt:        pgtype.Timestamptz{Time: expiresAt, Valid: true},
		SelectedSquadIds: noSquads(),
	})
	if err != nil {
		return dbgen.Order{}, err
	}
	if _, err = queries.CreateGoodsOrder(ctx, dbgen.CreateGoodsOrderParams{
		OrderID: order.ID, UserID: userID, ProductID: productID,
		Quantity:          int32(input.Quantity),
		RecipientUsername: input.Recipient,
		RecipientIsSelf:   input.RecipientIsSelf,
		QuotedCostMinor:   input.CostMinor,
		QuotedPriceMinor:  input.PriceMinor,
		DiscountMinor:     discountMinor,
		Currency:          input.Currency,
		QuoteExpiresAt:    pgtype.Timestamptz{Time: expiresAt, Valid: true},
		CostKnown:         input.CostKnown,
	}); err != nil {
		return dbgen.Order{}, err
	}
	// The redemption is recorded against the order, using the same table a plan
	// purchase uses, so a promotion's usage count covers both catalogues and an
	// operator reading "redeemed 40 times" gets one number rather than two.
	if promo != nil {
		if _, err = queries.InsertPromoRedemption(ctx, dbgen.InsertPromoRedemptionParams{
			PromoCodeID: promo.ID, PromotionID: promo.PromotionID,
			UserID: userID, OrderID: order.ID, DiscountMinor: discountMinor,
		}); err != nil {
			return dbgen.Order{}, err
		}
	}
	if state == string(commerce.OrderPaid) {
		if err = store.settleGoodsOrder(ctx, queries, order, "wallet:"+input.IdempotencyKey); err != nil {
			return dbgen.Order{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return dbgen.Order{}, err
	}
	return order, nil
}

// walletContribution is how much of a purchase the customer's balance covers.
//
// It reads the *available* balance, which excludes credit already committed to
// a pending order, so two orders opened in quick succession cannot each be told
// the same money is theirs.
func (store *Store) walletContribution(
	ctx context.Context, queries *dbgen.Queries, userID pgtype.UUID, currency string, total int64,
) (int64, error) {
	balance, err := queries.GetAvailableWalletBalance(ctx, dbgen.GetAvailableWalletBalanceParams{
		TargetUserID: userID, TargetCurrency: currency,
	})
	if err != nil {
		return 0, err
	}
	if balance <= 0 {
		return 0, nil
	}
	return min(balance, total), nil
}

// settleGoodsOrder turns a paid shop order into a delivery waiting to happen.
//
// It records revenue and writes exactly one `goods_deliveries` row. The primary
// key on `order_id` is the double-delivery guard: a replayed webhook, a
// reconciliation poll, and a manual approval all conflict on it and change
// nothing, so a customer cannot be sent two months of Premium for one payment.
//
// No entitlement is created and nothing is pushed to Remnawave. A shop purchase
// is not a subscription, and folding it into the entitlement path would put
// Telegram Stars into a customer's VPN history.
func (store *Store) settleGoodsOrder(
	ctx context.Context, queries *dbgen.Queries, order dbgen.Order, correlationID string,
) error {
	if err := store.recordOrderRevenue(ctx, queries, order, correlationID); err != nil {
		return err
	}
	goodsOrder, err := queries.GetGoodsOrder(ctx, order.ID)
	if err != nil {
		return err
	}
	product, err := queries.GetGoodsProduct(ctx, goodsOrder.ProductID)
	if err != nil {
		return err
	}
	if _, err = queries.SetGoodsOrderStatus(ctx, dbgen.SetGoodsOrderStatusParams{
		OrderID: order.ID, Status: "paid",
	}); err != nil {
		return err
	}
	// The idempotency key is derived from the order, so every attempt at every
	// layer presents the same one to the provider.
	_, err = queries.CreateGoodsDelivery(ctx, dbgen.CreateGoodsDeliveryParams{
		OrderID:        order.ID,
		ProviderSlug:   product.ProviderSlug,
		IdempotencyKey: "goods:" + uuidString(order.ID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// The delivery already exists. That is the guard working, not a failure.
		return nil
	}
	return err
}

// goodsDiscount resolves a promo code against one shop line.
//
// It repeats the shape of the plan path deliberately rather than sharing it:
// the two differ in which eligibility query runs and in the cost floor, and a
// shared function with two flags would hide exactly the parts that differ. What
// is shared is everything that matters for correctness — the same row lock, the
// same redemption counting, the same customer eligibility check, and the same
// domain rules for the window and the limits.
func (store *Store) goodsDiscount(
	ctx context.Context, queries *dbgen.Queries,
	userID, productID pgtype.UUID, input GoodsOrderInput,
) (int64, *dbgen.GetPromoForRedemptionRow, error) {
	if input.PromoCode == "" {
		return 0, nil, nil
	}
	normalized, err := commerce.NormalizePromoCode(input.PromoCode)
	if err != nil {
		return 0, nil, ErrPromoInvalid
	}
	// FOR UPDATE on the promotion, so two customers redeeming the last use of a
	// limited code serialise rather than both succeeding.
	locked, err := queries.GetPromoForRedemption(ctx, normalized)
	if err != nil {
		return 0, nil, ErrPromoUnknown
	}
	eligible, err := queries.IsPromotionGoodsEligible(ctx, dbgen.IsPromotionGoodsEligibleParams{
		TargetPromotionID: locked.PromotionID, TargetProductID: productID,
	})
	if err != nil || !eligible {
		return 0, nil, ErrPromoIneligible
	}
	counts, err := queries.CountPromoRedemptions(ctx, dbgen.CountPromoRedemptionsParams{
		UserID: userID, PromoCodeID: locked.ID, PromotionID: locked.PromotionID,
	})
	if err != nil {
		return 0, nil, err
	}
	customerEligible, err := queries.CheckPromotionCustomerEligibility(
		ctx, dbgen.CheckPromotionCustomerEligibilityParams{
			Eligibility: locked.Eligibility, UserID: userID,
		})
	if err != nil || !customerEligible.Valid || !customerEligible.Bool {
		return 0, nil, ErrPromoIneligible
	}
	limit := locked.PromotionRedemptionLimit
	if limit.Valid && counts.TotalCount >= limit.Int32 ||
		locked.RedemptionLimit.Valid && counts.CodeCount >= locked.RedemptionLimit.Int32 ||
		counts.CustomerCount >= locked.PerCustomerLimit {
		return 0, nil, ErrPromoExhausted
	}

	promotion := commerce.Promotion{
		Kind: locked.Kind, Value: locked.Value, AppliesTo: locked.AppliesTo,
		CustomerLimit:   int(locked.PerCustomerLimit),
		RedemptionCount: int(counts.TotalCount), CustomerRedeemed: int(counts.CustomerCount),
	}
	if locked.Currency.Valid {
		promotion.Currency = locked.Currency.String
	}
	if locked.StartsAt.Valid {
		promotion.StartsAt = &locked.StartsAt.Time
	}
	if locked.EndsAt.Valid {
		promotion.EndsAt = &locked.EndsAt.Time
	}
	if limit.Valid {
		value := int(limit.Int32)
		promotion.RedemptionLimit = &value
	}

	discount, err := promotion.DiscountForGoods(store.clock(), commerce.GoodsDiscountRequest{
		CustomerID: input.CustomerID, ProductID: uuidString(productID),
		Price:     commerce.Money{Amount: input.PriceMinor, Currency: input.Currency},
		CostMinor: input.CostMinor, CostKnown: input.CostKnown,
		// A product with no provider cost is one the operator priced themselves,
		// which is the same kind of number as a plan price and theirs to
		// discount. The shop service sets CostKnown, so this needs no second
		// source of truth.
		OperatorPriced: !input.CostKnown && input.OperatorPriced,
	})
	switch {
	case errors.Is(err, commerce.ErrDiscountBelowCost):
		return 0, nil, ErrPromoBelowCost
	case errors.Is(err, commerce.ErrCostUnknownForDiscount):
		return 0, nil, ErrPromoBelowCost
	case errors.Is(err, commerce.ErrPromotionNotForGoods):
		return 0, nil, ErrPromoIneligible
	case err != nil:
		return 0, nil, ErrPromoIneligible
	}
	return discount.Amount, &locked, nil
}
