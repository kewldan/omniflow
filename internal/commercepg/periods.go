package commercepg

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// ErrPeriodUnavailable is commerce.ErrPeriodUnavailable for callers that
// only know this package.
var ErrPeriodUnavailable = commerce.ErrPeriodUnavailable

// periodOptions reads what a version sells beside its own duration, in one
// currency: its listed periods and its custom-day bounds and day price.
func periodOptions(ctx context.Context, queries *dbgen.Queries, plan dbgen.GetPlanVersionForOrderRow) ([]commerce.PeriodOption, commerce.CustomDays, error) {
	rows, err := queries.ListPlanVersionPeriods(ctx, plan.ID)
	if err != nil {
		return nil, commerce.CustomDays{}, err
	}
	options := make([]commerce.PeriodOption, 0, len(rows))
	for _, row := range rows {
		if row.Currency != plan.Currency {
			continue
		}
		options = append(options, commerce.PeriodOption{
			Days: int(row.Days), AmountMinor: row.AmountMinor,
			ForPurchase: row.ForPurchase, ForRenewal: row.ForRenewal,
		})
	}
	custom := commerce.CustomDays{}
	if plan.CustomDaysMin.Valid {
		prices, priceErr := queries.ListPlanVersionDayPrices(ctx, plan.ID)
		if priceErr != nil {
			return nil, commerce.CustomDays{}, priceErr
		}
		for _, price := range prices {
			if price.Currency == plan.Currency {
				custom = commerce.CustomDays{
					Min: int(plan.CustomDaysMin.Int32), Max: int(plan.CustomDaysMax.Int32),
					PricePerDayMinor: price.AmountMinor,
				}
			}
		}
	}
	return options, custom, nil
}

// periodPrice prices the order's chosen period on its version.
func (store *Store) periodPrice(ctx context.Context, queries *dbgen.Queries, plan dbgen.GetPlanVersionForOrderRow, input CreateOrderInput) (int64, int64, error) {
	options, custom, err := periodOptions(ctx, queries, plan)
	if err != nil {
		return 0, 0, err
	}
	amount, duration, err := commerce.ResolvePeriod(input.PeriodDays, input.Operation, options, custom)
	if err != nil {
		return 0, 0, err
	}
	return amount, int64(duration / time.Second), nil
}

// recordOrderPeriod writes the period an order bought onto it, when it is not
// the version's own, and returns the order as settlement will read it.
func recordOrderPeriod(ctx context.Context, queries *dbgen.Queries, order dbgen.Order, input CreateOrderInput, durationSeconds int64) (dbgen.Order, error) {
	if input.PeriodDays <= 0 {
		return order, nil
	}
	if err := queries.SetOrderPeriod(ctx, dbgen.SetOrderPeriodParams{OrderID: order.ID, PeriodSeconds: pgtype.Int8{Int64: durationSeconds, Valid: true}}); err != nil {
		return dbgen.Order{}, err
	}
	order.PeriodSeconds = pgtype.Int8{Int64: durationSeconds, Valid: true}
	return order, nil
}

// RenewalPeriodDays is the period an automatic renewal of a subscription
// repeats: the one its last paid order bought, when the version still offers
// it for renewal, and otherwise the version's own (zero).
func (store *Store) RenewalPeriodDays(ctx context.Context, subscriptionID, planVersionID, currency string) (int, error) {
	subscription, err := parseUUID(subscriptionID)
	if err != nil {
		return 0, nil
	}
	queries := dbgen.New(store.pool)
	last, err := queries.LastOrderPeriod(ctx, subscription)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !last.Valid {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	versionID, err := parseUUID(planVersionID)
	if err != nil {
		return 0, nil
	}
	plan, err := queries.GetPlanVersionForOrder(ctx, dbgen.GetPlanVersionForOrderParams{PlanVersionID: versionID, Currency: currency})
	if err != nil {
		return 0, nil
	}
	days := int(last.Int64 / 86_400)
	options, custom, err := periodOptions(ctx, queries, plan)
	if err != nil {
		return 0, err
	}
	if _, _, resolveErr := commerce.ResolvePeriod(days, "renewal", options, custom); resolveErr != nil {
		return 0, nil
	}
	return days, nil
}
