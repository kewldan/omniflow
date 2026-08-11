package commerce

import (
	"errors"
	"testing"
	"time"
)

func at(hour int) time.Time {
	return time.Date(2026, 8, 12, hour, 0, 0, 0, time.UTC)
}

func goodsPromotion(kind string, value int64) Promotion {
	return Promotion{
		ID: "promo-1", Kind: kind, Value: value, Currency: "USD",
		AppliesTo: PromotionAppliesToGoods, CustomerLimit: 1,
	}
}

// A shop line with a healthy margin: the customer pays 1000, the provider
// charges 600, so there is 400 of headroom.
func shopLine() GoodsDiscountRequest {
	return GoodsDiscountRequest{
		CustomerID: "c-1", ProductID: "p-1",
		Price:     Money{Amount: 1000, Currency: "USD"},
		CostMinor: 600, CostKnown: true,
	}
}

// The two catalogues are separate, and a promotion names which one it
// discounts.
func TestAPlanPromotionDoesNotDiscountTheShop(t *testing.T) {
	plan := Promotion{
		ID: "promo-1", Kind: "percent", Value: 1000, CustomerLimit: 1,
		AppliesTo: PromotionAppliesToPlans,
	}
	if _, err := plan.DiscountForGoods(at(12), shopLine()); !errors.Is(
		err, ErrPromotionNotForGoods,
	) {
		t.Fatalf("expected ErrPromotionNotForGoods, got %v", err)
	}

	// An empty value reads as plans, which is what every promotion written
	// before the shop existed meant. This is the upgrade-safety property.
	legacy := Promotion{ID: "promo-2", Kind: "percent", Value: 1000, CustomerLimit: 1}
	if _, err := legacy.DiscountForGoods(at(12), shopLine()); !errors.Is(
		err, ErrPromotionNotForGoods,
	) {
		t.Fatalf("a promotion written before the shop existed discounted it: %v", err)
	}
}

// And the reverse, so neither catalogue can be discounted by the other's codes.
func TestAGoodsPromotionDoesNotDiscountAPlan(t *testing.T) {
	promotion := goodsPromotion("percent", 1000)
	if _, err := promotion.Discount(
		at(12), "c-1", "plan-1", Money{Amount: 1000, Currency: "USD"},
	); !errors.Is(err, ErrPromotionInvalid) {
		t.Fatalf("a goods promotion discounted a plan: %v", err)
	}
}

// Ten percent off a line with forty percent margin is fine.
func TestADiscountWithinTheMarginApplies(t *testing.T) {
	promotion := goodsPromotion("percent", 1000)
	discount, err := promotion.DiscountForGoods(at(12), shopLine())
	if err != nil {
		t.Fatalf("discount: %v", err)
	}
	if discount.Amount != 100 || discount.Currency != "USD" {
		t.Fatalf("wrong discount: %+v", discount)
	}
}

// The provider still charges its cost whatever the customer pays, so a discount
// past the markup is the installation paying to make a sale.
func TestADiscountPastTheMarginIsRefused(t *testing.T) {
	// Fifty percent off a line with only forty percent of headroom.
	if _, err := goodsPromotion("percent", 5000).DiscountForGoods(
		at(12), shopLine(),
	); !errors.Is(err, ErrDiscountBelowCost) {
		t.Fatalf("expected ErrDiscountBelowCost, got %v", err)
	}

	// A fixed amount larger than the headroom, which is the easier mistake to
	// make: 500 off a line with 400 of margin.
	if _, err := goodsPromotion("fixed", 500).DiscountForGoods(
		at(12), shopLine(),
	); !errors.Is(err, ErrDiscountBelowCost) {
		t.Fatalf("expected ErrDiscountBelowCost, got %v", err)
	}
}

// Exactly at cost is allowed. The floor is selling below cost, not selling at
// it, and an operator running a break-even promotion chose that.
func TestADiscountThatLandsExactlyOnCostIsAllowed(t *testing.T) {
	discount, err := goodsPromotion("fixed", 400).DiscountForGoods(at(12), shopLine())
	if err != nil {
		t.Fatalf("a break-even discount was refused: %v", err)
	}
	if discount.Amount != 400 {
		t.Fatalf("wrong discount: %+v", discount)
	}
}

// A refusal names the reason rather than quietly giving less than the code said.
func TestTheRefusalIsNotASilentCap(t *testing.T) {
	discount, err := goodsPromotion("percent", 9000).DiscountForGoods(at(12), shopLine())
	if err == nil {
		t.Fatalf("an over-large discount was applied as %+v", discount)
	}
	if discount.Amount != 0 {
		t.Fatalf("a refused promotion still returned a discount: %+v", discount)
	}
}

