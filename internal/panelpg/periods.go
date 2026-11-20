package panelpg

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// PlanPeriod is one more billing period a version is sold for, in one
// currency. ForPurchase offers it on a purchase or a change and ForRenewal on
// an extension or an automatic renewal, as Bedolaga's renewal periods do.
type PlanPeriod struct {
	Days        int32  `json:"days"`
	Currency    string `json:"currency"`
	AmountMinor int64  `json:"amountMinor"`
	ForPurchase bool   `json:"forPurchase"`
	ForRenewal  bool   `json:"forRenewal"`
}

// validatePeriods refuses periods the database would and combinations that
// cannot be sold: a period offered for nothing, the same period twice in one
// currency, and custom days without both bounds and a day price.
func validatePeriods(input PlanVersionInput) error {
	seen := map[string]bool{}
	for _, period := range input.Periods {
		key := fmt.Sprintf("%s:%d", strings.ToUpper(period.Currency), period.Days)
		if period.Days < 1 || period.Days > 3650 || period.AmountMinor < 0 ||
			!currencyShape(period.Currency) || !period.ForPurchase && !period.ForRenewal || seen[key] {
			return ErrValidaton
		}
		seen[key] = true
	}
	bounded := input.CustomDaysMin != nil || input.CustomDaysMax != nil
	if !bounded {
		if len(input.DayPrices) > 0 {
			return ErrValidaton
		}
		return nil
	}
	if input.CustomDaysMin == nil || input.CustomDaysMax == nil || *input.CustomDaysMin < 1 ||
		*input.CustomDaysMax < *input.CustomDaysMin || *input.CustomDaysMax > 3650 || len(input.DayPrices) == 0 {
		return ErrValidaton
	}
	for currency, amount := range input.DayPrices {
		if !currencyShape(currency) || amount <= 0 {
			return ErrValidaton
		}
	}
	return nil
}

func currencyShape(currency string) bool {
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if len(currency) != 3 {
		return false
	}
	for _, letter := range currency {
		if letter < 'A' || letter > 'Z' {
			return false
		}
	}
	return true
}

// writePeriods stores a version's further periods and custom days in the
// transaction that publishes it.
func writePeriods(ctx context.Context, queries *dbgen.Queries, versionID pgtype.UUID, input PlanVersionInput) error {
	for _, period := range input.Periods {
		if err := queries.InsertPlanVersionPeriod(ctx, dbgen.InsertPlanVersionPeriodParams{
			PlanVersionID: versionID, Days: period.Days, Currency: strings.ToUpper(period.Currency),
			AmountMinor: period.AmountMinor, ForPurchase: period.ForPurchase, ForRenewal: period.ForRenewal,
		}); err != nil {
			return err
		}
	}
	if input.CustomDaysMin == nil {
		return nil
	}
	if err := queries.SetPlanVersionCustomDays(ctx, dbgen.SetPlanVersionCustomDaysParams{
		PlanVersionID: versionID, CustomDaysMin: optionalInt4(input.CustomDaysMin), CustomDaysMax: optionalInt4(input.CustomDaysMax),
	}); err != nil {
		return err
	}
	for currency, amount := range input.DayPrices {
		if err := queries.InsertPlanVersionDayPrice(ctx, dbgen.InsertPlanVersionDayPriceParams{
			PlanVersionID: versionID, Currency: strings.ToUpper(currency), AmountMinor: amount,
		}); err != nil {
			return err
		}
	}
	return nil
}

// versionPeriods reads a version's further periods and custom days back for
// the editor.
func versionPeriods(ctx context.Context, queries *dbgen.Queries, version dbgen.PlanVersion) ([]PlanPeriod, map[string]int64, error) {
	rows, err := queries.ListPlanVersionPeriods(ctx, version.ID)
	if err != nil {
		return nil, nil, err
	}
	periods := make([]PlanPeriod, 0, len(rows))
	for _, row := range rows {
		periods = append(periods, PlanPeriod{
			Days: row.Days, Currency: row.Currency, AmountMinor: row.AmountMinor,
			ForPurchase: row.ForPurchase, ForRenewal: row.ForRenewal,
		})
	}
	prices, err := queries.ListPlanVersionDayPrices(ctx, version.ID)
	if err != nil {
		return nil, nil, err
	}
	dayPrices := make(map[string]int64, len(prices))
	for _, price := range prices {
		dayPrices[price.Currency] = price.AmountMinor
	}
	return periods, dayPrices, nil
}
