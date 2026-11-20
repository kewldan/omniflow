package accountcheckout

import (
	"context"
	"errors"

	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/commercepg"
)

// TrafficPackageOption is one traffic package a subscription may switch to,
// or none at all when AddonVersionID is empty, priced for the time left.
type TrafficPackageOption struct {
	AddonVersionID string
	Name           string
	TrafficBytes   int64
	// Direction is "up" for more traffic than the current packages, "down"
	// for less.
	Direction string
	Quote     commercepg.CapacityChangeQuote
}

// TrafficPackages is the traffic package screen of one subscription.
type TrafficPackages struct {
	Available           bool
	Reason              string
	Currency            string
	CurrentTrafficBytes int64
	Options             []TrafficPackageOption
}

// TrafficPackages lists the traffic packages a live subscription may switch
// to, each priced against what is left of the packages it has: a bigger one
// costs its prorated price less that remainder, a smaller one credits the
// difference to the wallet.
func (service *Service) TrafficPackages(ctx context.Context, customerID, subscriptionID, locale string) (TrafficPackages, error) {
	result := TrafficPackages{Currency: service.settings.Currency, Options: []TrafficPackageOption{}}
	input := commercepg.CapacityChangeInput{CustomerID: customerID, SubscriptionID: subscriptionID, Currency: service.settings.Currency}
	none, err := service.orders.TrafficChangeQuote(ctx, input)
	switch {
	case err == nil:
		result.CurrentTrafficBytes = none.CurrentTrafficBytes
		result.Options = append(result.Options, TrafficPackageOption{Direction: "down", Quote: none})
	case errors.Is(err, commerce.ErrCapacityChangeSame):
	case errors.Is(err, commerce.ErrTrafficChangeUnavailable), errors.Is(err, commercepg.ErrNoActiveSubscription):
		result.Reason = unwrapReason(err)
		return result, nil
	default:
		return TrafficPackages{}, err
	}
	addons, err := service.SubscriptionAddons(ctx, customerID, subscriptionID, locale)
	if err != nil {
		return TrafficPackages{}, err
	}
	for _, offer := range addons.Addons {
		if offer.Kind != "traffic" || offer.TrafficBytes == nil {
			continue
		}
		input.AddonVersionID = offer.AddonVersionID
		quote, quoteErr := service.orders.TrafficChangeQuote(ctx, input)
		if quoteErr != nil {
			// The current package itself, or one past the cap on bought
			// traffic: not something the customer can switch to.
			continue
		}
		direction := "up"
		if quote.TargetTrafficBytes < quote.CurrentTrafficBytes {
			direction = "down"
		}
		result.CurrentTrafficBytes = quote.CurrentTrafficBytes
		result.Options = append(result.Options, TrafficPackageOption{
			AddonVersionID: offer.AddonVersionID, Name: offer.Name,
			TrafficBytes: *offer.TrafficBytes, Direction: direction, Quote: quote,
		})
	}
	result.Available = len(result.Options) > 0
	if !result.Available {
		result.Reason = commerce.ErrTrafficChangeUnavailable.Error()
	}
	return result, nil
}

// BuyTrafficChange opens the order that switches a subscription's traffic
// package. An empty addonVersionID drops the current packages.
func (service *Service) BuyTrafficChange(ctx context.Context, customerID, subscriptionID, addonVersionID, idempotencyKey, locale string) (OrderSummary, error) {
	if err := service.ChannelGate(ctx, customerID); err != nil {
		return OrderSummary{}, err
	}
	order, err := service.orders.CreateTrafficChangeOrder(ctx, commercepg.CapacityChangeInput{
		CustomerID: customerID, SubscriptionID: subscriptionID, Currency: service.settings.Currency,
		AddonVersionID: addonVersionID, IdempotencyKey: "web-traffic:" + idempotencyKey,
	})
	if err != nil {
		return OrderSummary{}, err
	}
	return service.store.Order(ctx, customerID, UUIDText(order.ID), locale)
}

// unwrapReason is the stable reason code of a refusal.
func unwrapReason(err error) string {
	for _, known := range []error{commerce.ErrTrafficChangeUnavailable, commercepg.ErrNoActiveSubscription, commerce.ErrCapacityChangeSame} {
		if errors.Is(err, known) {
			return known.Error()
		}
	}
	return err.Error()
}

// DeviceReductionOption is one lower device limit, priced.
type DeviceReductionOption struct {
	DeviceLimit int
	Quote       commercepg.CapacityChangeQuote
}

