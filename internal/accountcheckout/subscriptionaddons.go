package accountcheckout

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/commercepg"
)

// SubscriptionAddon is one add-on a customer may buy for a live subscription,
// priced for the time its current period has left.
type SubscriptionAddon struct {
	AddonOffer
	// ChargedMinor is what one unit costs right now under the version's
	// proration rule. The order is priced again when it is created, so this is
	// what the screen shows, not what is charged.
	ChargedMinor int64
}

// SubscriptionAddons is the mid-period add-on screen for one subscription.
type SubscriptionAddons struct {
	SubscriptionID string
	// Available is false, with a Reason, when nothing can be bought now: no
	// live entitlement, or one paused with its clock stopped.
	Available     bool
	Reason        string
	EndsAt        time.Time
	Currency      string
	WalletMinor   int64
	Addons        []SubscriptionAddon
	PlanVersionID string
	// Cart is the add-on saved for this subscription until a top-up covers
	// it, when there is one.
	Cart *SavedAddon
}

// SavedAddon is an add-on saved for after a top-up: bought automatically the
// moment the wallet covers it, never for more than SavedMinor.
type SavedAddon struct {
	AddonVersionID string
	Quantity       int
	SavedMinor     int64
	// TotalMinor is what it costs now, which may be less than SavedMinor as
	// the period runs on; zero with Stale set when it can no longer be bought.
	TotalMinor   int64
	Stale        bool
	AutoPurchase bool
	ExpiresAt    time.Time
	LastFailure  string
}

// SubscriptionAddons lists what may be added to one of the customer's
// subscriptions part-way through its period. It is the web half of what the
// bot offered alone: the same offer set, the same proration, the same order.
func (service *Service) SubscriptionAddons(ctx context.Context, customerID, subscriptionID, locale string) (SubscriptionAddons, error) {
	result := SubscriptionAddons{SubscriptionID: subscriptionID, Currency: service.settings.Currency, Addons: []SubscriptionAddon{}}
	var (
		status           string
		startsAt, endsAt time.Time
	)
	err := service.store.pool.QueryRow(ctx, `SELECT e.plan_version_id::text, e.status, e.starts_at, e.ends_at
		FROM subscriptions s
		JOIN LATERAL (
			SELECT * FROM entitlements ent
			WHERE ent.subscription_id = s.id AND ent.status <> 'superseded'
			ORDER BY (ent.starts_at <= now()) DESC, ent.ends_at DESC LIMIT 1
		) e ON true
		WHERE s.id = $1::uuid AND s.user_id = $2::uuid AND s.status = 'active'`,
		subscriptionID, customerID).Scan(&result.PlanVersionID, &status, &startsAt, &endsAt)
	if errors.Is(err, pgx.ErrNoRows) {
		result.Reason = commercepg.ErrNoActiveSubscription.Error()
		return result, nil
	}
	if err != nil {
		return SubscriptionAddons{}, err
	}
	now := service.clock().UTC()
	result.EndsAt = endsAt.UTC()
	if !commerce.EntitlementLive(status, endsAt, now) || status == "paused" {
		result.Reason = commercepg.ErrNoActiveSubscription.Error()
		return result, nil
	}
	offers, err := service.store.PlanAddons(ctx, result.PlanVersionID, locale, service.settings.Currency)
	if err != nil {
		return SubscriptionAddons{}, err
	}
	for _, offer := range offers {
		charge, priceErr := commerce.PriceAddon(offer.AmountMinor, 1, offer.MaxQuantity, offer.Proration, now, startsAt, endsAt)
		if priceErr != nil {
			continue
		}
		result.Addons = append(result.Addons, SubscriptionAddon{AddonOffer: offer, ChargedMinor: charge.ChargedMinor})
	}
	result.Available = true
	if result.WalletMinor, err = service.orders.AvailableWalletBalance(ctx, customerID, service.settings.Currency); err != nil {
		return SubscriptionAddons{}, err
	}
	if result.Cart, err = service.savedAddon(ctx, customerID, subscriptionID); err != nil {
		return SubscriptionAddons{}, err
	}
	return result, nil
}

// savedAddon reads the add-on cart saved for one subscription, re-quoted.
func (service *Service) savedAddon(ctx context.Context, customerID, subscriptionID string) (*SavedAddon, error) {
	cart, found, err := service.orders.OpenAddonCart(ctx, customerID)
	if err != nil || !found || UUIDText(cart.SubscriptionID) != subscriptionID || len(cart.Addons) == 0 {
		return nil, err
	}
	saved := &SavedAddon{
		AddonVersionID: cart.Addons[0].AddonVersionID, Quantity: cart.Addons[0].Quantity,
		SavedMinor: cart.SavedPriceMinor.Int64, AutoPurchase: cart.AutoPurchase,
		ExpiresAt: cart.ExpiresAt.Time.UTC(), LastFailure: cart.LastFailure.String,
	}
	quote, err := service.orders.QuoteCart(ctx, cart)
	switch {
	case errors.Is(err, commercepg.ErrAddonUnavailable), errors.Is(err, commercepg.ErrNoActiveSubscription), errors.Is(err, commerce.ErrAddonLimit):
		saved.Stale = true
	case err != nil:
		return nil, err
	default:
		saved.TotalMinor = quote.TotalMinor
	}
	return saved, nil
}

