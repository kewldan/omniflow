package panelpg

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// maxCustomTrafficGB is the largest volume a version may let a customer
// choose, the bound the database holds it to.
const maxCustomTrafficGB = 1_000_000

// validateTraffic refuses a custom traffic range the checkout could never
// sell: bounds without both ends or without a price per gigabyte, and prices
// without bounds.
func validateTraffic(input PlanVersionInput) error {
	bounded := input.CustomTrafficMinGB != nil || input.CustomTrafficMaxGB != nil
	if !bounded {
		if len(input.GBPrices) > 0 {
			return ErrValidaton
		}
		return nil
	}
	if input.CustomTrafficMinGB == nil || input.CustomTrafficMaxGB == nil || *input.CustomTrafficMinGB < 1 ||
		*input.CustomTrafficMaxGB < *input.CustomTrafficMinGB || *input.CustomTrafficMaxGB > maxCustomTrafficGB ||
		len(input.GBPrices) == 0 {
		return ErrValidaton
	}
	for currency, amount := range input.GBPrices {
		if !currencyShape(currency) || amount <= 0 {
			return ErrValidaton
		}
	}
	return nil
}

// writeTraffic stores a version's custom traffic range and its prices per
// gigabyte in the transaction that publishes it.
func writeTraffic(ctx context.Context, queries *dbgen.Queries, versionID pgtype.UUID, input PlanVersionInput) error {
	if input.CustomTrafficMinGB == nil {
		return nil
	}
	if err := queries.SetPlanVersionCustomTraffic(ctx, dbgen.SetPlanVersionCustomTrafficParams{
		PlanVersionID:      versionID,
		CustomTrafficMinGb: optionalInt4(input.CustomTrafficMinGB),
		CustomTrafficMaxGb: optionalInt4(input.CustomTrafficMaxGB),
	}); err != nil {
		return err
	}
	for currency, amount := range input.GBPrices {
		if err := queries.InsertPlanVersionGBPrice(ctx, dbgen.InsertPlanVersionGBPriceParams{
			PlanVersionID: versionID, Currency: strings.ToUpper(currency), AmountMinor: amount,
		}); err != nil {
			return err
		}
	}
	return nil
}

// versionGBPrices reads a version's prices per gigabyte back for the editor.
func versionGBPrices(ctx context.Context, queries *dbgen.Queries, version dbgen.PlanVersion) (map[string]int64, error) {
	rows, err := queries.ListPlanVersionGBPrices(ctx, version.ID)
	if err != nil {
		return nil, err
	}
	prices := make(map[string]int64, len(rows))
	for _, row := range rows {
		prices[row.Currency] = row.AmountMinor
	}
	return prices, nil
}