// A price the operator set themselves is theirs to discount to zero, the same
// as a plan price.
func TestAnOperatorSetPriceHasNoCostFloor(t *testing.T) {
	line := shopLine()
	line.OperatorPriced = true
	line.CostKnown = false
	line.CostMinor = 0

	discount, err := goodsPromotion("percent", 9000).DiscountForGoods(at(12), line)
	if err != nil {
		t.Fatalf("an operator-set price refused a discount: %v", err)
	}
	if discount.Amount != 900 {
		t.Fatalf("wrong discount: %+v", discount)
	}
}

// Discounting a number nobody can bound is how an installation sells below cost
// without knowing.
func TestAnUnknownCostCannotBeDiscounted(t *testing.T) {
	line := shopLine()
	line.CostKnown = false
	line.CostMinor = 0

	if _, err := goodsPromotion("percent", 1000).DiscountForGoods(
		at(12), line,
	); !errors.Is(err, ErrCostUnknownForDiscount) {
		t.Fatalf("expected ErrCostUnknownForDiscount, got %v", err)
	}
}

// Product scoping works the way plan scoping does, and empty means everything.
func TestProductScopingSelectsWhichLinesQualify(t *testing.T) {
	scoped := goodsPromotion("percent", 1000)
	scoped.ProductIDs = map[string]struct{}{"p-2": {}}

	if _, err := scoped.DiscountForGoods(at(12), shopLine()); !errors.Is(
		err, ErrPromotionInvalid,
	) {
		t.Fatalf("a promotion applied to a product it was not scoped to: %v", err)
	}

	line := shopLine()
	line.ProductID = "p-2"
	if _, err := scoped.DiscountForGoods(at(12), line); err != nil {
		t.Fatalf("a scoped promotion refused its own product: %v", err)
	}

	if _, err := goodsPromotion("percent", 1000).DiscountForGoods(
		at(12), shopLine(),
	); err != nil {
		t.Fatalf("an unscoped goods promotion refused a product: %v", err)
	}
}

// A plan scope left populated on a goods promotion must not decide a shop
// question.
func TestAPlanScopeIsIgnoredOnAGoodsPromotion(t *testing.T) {
	promotion := goodsPromotion("percent", 1000)
	promotion.PlanIDs = map[string]struct{}{"plan-9": {}}

	if _, err := promotion.DiscountForGoods(at(12), shopLine()); err != nil {
		t.Fatalf("an unrelated plan scope refused a shop line: %v", err)
	}
}

// The shared rules are evaluated by the same code the plan path uses, so a
// window or a redemption limit cannot apply to one catalogue and not the other.
func TestTheSharedRulesStillApply(t *testing.T) {
	expired := goodsPromotion("percent", 1000)
	ended := at(10)
	expired.EndsAt = &ended
	if _, err := expired.DiscountForGoods(at(12), shopLine()); !errors.Is(
		err, ErrPromotionInvalid,
	) {
		t.Fatalf("an expired promotion applied: %v", err)
	}

	exhausted := goodsPromotion("percent", 1000)
	exhausted.CustomerRedeemed = 1
	if _, err := exhausted.DiscountForGoods(at(12), shopLine()); !errors.Is(
		err, ErrPromotionInvalid,
	) {
		t.Fatalf("an exhausted promotion applied: %v", err)
	}

	wrongCurrency := goodsPromotion("fixed", 100)
	wrongCurrency.Currency = "EUR"
	if _, err := wrongCurrency.DiscountForGoods(at(12), shopLine()); !errors.Is(
		err, ErrCurrencyMismatch,
	) {
		t.Fatalf("a promotion in another currency applied: %v", err)
	}
}

// The headroom is published so a surface can say what a product can bear rather
// than offering a code that will be refused.
func TestMaxGoodsDiscountReportsTheHeadroom(t *testing.T) {
	if headroom := MaxGoodsDiscount(shopLine()); headroom != 400 {
		t.Fatalf("expected 400 of headroom, got %d", headroom)
	}

	operatorPriced := shopLine()
	operatorPriced.OperatorPriced = true
	if headroom := MaxGoodsDiscount(operatorPriced); headroom != 1000 {
		t.Fatalf("an operator-set price should be fully discountable, got %d", headroom)
	}

	unknown := shopLine()
	unknown.CostKnown = false
	if headroom := MaxGoodsDiscount(unknown); headroom != 0 {
		t.Fatalf("an unknown cost should offer no headroom, got %d", headroom)
	}

	// A product already selling at or under cost offers nothing rather than a
	// negative number the caller has to remember to clamp.
	underwater := shopLine()
	underwater.CostMinor = 1200
	if headroom := MaxGoodsDiscount(underwater); headroom != 0 {
		t.Fatalf("expected no headroom on an underwater line, got %d", headroom)
	}
}
