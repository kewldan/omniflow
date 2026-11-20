package accountcheckout

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/omniflow/omniflow/internal/commercepg"
)

// TrafficOffer is what a version sells beside its own traffic allowance, in
// one currency: a volume the customer chooses between MinGB and MaxGB, priced
// per gigabyte for the version's own duration, when MinGB is above zero.
// OwnBytes is the version's own allowance, nil for unlimited.
type TrafficOffer struct {
	Currency        string
	OwnBytes        *int64
	MinGB, MaxGB    int
	PricePerGBMinor int64
}

// Choosable reports whether the customer has a traffic choice to make.
func (offer TrafficOffer) Choosable() bool { return offer.MinGB > 0 }

// PlanTraffic reads the traffic a version sells beside its own allowance.
func (store *Store) PlanTraffic(ctx context.Context, planVersionID, currency string) (TrafficOffer, error) {
	offer := TrafficOffer{Currency: currency}
	var (
		own, minGB, maxGB, perGB *int64
	)
	err := store.pool.QueryRow(ctx, `SELECT v.traffic_allowance_bytes, v.custom_traffic_min_gb::bigint,
			v.custom_traffic_max_gb::bigint, g.amount_minor
		FROM plan_versions v
		LEFT JOIN plan_version_gb_prices g ON g.plan_version_id = v.id AND g.currency = $2
		WHERE v.id = $1::uuid`, planVersionID, currency).Scan(&own, &minGB, &maxGB, &perGB)
	if errors.Is(err, pgx.ErrNoRows) {
		return offer, nil
	}
	if err != nil {
		return TrafficOffer{}, err
	}
	offer.OwnBytes = own
	if minGB != nil && maxGB != nil && perGB != nil {
		offer.MinGB, offer.MaxGB, offer.PricePerGBMinor = int(*minGB), int(*maxGB), *perGB
	}
	return offer, nil
}

// ChooseTraffic records the traffic volume chosen in a checkout after
// pricing it the way order creation will, so a volume the version does not
// sell is refused here with the checkout untouched. Zero returns to the
// version's own allowance. The web panel and the bot both choose through it.
func (service *Service) ChooseTraffic(ctx context.Context, session Session, gigabytes int) (Session, error) {
	if gigabytes < 0 {
		return Session{}, commercepg.ErrTrafficUnavailable
	}
	if gigabytes > 0 {
		candidate := session
		candidate.TrafficGB = gigabytes
		input, err := service.OrderInput(ctx, candidate)
		if err != nil {
			return Session{}, err
		}
		if _, err = service.orders.PreviewOrder(ctx, input); errors.Is(err, commercepg.ErrTrafficUnavailable) {
			return Session{}, err
		}
	}
	return service.store.SetCheckoutTraffic(ctx, session.ID, gigabytes)
}
