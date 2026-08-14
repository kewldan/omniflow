package panelpg

import (
	"context"
	"fmt"
	"time"

	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// Sales reporting over a period an operator chooses.
//
// The dashboard's fixed thirty-day window is deliberate and stays: two visits to
// it compare, which is the property a dashboard needs. What it cannot answer is
// "how did last quarter go", "which plan is actually selling", and "are the
// trials turning into anything", and those are the questions this answers.
//
// Three rules run through all of it, and each is a decision rather than a
// detail:
//
//   - The period keys on `orders.paid_at`, which is set once and never moves, so
//     re-running a report over a closed period returns the same figures.
//   - Provider money and wallet credit are never added together. The balance was
//     already revenue when it was funded.
//   - Refunds are reported on the date they were issued rather than against the
//     sale they reverse, because the first is the only one an operator can act
//     on and the second silently changes a month that had closed.

// SalesLine is one row of the operation breakdown: what kind of sale, in what
// currency, and what it was worth.
type SalesLine struct {
	// Operation is the order's own kind — purchase, renewal, extension, upgrade,
	// downgrade, addon, topup, gift, or goods. It is the breakdown that
	// separates new business from renewals and from money that only moved into a
	// wallet.
	Operation string `json:"operation"`
	Currency  string `json:"currency"`
	Orders    int64  `json:"orders"`
	// SubtotalMinor is the list price before any discount.
	SubtotalMinor int64 `json:"subtotalMinor"`
	DiscountMinor int64 `json:"discountMinor"`
	// PaidMinor arrived through a payment provider. WalletMinor came out of a
	// balance that was funded earlier, and adding the two counts it twice.
	PaidMinor   int64 `json:"paidMinor"`
	WalletMinor int64 `json:"walletMinor"`
}

// PlanSales is one plan version's contribution.
type PlanSales struct {
	PlanCode      string `json:"planCode"`
	PlanVersion   int32  `json:"planVersion"`
	BillingPeriod string `json:"billingPeriod"`
	Currency      string `json:"currency"`
	Orders        int64  `json:"orders"`
	// GrossMinor is the list value of the lines, before order-level discounts.
	// It is the figure that compares plans against each other; the money that
	// actually arrived is in the operation breakdown.
	GrossMinor int64 `json:"grossMinor"`
}

// DaySales is one day in the installation's own timezone.
type DaySales struct {
	Day         string `json:"day"`
	Currency    string `json:"currency"`
	Orders      int64  `json:"orders"`
	PaidMinor   int64  `json:"paidMinor"`
	WalletMinor int64  `json:"walletMinor"`
}

// PeriodRefunds is what was refunded in the period, by the refund's own date.
//
// It is a separate type from the finance screen's RefundSummary because it
// answers a different question: that one is a refund against one order, this one
// is a period total keyed on when the money went back.
type PeriodRefunds struct {
	Currency      string `json:"currency"`
	Refunds       int64  `json:"refunds"`
	RefundedMinor int64  `json:"refundedMinor"`
}

// TrialConversion is a cohort figure: of the trials claimed in this period, how
// many of those customers have since paid for something.
type TrialConversion struct {
	Trials    int64 `json:"trials"`
	Converted int64 `json:"converted"`
	// Cohort says out loud that the denominator is trials started in the window
	// and the numerator counts conversions at any later time, so a window ending
	// today reads low. The panel renders it beside the figure rather than
	// leaving an operator to discover the shape of the measure from a graph.
	Cohort bool `json:"cohort"`
}

// SalesReport is the whole screen in one read.
type SalesReport struct {
	Since    time.Time `json:"since"`
	Until    time.Time `json:"until"`
	Timezone string    `json:"timezone"`
	// Currency is the filter that was applied, or empty for every currency.
	Currency string `json:"currency,omitempty"`

	ByOperation []SalesLine     `json:"byOperation"`
	ByPlan      []PlanSales     `json:"byPlan"`
	ByDay       []DaySales      `json:"byDay"`
	Refunds     []PeriodRefunds `json:"refunds"`
	Trials      TrialConversion `json:"trials"`
	GeneratedAt time.Time       `json:"generatedAt"`
}

// MaxSalesReportWindow bounds a single report.
//
// A period is a scan over an indexed range, and an operator who types 1970 into
// a date field should get a refusal naming the limit rather than a query that
// runs for a minute and times out behind a proxy. Two years covers every
// comparison anybody makes against a service that has existed for less.
const MaxSalesReportWindow = 2 * 366 * 24 * time.Hour

// SalesReport reads the report for a period.
//
// The timezone decides where a day begins, and it is the caller's rather than
// UTC: "sales on the third" means the operator's third, and a report bucketed in
// UTC puts an evening sale several hours east on the wrong day.
func (service *Service) SalesReport(
	ctx context.Context, since, until time.Time, timezone, currency string,
) (SalesReport, error) {
	if !until.After(since) {
		return SalesReport{}, wrapValidation(fmt.Errorf("the period ends before it starts"))
	}
	if until.Sub(since) > MaxSalesReportWindow {
		return SalesReport{}, wrapValidation(fmt.Errorf(
			"a report covers at most %d days", int(MaxSalesReportWindow.Hours()/24),
		))
	}
	if timezone == "" {
		timezone = "UTC"
	}
	// A timezone the database does not know would make date_trunc fail deep
	// inside a query. Loading it here turns that into a message naming the
	// field.
	if _, err := time.LoadLocation(timezone); err != nil {
		return SalesReport{}, wrapValidation(fmt.Errorf("%q is not a known timezone", timezone))
	}

	queries := service.queries()
	report := SalesReport{
		Since: since.UTC(), Until: until.UTC(), Timezone: timezone, Currency: currency,
		ByOperation: []SalesLine{}, ByPlan: []PlanSales{}, ByDay: []DaySales{},
		Refunds: []PeriodRefunds{}, Trials: TrialConversion{Cohort: true},
		GeneratedAt: time.Now().UTC(),
	}

	operations, err := queries.SalesByOperation(ctx, dbgen.SalesByOperationParams{
		Since: timestamp(since), Until: timestamp(until), Currency: optionalText(currency),
	})
	if err != nil {
		return SalesReport{}, err
	}
	for _, row := range operations {
		report.ByOperation = append(report.ByOperation, SalesLine{
			Operation: row.Operation, Currency: row.Currency, Orders: row.OrderCount,
			SubtotalMinor: row.SubtotalMinor, DiscountMinor: row.DiscountMinor,
			PaidMinor: row.PaidMinor, WalletMinor: row.WalletMinor,
		})
	}

	plans, err := queries.SalesByPlan(ctx, dbgen.SalesByPlanParams{
		Since: timestamp(since), Until: timestamp(until), Currency: optionalText(currency),
	})
	if err != nil {
		return SalesReport{}, err
	}
	for _, row := range plans {
		report.ByPlan = append(report.ByPlan, PlanSales{
			PlanCode: row.PlanCode, PlanVersion: row.PlanVersion,
			BillingPeriod: row.BillingPeriod, Currency: row.Currency,
			Orders: row.OrderCount, GrossMinor: row.GrossMinor,
		})
	}

	days, err := queries.SalesByDay(ctx, dbgen.SalesByDayParams{
		Since: timestamp(since), Until: timestamp(until),
		Timezone: timezone, Currency: optionalText(currency),
	})
	if err != nil {
		return SalesReport{}, err
	}
	for _, row := range days {
		report.ByDay = append(report.ByDay, DaySales{
			// A date rather than an instant, because it is a bucket in the
			// operator's timezone and rendering it as midnight UTC would move
			// it back a day for half the world.
			Day: row.Day.Time.Format(time.DateOnly), Currency: row.Currency,
			Orders: row.OrderCount, PaidMinor: row.PaidMinor, WalletMinor: row.WalletMinor,
		})
	}

	refunds, err := queries.RefundsInPeriod(ctx, dbgen.RefundsInPeriodParams{
		Since: timestamp(since), Until: timestamp(until), Currency: optionalText(currency),
	})
	if err != nil {
		return SalesReport{}, err
	}
	for _, row := range refunds {
		report.Refunds = append(report.Refunds, PeriodRefunds{
			Currency: row.Currency, Refunds: row.RefundCount, RefundedMinor: row.RefundedMinor,
		})
	}

	trials, err := queries.TrialConversion(ctx, dbgen.TrialConversionParams{
		Since: timestamp(since), Until: timestamp(until),
	})
	if err != nil {
		return SalesReport{}, err
	}
	report.Trials.Trials, report.Trials.Converted = trials.Trials, trials.Converted
	return report, nil
}
