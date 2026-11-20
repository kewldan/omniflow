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
