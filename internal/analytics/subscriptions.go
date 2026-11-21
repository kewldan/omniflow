package analytics

import (
	"context"
	"sort"
	"time"

	"github.com/omniflow/omniflow/internal/database/dbgen"
)

const (
	// RenewalGrace is how long after a period ends a successor still counts
	// as a renewal. A customer who comes back a few days late renewed; one who
	// comes back a month later is a returning customer, and the sales report's
	// returning-payers figure is where they show up.
	RenewalGrace = 7 * 24 * time.Hour
	// CohortMonths is how many monthly cohorts the report follows.
	CohortMonths = 12
)

// BaseDay is the subscription base on one day, by kind.
type BaseDay struct {
	Day      string `json:"day"`
	Paid     int64  `json:"paid"`
	Imported int64  `json:"imported"`
	Trial    int64  `json:"trial"`
	Free     int64  `json:"free"`
}

// RecurringRevenueDay is monthly recurring revenue on one day in one
// currency. Estimated is the imported periods' share, valued at list price.
type RecurringRevenueDay struct {
	Day            string `json:"day"`
	Currency       string `json:"currency"`
	MrrMinor       int64  `json:"mrrMinor"`
	EstimatedMinor int64  `json:"estimatedMinor"`
}

// PlanLine is one plan and billing period in the latest snapshot.
type PlanLine struct {
	PlanCode      string `json:"planCode"`
	BillingPeriod string `json:"billingPeriod"`
	Kind          string `json:"kind"`
	Currency      string `json:"currency"`
	Active        int64  `json:"active"`
	MrrMinor      int64  `json:"mrrMinor"`
}

// RenewalLine is how the periods of one plan that ended in the window fared.
type RenewalLine struct {
	PlanCode      string   `json:"planCode"`
	BillingPeriod string   `json:"billingPeriod"`
	Kind          string   `json:"kind"`
	Ended         int64    `json:"ended"`
	Renewed       int64    `json:"renewed"`
	Churned       int64    `json:"churned"`
	Pending       int64    `json:"pending"`
	RenewalRate   *float64 `json:"renewalRate,omitempty"`
}

// Renewals sums the renewal lines and relates the churned periods to the paying
// base at the start of the window.
type Renewals struct {
	GraceDays   int           `json:"graceDays"`
	Ended       int64         `json:"ended"`
	Renewed     int64         `json:"renewed"`
	Churned     int64         `json:"churned"`
	Pending     int64         `json:"pending"`
	RenewalRate *float64      `json:"renewalRate,omitempty"`
	ActiveStart *int64        `json:"activeAtStart,omitempty"`
	ChurnRate   *float64      `json:"churnRate,omitempty"`
	Lines       []RenewalLine `json:"lines"`
}

// AutoRenewLine is automatic renewal for one funding source.
type AutoRenewLine struct {
	Funding      string   `json:"funding"`
	Cycles       int64    `json:"cycles"`
	FirstTry     int64    `json:"firstTry"`
	Recovered    int64    `json:"recovered"`
	Lost         int64    `json:"lost"`
	InProgress   int64    `json:"inProgress"`
	SuccessRate  *float64 `json:"successRate,omitempty"`
	RecoveryRate *float64 `json:"recoveryRate,omitempty"`
}

// FailureCount is how often one failure code occurred.
type FailureCount struct {
	Code     string `json:"code"`
	Attempts int64  `json:"attempts"`
}

// AutoRenew summarises automatic renewal.
type AutoRenew struct {
	Enabled      int64           `json:"enabled"`
	Lines        []AutoRenewLine `json:"lines"`
	FailureCodes []FailureCount  `json:"failureCodes"`
}

// RevenuePerCustomer is revenue per payer and per subscriber in one currency.
type RevenuePerCustomer struct {
	Currency  string `json:"currency"`
	PaidMinor int64  `json:"paidMinor"`
	Payers    int64  `json:"payers"`
	// ArppuMinor is provider money per customer who paid in the window.
	ArppuMinor *int64 `json:"arppuMinor,omitempty"`
	// AverageSubscribers is the mean daily count of paid and imported
	// subscriptions in this currency over the window.
	AverageSubscribers *float64 `json:"averageSubscribers,omitempty"`
	// ArpuMinor is provider money per average subscriber.
	ArpuMinor *int64 `json:"arpuMinor,omitempty"`
}

