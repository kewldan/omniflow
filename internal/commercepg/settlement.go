package commercepg

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// errNoSubscription is the invariant settlement refuses to break: an
// entitlement is always created on a subscription. One created without would
// be adopted onto whatever subscription the customer happens to hold first.
var errNoSubscription = errors.New("a subscription order cannot settle without a subscription")

// revalidateSettlement re-checks, inside the settling transaction, what an
// order was checked against when it was opened. An order paid inside its
// window passes untouched. One paid after it closed may find the world moved
// on: what can still be honoured is put right, and what cannot changes the
// classification to one that does not settle.
func (store *Store) revalidateSettlement(ctx context.Context, queries *dbgen.Queries, order dbgen.Order, classification string) (dbgen.Order, string, error) {
	if order.Operation == "goods" && commerce.StaleGoodsQuote(classification, order.ExpiresAt.Time, store.clock()) {
		// The goods were priced on a gateway rate that has lapsed. Nothing is
		// delivered at it; an operator refunds or honours it by hand.
		return order, commerce.ClassificationStaleQuote, nil
	}
	closed := commerce.ClosedUnpaid(commerce.OrderState(order.State))
	trial := false
	if closed {
		// A closed order stopped counting against its promotion and its
		// trial the moment it closed, so both may have been claimed since.
		// The checks only read; nothing is claimed until every check passed.
		exhausted, err := store.promoExhaustedSince(ctx, queries, order)
		if err != nil {
			return dbgen.Order{}, "", err
		}
		if exhausted {
			return order, commerce.ClassificationLimitExceeded, nil
		}
		taken, isTrial, err := store.trialTakenSince(ctx, queries, order)
		if err != nil {
			return dbgen.Order{}, "", err
		}
		if taken {
			return order, commerce.ClassificationLimitExceeded, nil
		}
		trial = isTrial
	}
	if commerce.SubscriptionOperation(order.Operation) && !order.SubscriptionID.Valid {
		reattached, ok, err := store.reattachSubscription(ctx, queries, order)
		if err != nil {
			return dbgen.Order{}, "", err
		}
		if !ok {
			return order, commerce.ClassificationSubscriptionUnavailable, nil
		}
		order = reattached
	}
	if trial {
		// The claim is the order's again. The check above held the claim row
		// locked, so this cannot find it taken.
		spec, err := queries.GetOrderEntitlementSpec(ctx, order.ID)
		if err != nil {
			return dbgen.Order{}, "", err
		}
		claimed, err := queries.ClaimTrial(ctx, dbgen.ClaimTrialParams{UserID: order.UserID, PlanID: spec.PlanID, OrderID: order.ID})
		if err != nil {
			return dbgen.Order{}, "", err
		}
		if claimed == 0 {
			return order, commerce.ClassificationLimitExceeded, nil
		}
	}
	return order, classification, nil
}

// promoExhaustedSince reports whether the promotion a closed order redeemed
// has been used up by other orders since it closed. The order's own
// redemption does not count — its order is closed — so the question is
// exactly whether settling it would take the promotion past a limit.
func (store *Store) promoExhaustedSince(ctx context.Context, queries *dbgen.Queries, order dbgen.Order) (bool, error) {
	limits, err := queries.LockOrderPromoLimits(ctx, order.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	counts, err := queries.CountPromoRedemptions(ctx, dbgen.CountPromoRedemptionsParams{UserID: order.UserID, PromoCodeID: limits.PromoCodeID, PromotionID: limits.PromotionID})
	if err != nil {
		return false, err
	}
	return commerce.PromoLimitReached(optionalLimit(limits.PromotionLimit), optionalLimit(limits.CodeLimit), int(limits.PerCustomerLimit), int(counts.TotalCount), int(counts.CodeCount), int(counts.CustomerCount)), nil
}

// trialTakenSince reports whether a closed trial order's claim now belongs to
// another order that has not closed, and whether the order is a trial at all.
func (store *Store) trialTakenSince(ctx context.Context, queries *dbgen.Queries, order dbgen.Order) (bool, bool, error) {
	if !commerce.SubscriptionOperation(order.Operation) {
		return false, false, nil
	}
	spec, err := queries.GetOrderEntitlementSpec(ctx, order.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	if spec.PlanKind != "trial" {
		return false, false, nil
	}
	claims, err := queries.LockTrialClaim(ctx, order.UserID)
	if err != nil {
		return false, true, err
	}
	for _, claim := range claims {
		if claim.OrderID != order.ID && !commerce.ClosedUnpaid(commerce.OrderState(claim.OrderState)) {
			return true, true, nil
		}
	}
	return false, true, nil
}

// optionalLimit turns a nullable limit column into the domain's optional
// limit.
func optionalLimit(value pgtype.Int4) *int {
	if !value.Valid {
		return nil
	}
	limit := int(value.Int32)
	return &limit
}

// reattachSubscription gives a subscription order back the subscription it was
// opened for.
//
// A subscription row is created with the order, before any money moves, and
// the ghost cleanup removes it — detaching the order — once the order closes
// unpaid. Money can still arrive afterwards: a manual transfer confirmed late,
// a checkout left open in another tab. The order then targets nothing, and the
// settlement used to fall back to the customer's latest entitlement of *any*
// subscription and the worker to adopt the result onto the primary one: a
// second subscription bought for thirty days overwrote a primary with three
// hundred left.
//
// What the order targeted was a subscription of its own with nothing on it, so
// a fresh one is opened in its place, under the same concurrency policy and
// lock a purchase uses. When the policy will not allow another subscription,
// the payment is not settled at all (ok=false); it is never redirected onto a
// subscription the customer did not buy it for.
func (store *Store) reattachSubscription(ctx context.Context, queries *dbgen.Queries, order dbgen.Order) (dbgen.Order, bool, error) {
	spec, err := queries.GetOrderEntitlementSpec(ctx, order.ID)
	if err != nil {
		return dbgen.Order{}, false, err
	}
	plan, err := queries.GetPlanVersionForOrder(ctx, dbgen.GetPlanVersionForOrderParams{PlanVersionID: spec.PlanVersionID, Currency: order.Currency})
	if errors.Is(err, pgx.ErrNoRows) {
		// The plan is no longer sold in this currency. The concurrency policy
		// cannot be judged without it, so an operator decides.
		return order, false, nil
	}
	if err != nil {
		return dbgen.Order{}, false, err
	}
	subscriptionID, err := store.resolveSubscription(ctx, queries, order.UserID, plan, CreateOrderInput{
		Operation: "purchase", NewSubscription: true,
	})
	if errors.Is(err, commerce.ErrSubscriptionRejected) {
		return order, false, nil
	}
	if err != nil {
		return dbgen.Order{}, false, err
	}
	reattached, err := queries.AttachOrderSubscription(ctx, dbgen.AttachOrderSubscriptionParams{OrderID: order.ID, SubscriptionID: subscriptionID})
	if err != nil {
		return dbgen.Order{}, false, err
	}
	return reattached, true, nil
}