// SaveSubscriptionAddonCart saves one add-on the wallet cannot cover yet,
// at the price the add-on screen shows now, as the customer's one cart: it is
// bought automatically once a top-up covers it.
func (service *Service) SaveSubscriptionAddonCart(ctx context.Context, customerID, subscriptionID, addonVersionID string, quantity int, locale string) (SubscriptionAddons, error) {
	if err := service.ChannelGate(ctx, customerID); err != nil {
		return SubscriptionAddons{}, err
	}
	if quantity <= 0 {
		quantity = 1
	}
	listing, err := service.SubscriptionAddons(ctx, customerID, subscriptionID, locale)
	if err != nil {
		return SubscriptionAddons{}, err
	}
	if !listing.Available {
		return SubscriptionAddons{}, commercepg.ErrNoActiveSubscription
	}
	addonVersionID = strings.TrimSpace(addonVersionID)
	for _, addon := range listing.Addons {
		if addon.AddonVersionID != addonVersionID {
			continue
		}
		if _, err = service.orders.SaveAddonCart(ctx, commercepg.AddonCartInput{
			CustomerID: customerID, SubscriptionID: subscriptionID, Currency: listing.Currency,
			AddonVersionID: addonVersionID, Quantity: quantity, SavedPriceMinor: addon.ChargedMinor * int64(quantity),
		}); err != nil {
			return SubscriptionAddons{}, err
		}
		return service.SubscriptionAddons(ctx, customerID, subscriptionID, locale)
	}
	return SubscriptionAddons{}, commercepg.ErrAddonUnavailable
}

// DiscardSubscriptionAddonCart drops the add-on saved for this subscription.
// A cart holding anything else is left alone.
func (service *Service) DiscardSubscriptionAddonCart(ctx context.Context, customerID, subscriptionID, locale string) (SubscriptionAddons, error) {
	cart, found, err := service.orders.OpenAddonCart(ctx, customerID)
	if err != nil {
		return SubscriptionAddons{}, err
	}
	if found && UUIDText(cart.SubscriptionID) == subscriptionID {
		if err = service.orders.DiscardCart(ctx, customerID); err != nil {
			return SubscriptionAddons{}, err
		}
	}
	return service.SubscriptionAddons(ctx, customerID, subscriptionID, locale)
}

// BuySubscriptionAddon opens the order for one mid-period add-on. The wallet
// settles it when it covers the price; otherwise the order waits for a
// payment the customer starts from its page, exactly as any other order.
func (service *Service) BuySubscriptionAddon(
	ctx context.Context, customerID, subscriptionID, addonVersionID string, quantity int, idempotencyKey, locale string,
) (OrderSummary, error) {
	if err := service.ChannelGate(ctx, customerID); err != nil {
		return OrderSummary{}, err
	}
	if quantity <= 0 {
		quantity = 1
	}
	order, err := service.orders.CreateAddonOrder(ctx, commercepg.AddonOrderInput{
		CustomerID: customerID, SubscriptionID: subscriptionID, Currency: service.settings.Currency,
		Addons:         []commercepg.AddonSelection{{AddonVersionID: strings.TrimSpace(addonVersionID), Quantity: quantity}},
		IdempotencyKey: "web-addon:" + idempotencyKey,
	})
	if err != nil {
		return OrderSummary{}, err
	}
	return service.store.Order(ctx, customerID, UUIDText(order.ID), locale)
}

// CloseSubscription closes one of the customer's additional subscriptions at
// their request; see commercepg.Store.CloseSubscription.
func (service *Service) CloseSubscription(ctx context.Context, customerID, subscriptionID string) error {
	return service.orders.CloseSubscription(ctx, customerID, subscriptionID)
}

// TrafficResetQuote is what restarting a subscription's traffic counter costs
// now in the settlement currency.
func (service *Service) TrafficResetQuote(ctx context.Context, customerID, subscriptionID string) (commercepg.TrafficResetQuote, error) {
	return service.orders.TrafficResetQuote(ctx, customerID, subscriptionID, service.settings.Currency)
}

// BuyTrafficReset opens the order for a paid traffic reset.
func (service *Service) BuyTrafficReset(ctx context.Context, customerID, subscriptionID, idempotencyKey, locale string) (OrderSummary, error) {
	if err := service.ChannelGate(ctx, customerID); err != nil {
		return OrderSummary{}, err
	}
	order, err := service.orders.CreateTrafficResetOrder(ctx, commercepg.TrafficResetOrderInput{
		CustomerID: customerID, SubscriptionID: subscriptionID, Currency: service.settings.Currency,
		IdempotencyKey: "web-reset:" + idempotencyKey,
	})
	if err != nil {
		return OrderSummary{}, err
	}
	return service.store.Order(ctx, customerID, UUIDText(order.ID), locale)
}