// CohortRetention is one month of a cohort.
type CohortRetention struct {
	Offset int      `json:"offset"`
	Active int64    `json:"active"`
	Rate   *float64 `json:"rate,omitempty"`
}

// CohortRevenue is what a cohort paid in one currency.
type CohortRevenue struct {
	Currency     string `json:"currency"`
	Customers    int64  `json:"customers"`
	RevenueMinor int64  `json:"revenueMinor"`
	// LtvMinor is revenue per customer of the cohort who paid in this
	// currency: lifetime value so far, not a forecast.
	LtvMinor *int64 `json:"ltvMinor,omitempty"`
}

// Cohort is the customers whose first payment fell in one month.
type Cohort struct {
	Month     string            `json:"month"`
	Customers int64             `json:"customers"`
	Retention []CohortRetention `json:"retention"`
	Revenue   []CohortRevenue   `json:"revenue"`
}

// SubscriptionReport is the subscriptions tab in one read.
type SubscriptionReport struct {
	Since       time.Time             `json:"since"`
	Until       time.Time             `json:"until"`
	Timezone    string                `json:"timezone"`
	GeneratedAt time.Time             `json:"generatedAt"`
	LatestDay   string                `json:"latestDay,omitempty"`
	Base        []BaseDay             `json:"base"`
	Mrr         []RecurringRevenueDay `json:"mrr"`
	Plans       []PlanLine            `json:"plans"`
	Renewals    Renewals              `json:"renewals"`
	AutoRenew   AutoRenew             `json:"autoRenew"`
	PerCustomer []RevenuePerCustomer  `json:"perCustomer"`
	Cohorts     []Cohort              `json:"cohorts"`
}

// SubscriptionReport reads the base, its revenue, renewals, and cohorts.
func (service *Service) SubscriptionReport(ctx context.Context, period Period) (SubscriptionReport, error) {
	queries := service.queries()
	report := SubscriptionReport{
		Since: period.Since, Until: period.Until, Timezone: period.Timezone,
		GeneratedAt: service.now(),
		Base:        []BaseDay{}, Mrr: []RecurringRevenueDay{}, Plans: []PlanLine{},
		Renewals:    Renewals{GraceDays: int(RenewalGrace.Hours() / 24), Lines: []RenewalLine{}},
		AutoRenew:   AutoRenew{Lines: []AutoRenewLine{}, FailureCodes: []FailureCount{}},
		PerCustomer: []RevenuePerCustomer{}, Cohorts: []Cohort{},
	}

	// Snapshots are UTC days; the window's own days bound them.
	firstDay, lastDay := dateOf(period.Since), dateOf(period.Until.Add(-time.Nanosecond))
	snapshots, err := queries.SubscriptionSnapshotsBetween(ctx, dbgen.SubscriptionSnapshotsBetweenParams{
		FromDay: date(firstDay), ToDay: date(lastDay),
	})
	if err != nil {
		return report, err
	}
	subscribers := report.applySnapshots(snapshots)

	if err := report.readRenewals(ctx, queries, period); err != nil {
		return report, err
	}
	if err := report.readAutoRenew(ctx, queries, period); err != nil {
		return report, err
	}
	payers, err := queries.RevenueAndPayers(ctx, dbgen.RevenueAndPayersParams{
		Since: timestamp(period.Since), Until: timestamp(period.Until),
	})
	if err != nil {
		return report, err
	}
	for _, row := range payers {
		line := RevenuePerCustomer{
			Currency: row.Currency, PaidMinor: row.PaidMinor, Payers: row.Payers,
			ArppuMinor: perUnit(row.PaidMinor, row.Payers),
		}
		if average, ok := subscribers[row.Currency]; ok && average > 0 {
			value := average
			line.AverageSubscribers = &value
			arpu := int64(float64(row.PaidMinor) / average)
			line.ArpuMinor = &arpu
		}
		report.PerCustomer = append(report.PerCustomer, line)
	}
	if err := report.readCohorts(ctx, queries, period, service.now()); err != nil {
		return report, err
	}
	return report, nil
}

