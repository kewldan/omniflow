package accountcheckout

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/omniflow/omniflow/internal/commerce"
)

// PeriodChoice is one listed billing period and its price.
type PeriodChoice struct {
	Days        int
	AmountMinor int64
	// Highlighted is the period the operator recommends.
	Highlighted bool
}

// PeriodOffer is what a version is sold for beside its own duration, in one
// currency and for one kind of order: listed periods, and a custom number of
// days priced per day when MinDays is above zero.
type PeriodOffer struct {
	Currency         string
	Choices          []PeriodChoice
	MinDays, MaxDays int
	PricePerDayMinor int64
}

// PlanPeriods reads a version's other billing periods for an operation. A
// renewal-only period is offered on an extension and not on a purchase.
func (store *Store) PlanPeriods(ctx context.Context, planVersionID, currency, operation string) (PeriodOffer, error) {
	offer := PeriodOffer{Currency: currency, Choices: []PeriodChoice{}}
	rows, err := store.pool.Query(ctx, `SELECT days, amount_minor, for_purchase, for_renewal, highlighted
		FROM plan_version_periods WHERE plan_version_id = $1::uuid AND currency = $2
		ORDER BY days`, planVersionID, currency)
	if err != nil {
		return PeriodOffer{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			choice                  PeriodChoice
			days                    int32
			forPurchase, forRenewal bool
		)
		if err = rows.Scan(&days, &choice.AmountMinor, &forPurchase, &forRenewal, &choice.Highlighted); err != nil {
			return PeriodOffer{}, err
		}
		if periodSoldFor(operation, forPurchase, forRenewal) {
			choice.Days = int(days)
			offer.Choices = append(offer.Choices, choice)
		}
	}
	if err = rows.Err(); err != nil {
		return PeriodOffer{}, err
	}
	var minDays, maxDays, perDay *int64
	err = store.pool.QueryRow(ctx, `SELECT v.custom_days_min::bigint, v.custom_days_max::bigint, d.amount_minor
		FROM plan_versions v
		LEFT JOIN plan_version_day_prices d ON d.plan_version_id = v.id AND d.currency = $2
		WHERE v.id = $1::uuid`, planVersionID, currency).Scan(&minDays, &maxDays, &perDay)
	if errors.Is(err, pgx.ErrNoRows) {
		return offer, nil
	}
	if err != nil {
		return PeriodOffer{}, err
	}
	if minDays != nil && maxDays != nil && perDay != nil {
		offer.MinDays, offer.MaxDays, offer.PricePerDayMinor = int(*minDays), int(*maxDays), *perDay
	}
	return offer, nil
}

// attachPeriods lists, on each offer, the further periods its first
// operation may be bought for in the offer's own currency: one query for the
// whole catalogue rather than one per card.
func (store *Store) attachPeriods(ctx context.Context, offers []PlanOffer) error {
	versions := make([]string, 0, len(offers))
	for _, offer := range offers {
		if len(offer.Operations) > 0 {
			versions = append(versions, offer.PlanVersionID)
		}
	}
	if len(versions) == 0 {
		return nil
	}
	rows, err := store.pool.Query(ctx, `SELECT plan_version_id::text, currency, days, amount_minor,
			for_purchase, for_renewal, highlighted
		FROM plan_version_periods WHERE plan_version_id = ANY($1::uuid[])
		ORDER BY days`, versions)
	if err != nil {
		return err
	}
	defer rows.Close()
	index := make(map[string]int, len(offers))
	for position, offer := range offers {
		index[offer.PlanVersionID] = position
	}
	for rows.Next() {
		var (
			version, currency       string
			days                    int32
			choice                  PeriodChoice
			forPurchase, forRenewal bool
		)
		if err = rows.Scan(&version, &currency, &days, &choice.AmountMinor,
			&forPurchase, &forRenewal, &choice.Highlighted); err != nil {
			return err
		}
		position, found := index[version]
		if !found {
			continue
		}
		offer := &offers[position]
		if currency != offer.Currency || len(offer.Operations) == 0 {
			continue
		}
		if periodSoldFor(offer.Operations[0], forPurchase, forRenewal) {
			choice.Days = int(days)
			offer.Periods = append(offer.Periods, choice)
		}
	}
	return rows.Err()
}

// periodSoldFor is whether a listed period may be bought by an operation: a
// renewal-only period renews, and a purchase-only one starts or changes a
// plan.
func periodSoldFor(operation string, forPurchase, forRenewal bool) bool {
	if commerce.RenewsPeriod(operation) {
		return forRenewal
	}
	return forPurchase
}

// PeriodPrices is what one listed period of a version costs in each currency
// it is priced in, for an operation. A currency without the period is absent,
// so a payment method that settles only there is not offered for it.
func (store *Store) PeriodPrices(ctx context.Context, planVersionID string, days int, operation string) (map[string]int64, error) {
	column := "for_purchase"
	if commerce.RenewsPeriod(operation) {
		column = "for_renewal"
	}
	rows, err := store.pool.Query(ctx, `SELECT currency, amount_minor FROM plan_version_periods
		WHERE plan_version_id = $1::uuid AND days = $2 AND `+column+` ORDER BY currency`, planVersionID, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	prices := map[string]int64{}
	for rows.Next() {
		var (
			currency string
			amount   int64
		)
		if err = rows.Scan(&currency, &amount); err != nil {
			return nil, err
		}
		prices[currency] = amount
	}
	return prices, rows.Err()
}
