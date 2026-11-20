package panelpg

import (
	"context"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// maxCustomTrafficGB is the largest volume a version may let a customer
// choose, the bound the database holds it to.
const maxCustomTrafficGB = 1_000_000

// TrafficPackage is one volume a selectable version is sold with, in one
// currency, priced for the version's own duration.
type TrafficPackage struct {
	GB          int32  `json:"gb"`
	Currency    string `json:"currency"`
	AmountMinor int64  `json:"amountMinor"`
}

// trafficMode is the mode a version is published with: the one the operator
// chose, or the one that matches what an editor without modes sent.
func trafficMode(input PlanVersionInput) string {
	if input.TrafficMode != "" {
		return input.TrafficMode
	}
	if len(input.TrafficPackages) > 0 || input.CustomTrafficMinGB != nil {
		return commerce.TrafficSelectable
	}
	return commerce.TrafficFixedWithTopUp
}

// validateTraffic refuses a traffic choice the checkout could never sell: an
// unknown mode; packages or a custom range on a version that is not
// selectable; a selectable version with nothing to choose; a package twice in
// one currency; bounds without both ends or without a price per gigabyte;
// and prices without bounds.
func validateTraffic(input PlanVersionInput) error {
	mode := trafficMode(input)
	switch mode {
	case commerce.TrafficFixed, commerce.TrafficFixedWithTopUp:
		if len(input.TrafficPackages) > 0 || input.CustomTrafficMinGB != nil || input.CustomTrafficMaxGB != nil {
			return ErrValidaton
		}
	case commerce.TrafficSelectable:
		if len(input.TrafficPackages) == 0 && input.CustomTrafficMinGB == nil {
			return ErrValidaton
		}
	default:
		return ErrValidaton
	}
	seen := map[string]bool{}
	for _, pack := range input.TrafficPackages {
		key := strings.ToUpper(pack.Currency) + ":" + strconv.Itoa(int(pack.GB))
		if pack.GB < 1 || pack.GB > maxCustomTrafficGB || pack.AmountMinor < 0 || !currencyShape(pack.Currency) || seen[key] {
			return ErrValidaton
		}
		seen[key] = true
	}
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
	if err := queries.SetPlanVersionTrafficMode(ctx, dbgen.SetPlanVersionTrafficModeParams{
		PlanVersionID: versionID, TrafficMode: trafficMode(input),
	}); err != nil {
		return err
	}
	for _, pack := range input.TrafficPackages {
		if err := queries.InsertPlanVersionTrafficPackage(ctx, dbgen.InsertPlanVersionTrafficPackageParams{
			PlanVersionID: versionID, TrafficGb: pack.GB, Currency: strings.ToUpper(pack.Currency), AmountMinor: pack.AmountMinor,
		}); err != nil {
			return err
		}
	}
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

// versionTrafficPackages reads a version's traffic packages back for the
// editor.
func versionTrafficPackages(ctx context.Context, queries *dbgen.Queries, version dbgen.PlanVersion) ([]TrafficPackage, error) {
	rows, err := queries.ListPlanVersionTrafficPackages(ctx, version.ID)
	if err != nil {
		return nil, err
	}
	packages := make([]TrafficPackage, 0, len(rows))
	for _, row := range rows {
		packages = append(packages, TrafficPackage{GB: row.TrafficGb, Currency: row.Currency, AmountMinor: row.AmountMinor})
	}
	return packages, nil
}