// applySnapshots folds the snapshot rows into the trend, the revenue trend,
// and the latest day's plan breakdown, and returns the mean daily number of
// paying subscriptions per currency.
func (report *SubscriptionReport) applySnapshots(rows []dbgen.SubscriptionSnapshotsBetweenRow) map[string]float64 {
	base := map[string]*BaseDay{}
	mrr := map[[2]string]*RecurringRevenueDay{}
	payingDays := map[string]map[string]int64{}
	var days []string
	for _, row := range rows {
		day := dayString(row.Day)
		entry, ok := base[day]
		if !ok {
			entry = &BaseDay{Day: day}
			base[day] = entry
			days = append(days, day)
		}
		active := int64(row.Active)
		switch row.Kind {
		case "paid":
			entry.Paid += active
		case "imported":
			entry.Imported += active
		case "trial":
			entry.Trial += active
		default:
			entry.Free += active
		}
		if row.Kind == "paid" || row.Kind == "imported" {
			key := [2]string{day, row.Currency}
			revenue, ok := mrr[key]
			if !ok {
				revenue = &RecurringRevenueDay{Day: day, Currency: row.Currency}
				mrr[key] = revenue
			}
			if row.Kind == "paid" {
				revenue.MrrMinor += row.MrrMinor
			} else {
				revenue.EstimatedMinor += row.MrrMinor
			}
			if payingDays[row.Currency] == nil {
				payingDays[row.Currency] = map[string]int64{}
			}
			payingDays[row.Currency][day] += active
		}
	}
	sort.Strings(days)
	for _, day := range days {
		report.Base = append(report.Base, *base[day])
	}
	for _, revenue := range mrr {
		report.Mrr = append(report.Mrr, *revenue)
	}
	sort.Slice(report.Mrr, func(i, j int) bool {
		if report.Mrr[i].Day != report.Mrr[j].Day {
			return report.Mrr[i].Day < report.Mrr[j].Day
		}
		return report.Mrr[i].Currency < report.Mrr[j].Currency
	})
	if len(days) > 0 {
		report.LatestDay = days[len(days)-1]
		for _, row := range rows {
			if dayString(row.Day) != report.LatestDay {
				continue
			}
			report.Plans = append(report.Plans, PlanLine{
				PlanCode: row.PlanCode, BillingPeriod: row.BillingPeriod, Kind: row.Kind,
				Currency: row.Currency, Active: int64(row.Active), MrrMinor: row.MrrMinor,
			})
		}
		sort.SliceStable(report.Plans, func(i, j int) bool { return report.Plans[i].Active > report.Plans[j].Active })
	}
	averages := map[string]float64{}
	if len(days) > 0 {
		for currency, perDay := range payingDays {
			var total int64
			for _, count := range perDay {
				total += count
			}
			averages[currency] = float64(total) / float64(len(days))
		}
		first := base[days[0]]
		start := first.Paid + first.Imported
		report.Renewals.ActiveStart = &start
	}
	return averages
}

func (report *SubscriptionReport) readRenewals(ctx context.Context, queries *dbgen.Queries, period Period) error {
	rows, err := queries.SubscriptionRenewals(ctx, dbgen.SubscriptionRenewalsParams{
		Since: timestamp(period.Since), Until: timestamp(period.Until),
		GraceSeconds: RenewalGrace.Seconds(),
	})
	if err != nil {
		return err
	}
	renewals := &report.Renewals
	for _, row := range rows {
		churned := row.Ended - row.Renewed - row.Pending
		renewals.Lines = append(renewals.Lines, RenewalLine{
			PlanCode: row.PlanCode, BillingPeriod: row.BillingPeriod, Kind: row.Kind,
			Ended: row.Ended, Renewed: row.Renewed, Churned: churned, Pending: row.Pending,
			// Pending periods are undecided and stay out of the rate.
			RenewalRate: rate(row.Renewed, row.Renewed+churned),
		})
		renewals.Ended += row.Ended
		renewals.Renewed += row.Renewed
		renewals.Pending += row.Pending
		renewals.Churned += churned
	}
	renewals.RenewalRate = rate(renewals.Renewed, renewals.Renewed+renewals.Churned)
	if renewals.ActiveStart != nil {
		renewals.ChurnRate = rate(renewals.Churned, *renewals.ActiveStart)
	}
	return nil
}

