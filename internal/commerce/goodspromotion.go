package commerce

import (
	"errors"
	"time"
)

// Promotions against digital goods.
//
// A shop price is not a plan price, and the difference is the whole reason this
// is a separate function rather than a flag on Discount.
//
// A plan price is a number the operator chose. Discounting it costs them margin
// they set themselves, and the floor is zero — an operator may give a plan away
// if they want to.
//
// A shop price is a provider quote plus a markup. The provider still charges its
// cost whatever the customer pays, so a discount past the markup is the
// installation paying to make a sale. That is a legitimate thing to choose and
// an illegitimate thing to arrive at by accident, so it is refused here and
// belongs in the markup control instead — the place that already exists for
// deciding what a product sells for.

var (
	// ErrPromotionNotForGoods reports a promotion written for plans being used
	// on a shop order, or the reverse. They are separate catalogues and a
	// promotion names which one it discounts.
	ErrPromotionNotForGoods = errors.New("this promotion does not apply to the shop")
	// ErrDiscountBelowCost reports a discount that would take a shop order under
	// what the provider charges.
	//
	// It is its own error rather than a silent cap because a percentage that
	// quietly becomes a different percentage is the mismatch that generates a
	// support ticket. The customer is told the code does not apply here; they
	// are not told it applied and then given less than it said.
	ErrDiscountBelowCost = errors.New("this promotion would take the price below cost")
	// ErrCostUnknownForDiscount reports a product whose provider will not say
	// what it charges and which has no operator-set fixed price. There is no
	// floor to check against, so there is nothing to discount safely.
	ErrCostUnknownForDiscount = errors.New("this product's cost is unknown, so it cannot be discounted")
)

// Catalogues a promotion can apply to.
const (
	PromotionAppliesToPlans = "plans"
	PromotionAppliesToGoods = "goods"
)

// GoodsDiscountRequest is one shop line a promotion is being applied to.
type GoodsDiscountRequest struct {
	CustomerID string
	ProductID  string
	// Price is what the customer would pay without the promotion.
	Price Money
	// CostMinor is what the provider charges for the same thing. It is the
	// floor.
	CostMinor int64
	// CostKnown is false when the provider sells at a price it will not
	// decompose.
	CostKnown bool
	// OperatorPriced is true when the operator configured a fixed price rather
	// than deriving one from a quote. Then the number being discounted is one
	// they chose, exactly like a plan price, and the floor is zero.
	OperatorPriced bool
}

// DiscountForGoods prices a promotion against a shop line.
//
// Every check Discount makes still applies — window, eligibility, redemption
// limits, currency — and this adds the two that are specific to goods: the
// promotion must be written for the shop, and the result may not breach cost.
func (promotion Promotion) DiscountForGoods(
	now time.Time, request GoodsDiscountRequest,
) (Money, error) {
	if promotion.AppliesTo != PromotionAppliesToGoods {
		return Money{}, ErrPromotionNotForGoods
	}
	if len(promotion.ProductIDs) > 0 {
		if _, scoped := promotion.ProductIDs[request.ProductID]; !scoped {
			return Money{}, ErrPromotionInvalid
		}
	}

	// The shared rules are evaluated by the same code the plan path uses, so a
	// change to the window or the redemption limits cannot apply to one
	// catalogue and not the other. PlanIDs is deliberately cleared: a goods
	// promotion is scoped by ProductIDs, and leaving a plan scope populated
	// would make an unrelated list decide a shop question.
	shared := promotion
	shared.PlanIDs = nil
	discount, err := shared.Discount(now, request.CustomerID, "", request.Price)
	if err != nil {
		return Money{}, err
	}

	// A price the operator set themselves is theirs to discount to zero, the
	// same as a plan.
	if request.OperatorPriced {
		return discount, nil
	}
	if !request.CostKnown {
		// Without a cost there is no floor, and discounting a number we cannot
		// bound is how an installation sells below cost without knowing.
		return Money{}, ErrCostUnknownForDiscount
	}
	if request.Price.Amount-discount.Amount < request.CostMinor {
		return Money{}, ErrDiscountBelowCost
	}
	return discount, nil
}

// MaxGoodsDiscount is the largest discount a shop line can bear.
//
// It exists so a surface can say "up to X off" rather than offering a code that
// will be refused, and so an operator writing a promotion can see the headroom
// a product actually has.
func MaxGoodsDiscount(request GoodsDiscountRequest) int64 {
	if request.OperatorPriced {
		return request.Price.Amount
	}
	if !request.CostKnown {
		return 0
	}
	headroom := request.Price.Amount - request.CostMinor
	if headroom < 0 {
		return 0
	}
	return headroom
}
