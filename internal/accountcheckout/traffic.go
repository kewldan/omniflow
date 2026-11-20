package accountcheckout

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/commercepg"
)

// TrafficOffer is what a version sells beside its own traffic allowance, in
// one currency: its traffic mode, listed packages, and a volume the customer
// chooses between MinGB and MaxGB, priced per gigabyte for the version's own
// duration, when MinGB is above zero. OwnBytes is the version's own
// allowance, nil for unlimited. Only a selectable version sells a choice.
type TrafficOffer struct {
	Currency        string
	Mode            string
	OwnBytes        *int64
	Packages        []TrafficPackage
	MinGB, MaxGB    int
	PricePerGBMinor int64
}

// TrafficPackage is one listed volume and its price for the version's own
// duration.
type TrafficPackage struct {
	GB          int
	AmountMinor int64
}

// Choosable reports whether the customer has a traffic choice to make.
func (offer TrafficOffer) Choosable() bool {
	return offer.Mode == commerce.TrafficSelectable && (len(offer.Packages) > 0 || offer.MinGB > 0)
}

// PlanTraffic reads the traffic a version sells beside its own allowance.
func (store *Store) PlanTraffic(ctx context.Context, planVersionID, currency string) (TrafficOffer, error) {
	offer := TrafficOffer{Currency: currency, Packages: []TrafficPackage{}}
	var (
		own, minGB, maxGB, perGB *int64
	)
	err := store.pool.QueryRow(ctx, `SELECT v.traffic_mode, v.traffic_allowance_bytes, v.custom_traffic_min_gb::bigint,
			v.custom_traffic_max_gb::bigint, g.amount_minor
		FROM plan_versions v
		LEFT JOIN plan_version_gb_prices g ON g.plan_version_id = v.id AND g.currency = $2
		WHERE v.id = $1::uuid`, planVersionID, currency).Scan(&offer.Mode, &own, &minGB, &maxGB, &perGB)
	if errors.Is(err, pgx.ErrNoRows) {
		return offer, nil
	}
	if err != nil {
		return TrafficOffer{}, err
	}
	offer.OwnBytes = own
	if offer.Mode != commerce.TrafficSelectable {
		return offer, nil
	}
	if minGB != nil && maxGB != nil && perGB != nil {
		offer.MinGB, offer.MaxGB, offer.PricePerGBMinor = int(*minGB), int(*maxGB), *perGB
	}
	rows, err := store.pool.Query(ctx, `SELECT traffic_gb, amount_minor FROM plan_version_traffic_packages
		WHERE plan_version_id = $1::uuid AND currency = $2 ORDER BY traffic_gb`, planVersionID, currency)
	if err != nil {
		return TrafficOffer{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var pack TrafficPackage
		if err = rows.Scan(&pack.GB, &pack.AmountMinor); err != nil {
			return TrafficOffer{}, err
		}
		offer.Packages = append(offer.Packages, pack)
	}
	return offer, rows.Err()
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