func (report *SubscriptionReport) readAutoRenew(ctx context.Context, queries *dbgen.Queries, period Period) error {
	enabled, err := queries.AutoRenewEnabledCount(ctx)
	if err != nil {
		return err
	}
	report.AutoRenew.Enabled = enabled
	cycles, err := queries.AutoRenewCycles(ctx, dbgen.AutoRenewCyclesParams{
		Since: timestamp(period.Since), Until: timestamp(period.Until),
	})
	if err != nil {
		return err
	}
	for _, row := range cycles {
		decided := row.FirstTry + row.Recovered + row.Lost
		report.AutoRenew.Lines = append(report.AutoRenew.Lines, AutoRenewLine{
			Funding: row.Funding, Cycles: row.Cycles, FirstTry: row.FirstTry,
			Recovered: row.Recovered, Lost: row.Lost, InProgress: row.InProgress,
			SuccessRate:  rate(row.FirstTry+row.Recovered, decided),
			RecoveryRate: rate(row.Recovered, row.Recovered+row.Lost),
		})
	}
	codes, err := queries.AutoRenewFailureCodes(ctx, dbgen.AutoRenewFailureCodesParams{
		Since: timestamp(period.Since), Until: timestamp(period.Until),
	})
	if err != nil {
		return err
	}
	for _, row := range codes {
		report.AutoRenew.FailureCodes = append(report.AutoRenew.FailureCodes,
			FailureCount{Code: row.FailureCode, Attempts: row.Attempts})
	}
	return nil
}

// readCohorts follows the last CohortMonths monthly cohorts ending with the
// month the window ends in, whatever the window's start: a cohort table is
// read across, and a window of a week would leave nothing to read.
func (report *SubscriptionReport) readCohorts(ctx context.Context, queries *dbgen.Queries, period Period, now time.Time) error {
	end := period.Until.Add(-time.Nanosecond).In(period.location)
	if now.Before(end) {
		end = now.In(period.location)
	}
	fromMonth := time.Date(end.Year(), end.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -(CohortMonths - 1), 0)

	sizes, err := queries.PayerCohortSizes(ctx, dbgen.PayerCohortSizesParams{
		FromMonth: date(fromMonth), Timezone: period.Timezone,
	})
	if err != nil {
		return err
	}
	byMonth := map[string]*Cohort{}
	var months []string
	for _, row := range sizes {
		month := dayString(row.Cohort)
		byMonth[month] = &Cohort{Month: month, Customers: row.Customers,
			Retention: []CohortRetention{}, Revenue: []CohortRevenue{}}
		months = append(months, month)
	}
	retention, err := queries.PayerCohortRetention(ctx, dbgen.PayerCohortRetentionParams{
		FromMonth: date(fromMonth), Timezone: period.Timezone, MaxOffset: CohortMonths - 1,
	})
	if err != nil {
		return err
	}
	for _, row := range retention {
		cohort, ok := byMonth[dayString(row.Cohort)]
		if !ok {
			continue
		}
		cohort.Retention = append(cohort.Retention, CohortRetention{
			Offset: int(row.MonthOffset), Active: row.Active, Rate: rate(row.Active, cohort.Customers),
		})
	}
	revenue, err := queries.PayerCohortRevenue(ctx, dbgen.PayerCohortRevenueParams{
		FromMonth: date(fromMonth), Timezone: period.Timezone,
	})
	if err != nil {
		return err
	}
	for _, row := range revenue {
		cohort, ok := byMonth[dayString(row.Cohort)]
		if !ok {
			continue
		}
		cohort.Revenue = append(cohort.Revenue, CohortRevenue{
			Currency: row.Currency, Customers: row.Customers, RevenueMinor: row.RevenueMinor,
			LtvMinor: perUnit(row.RevenueMinor, row.Customers),
		})
	}
	sort.Strings(months)
	for _, month := range months {
		report.Cohorts = append(report.Cohorts, *byMonth[month])
	}
	return nil
}
