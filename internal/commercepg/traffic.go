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

// ErrTrafficUnavailable is commerce.ErrTrafficUnavailable for callers that
// only know this package.
var ErrTrafficUnavailable = commerce.ErrTrafficUnavailable

// trafficOffer reads the traffic a version sells beside its own allowance,
// in the order's currency.
func trafficOffer(ctx context.Context, queries *dbgen.Queries, plan dbgen.GetPlanVersionForOrderRow) (commerce.TrafficOffer, error) {
	offer := commerce.TrafficOffer{}
	if !plan.CustomTrafficMinGb.Valid {
		return offer, nil
	}
	prices, err := queries.ListPlanVersionGBPrices(ctx, plan.ID)
	if err != nil {
		return offer, err
	}
	for _, price := range prices {
		if price.Currency == plan.Currency {
			offer = commerce.TrafficOffer{
				MinGB: int(plan.CustomTrafficMinGb.Int32), MaxGB: int(plan.CustomTrafficMaxGb.Int32),
				PricePerGBMinor: price.AmountMinor,
			}
		}
	}
	return offer, nil
}

// applyTrafficChoice prices the order's chosen traffic onto the plan row the
// quote is built from: the surcharge joins the period's price, so discounts
// apply to both, and the allowance replaces the version's own, so the order's
// snapshot says what was bought. `ownSeconds` is the version's own duration,
// which the price per gigabyte is set for.
func applyTrafficChoice(ctx context.Context, queries *dbgen.Queries, plan *dbgen.GetPlanVersionForOrderRow, gigabytes int, ownSeconds int64) error {
	if gigabytes <= 0 {
		return nil
	}
	offer, err := trafficOffer(ctx, queries, *plan)
	if err != nil {
		return err
	}
	surcharge, bytes, err := commerce.ResolveTraffic(gigabytes, offer,
		time.Duration(plan.DurationSeconds)*time.Second, time.Duration(ownSeconds)*time.Second)
	if err != nil {
		return err
	}
	if plan.AmountMinor > (1<<62)-surcharge {
		return commerce.ErrInvalidAmount
	}
	plan.AmountMinor += surcharge
	plan.TrafficAllowanceBytes = pgtype.Int8{Int64: bytes, Valid: true}
	return nil
}

// recordOrderTraffic writes the traffic an order bought onto it, when it is
// not the version's own allowance.
func recordOrderTraffic(ctx context.Context, queries *dbgen.Queries, order dbgen.Order, gigabytes int) (dbgen.Order, error) {
	if gigabytes <= 0 {
		return order, nil
	}
	value := pgtype.Int4{Int32: int32(gigabytes), Valid: true}
	if err := queries.SetOrderTraffic(ctx, dbgen.SetOrderTrafficParams{OrderID: order.ID, TrafficGb: value}); err != nil {
		return dbgen.Order{}, err
	}
	order.TrafficGb = value
	return order, nil
}

// orderTrafficAllowance is the allowance an order's entitlement carries: the
// volume it bought, or the version's own.
func orderTrafficAllowance(order dbgen.Order, versionAllowance pgtype.Int8) pgtype.Int8 {
	if order.TrafficGb.Valid && order.TrafficGb.Int32 > 0 {
		return pgtype.Int8{Int64: int64(order.TrafficGb.Int32) * commerce.GiB, Valid: true}
	}
	return versionAllowance
}

// RenewalTrafficGB is the traffic an automatic renewal of a subscription
// buys: the volume its last paid subscription order bought, when the version
// it renews at still sells that volume, and otherwise the version's own
// allowance (zero).
func (store *Store) RenewalTrafficGB(ctx context.Context, subscriptionID, planVersionID, currency string) (int, error) {
	subscription, err := parseUUID(subscriptionID)
	if err != nil {
		return 0, nil
	}
	queries := dbgen.New(store.pool)
	last, err := queries.LastOrderTraffic(ctx, subscription)
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
	offer, err := trafficOffer(ctx, queries, plan)
	if err != nil {
		return 0, err
	}
	own := time.Duration(plan.DurationSeconds) * time.Second
	if _, _, resolveErr := commerce.ResolveTraffic(int(last.Int32), offer, own, own); resolveErr != nil {
		return 0, nil
	}
	return int(last.Int32), nil
}

// RenewalSelection is what an automatic renewal of a subscription buys beside
// the version itself — its period and its traffic — so the renewal, its
// low-balance warning, and a daily charge all price the same order.
func (store *Store) RenewalSelection(ctx context.Context, subscriptionID, planVersionID, currency string) (int, int, error) {
	periodDays, err := store.RenewalPeriodDays(ctx, subscriptionID, planVersionID, currency)
	if err != nil {
		return 0, 0, err
	}
	trafficGB, err := store.RenewalTrafficGB(ctx, subscriptionID, planVersionID, currency)
	if err != nil {
		return 0, 0, err
	}
	return periodDays, trafficGB, nil
}