// DeviceReduction is the device limit screen of one subscription.
type DeviceReduction struct {
	Available    bool
	Reason       string
	Currency     string
	CurrentLimit int
	MinimumLimit int
	Connected    int
	Options      []DeviceReductionOption
}

// DeviceReduction lists the lower device limits a subscription may move to by
// giving back slots bought as add-ons, each with the credit it would hand
// back and how many of the connected devices it would leave over.
func (service *Service) DeviceReduction(ctx context.Context, customerID, subscriptionID string, connected int) (DeviceReduction, error) {
	result := DeviceReduction{Currency: service.settings.Currency, Connected: connected, Options: []DeviceReductionOption{}}
	current, minimum, ok, err := service.orders.DeviceLimitRange(ctx, customerID, subscriptionID)
	if err != nil {
		return DeviceReduction{}, err
	}
	if !ok {
		result.Reason = commerce.ErrDeviceReductionUnavailable.Error()
		return result, nil
	}
	result.CurrentLimit, result.MinimumLimit = current, minimum
	for limit := current - 1; limit >= minimum; limit-- {
		quote, quoteErr := service.orders.DeviceReductionQuote(ctx, commercepg.CapacityChangeInput{
			CustomerID: customerID, SubscriptionID: subscriptionID, Currency: service.settings.Currency,
			DeviceLimit: limit, ConnectedDevices: connected,
		})
		if quoteErr != nil {
			return DeviceReduction{}, quoteErr
		}
		result.Options = append(result.Options, DeviceReductionOption{DeviceLimit: limit, Quote: quote})
	}
	result.Available = len(result.Options) > 0
	return result, nil
}

// ReduceDevices opens the order that lowers a subscription's device limit. It
// is refused while more devices are connected than the new limit allows.
func (service *Service) ReduceDevices(ctx context.Context, customerID, subscriptionID string, limit, connected int, idempotencyKey, locale string) (OrderSummary, error) {
	order, err := service.orders.CreateDeviceReductionOrder(ctx, commercepg.CapacityChangeInput{
		CustomerID: customerID, SubscriptionID: subscriptionID, Currency: service.settings.Currency,
		DeviceLimit: limit, ConnectedDevices: connected, IdempotencyKey: "web-devices:" + idempotencyKey,
	})
	if err != nil {
		return OrderSummary{}, err
	}
	return service.store.Order(ctx, customerID, UUIDText(order.ID), locale)
}

// SquadAddonRemoval is one paid squad add-on a subscription may remove.
type SquadAddonRemoval struct {
	commercepg.SquadRemovalOption
	Name string
}

// SquadAddonRemovals lists the paid squad add-ons of a live subscription with
// what removing each hands back now.
func (service *Service) SquadAddonRemovals(ctx context.Context, customerID, subscriptionID, locale string) ([]SquadAddonRemoval, error) {
	options, err := service.orders.SquadRemovals(ctx, customerID, subscriptionID, service.settings.Currency)
	if err != nil {
		return nil, err
	}
	result := make([]SquadAddonRemoval, 0, len(options))
	for _, option := range options {
		name := ""
		if lookupErr := service.store.pool.QueryRow(ctx, `SELECT COALESCE(
				(SELECT l.name FROM addon_localizations l WHERE l.addon_id = v.addon_id AND l.locale = $2),
				(SELECT l.name FROM addon_localizations l WHERE l.addon_id = v.addon_id ORDER BY l.locale LIMIT 1), '')
			FROM addon_versions v WHERE v.id = $1::uuid`, option.AddonVersionID, normalizeLocale(locale)).Scan(&name); lookupErr != nil {
			return nil, lookupErr
		}
		result = append(result, SquadAddonRemoval{SquadRemovalOption: option, Name: name})
	}
	return result, nil
}

// RemoveSquadAddon opens the order that removes one paid squad add-on.
func (service *Service) RemoveSquadAddon(ctx context.Context, customerID, subscriptionID, packID, idempotencyKey, locale string) (OrderSummary, error) {
	order, err := service.orders.CreateSquadRemovalOrder(ctx, commercepg.CapacityChangeInput{
		CustomerID: customerID, SubscriptionID: subscriptionID, Currency: service.settings.Currency,
		AddonPackID: packID, IdempotencyKey: "web-squad:" + idempotencyKey,
	})
	if err != nil {
		return OrderSummary{}, err
	}
	return service.store.Order(ctx, customerID, UUIDText(order.ID), locale)
}
