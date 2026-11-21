package panelpg

import "testing"

// One period per currency can be the recommended one: two would leave the
// bot and the checkout to pick between them.
func TestOneHighlightedPeriodPerCurrency(t *testing.T) {
	period := func(days int32, currency string, highlighted bool) PlanPeriod {
		return PlanPeriod{Days: days, Currency: currency, AmountMinor: 100, ForPurchase: true, Highlighted: highlighted}
	}
	if err := validatePeriods(PlanVersionInput{Periods: []PlanPeriod{
		period(90, "RUB", true), period(180, "RUB", false), period(90, "XTR", true),
	}}); err != nil {
		t.Fatalf("one per currency is allowed: %v", err)
	}
	if err := validatePeriods(PlanVersionInput{Periods: []PlanPeriod{
		period(90, "RUB", true), period(180, "rub", true),
	}}); err == nil {
		t.Fatal("two highlighted periods in one currency are refused")
	}
}
