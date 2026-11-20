package accountcheckout

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/omniflow/omniflow/internal/commercepg"
)

// The "simple subscription" (Bedolaga's SIMPLE_SUBSCRIPTION_*): when the
// operator turns it on, the bot and the web store show one plan with one Buy
// button instead of the catalogue, and the button opens a checkout with the
// period, traffic, and extra devices already chosen. Everything after that is
// the ordinary checkout, so the price, the refusals, and the order are the
// ones any purchase gets.

// ErrSimpleUnavailable is returned when the simple subscription is off, or
// its plan has no live version to sell.
var ErrSimpleUnavailable = errors.New("simple_subscription_unavailable")

// SimpleSetting is the operator's setting resolved to what can be bought
// now: the plan's newest live version and the devices add-on's newest one.
type SimpleSetting struct {
	Enabled            bool
	PlanVersionID      string
	PeriodDays         int
	TrafficGB          int
	DeviceAddonVersion string
	DeviceQuantity     int
	DeviceSlots        int
}

// SimpleOffer is the one plan the store shows, priced for this customer.
type SimpleOffer struct {
	SimpleSetting
	Plan       PlanOffer
	TotalMinor int64
	Currency   string
}

// SimpleSetting reads the setting. A setting that is off, or whose plan has
// no live version, reports Enabled false.
func (store *Store) SimpleSetting(ctx context.Context) (SimpleSetting, error) {
	var setting SimpleSetting
	err := store.pool.QueryRow(ctx, `SELECT s.enabled AND v.id IS NOT NULL, COALESCE(v.id::text, ''),
			COALESCE(s.period_days, 0), COALESCE(s.traffic_gb, 0), COALESCE(av.id::text, ''), s.device_quantity,
			COALESCE(av.device_slots, 0)
		FROM simple_subscription s
		LEFT JOIN plans p ON p.id = s.plan_id AND p.archived_at IS NULL
		LEFT JOIN LATERAL (
			SELECT pv.id FROM plan_versions pv WHERE pv.plan_id = p.id AND pv.retired_at IS NULL
			ORDER BY pv.version DESC LIMIT 1
		) v ON true
		LEFT JOIN LATERAL (
			SELECT a.id, a.device_slots FROM addon_versions a
			JOIN addons ad ON ad.id = a.addon_id AND ad.archived_at IS NULL
			WHERE a.addon_id = s.device_addon_id AND a.retired_at IS NULL
			ORDER BY a.version DESC LIMIT 1
		) av ON true
		WHERE s.singleton`).Scan(&setting.Enabled, &setting.PlanVersionID, &setting.PeriodDays, &setting.TrafficGB,
		&setting.DeviceAddonVersion, &setting.DeviceQuantity, &setting.DeviceSlots)
	if errors.Is(err, pgx.ErrNoRows) {
		return SimpleSetting{}, nil
	}
	if setting.DeviceAddonVersion == "" {
		setting.DeviceQuantity = 0
	}
	return setting, err
}

// SetCheckoutAddonQuantity puts an add-on on a checkout at a quantity.
func (store *Store) SetCheckoutAddonQuantity(ctx context.Context, sessionID, addonVersionID string, quantity int) error {
	_, err := store.pool.Exec(ctx, `INSERT INTO bot_checkout_addons (checkout_id, addon_version_id, quantity)
		VALUES ($1::uuid, $2::uuid, $3)
		ON CONFLICT (checkout_id, addon_version_id) DO UPDATE SET quantity = EXCLUDED.quantity`,
		sessionID, addonVersionID, quantity)
	return err
}

// SimpleOffer reads the simple subscription priced for the customer, or
// ErrSimpleUnavailable when it is off.
func (service *Service) SimpleOffer(ctx context.Context, customerID, locale string) (SimpleOffer, error) {
	setting, err := service.store.SimpleSetting(ctx)
	if err != nil {
		return SimpleOffer{}, err
	}
	if !setting.Enabled {
		return SimpleOffer{}, ErrSimpleUnavailable
	}
	currency := service.settings.Currency
	record, err := service.store.planRecord(ctx, setting.PlanVersionID, locale, currency)
	if err != nil {
		return SimpleOffer{}, err
	}
	input := commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: setting.PlanVersionID, Currency: currency, Operation: "purchase",
		IdempotencyKey: "simple-preview", PeriodDays: setting.PeriodDays, TrafficGB: setting.TrafficGB,
		SkipWallet: true,
	}
	if setting.DeviceQuantity > 0 {
		input.Addons = []commercepg.AddonSelection{{AddonVersionID: setting.DeviceAddonVersion, Quantity: setting.DeviceQuantity}}
	}
	quote, err := service.orders.PreviewOrder(ctx, input)
	if err != nil {
		return SimpleOffer{}, err
	}
	return SimpleOffer{SimpleSetting: setting, Plan: record.offer, TotalMinor: quote.TotalMinor(), Currency: currency}, nil
}

// OpenSimple opens the checkout the Buy button stands for: the simple plan,
// with its period, traffic, and devices already chosen. A customer who
// already holds a live subscription extends it rather than being refused a
// second purchase.
func (service *Service) OpenSimple(ctx context.Context, customerID, locale string) (CheckoutView, error) {
	setting, err := service.store.SimpleSetting(ctx)
	if err != nil {
		return CheckoutView{}, err
	}
	if !setting.Enabled {
		return CheckoutView{}, ErrSimpleUnavailable
	}
	_, err = service.Open(ctx, customerID, locale, OpenRequest{PlanVersionID: setting.PlanVersionID, Operation: "purchase"})
	if errors.Is(err, ErrPurchaseOnLiveSubscription) {
		_, err = service.Open(ctx, customerID, locale, OpenRequest{PlanVersionID: setting.PlanVersionID, Operation: "extension"})
	}
	if err != nil {
		return CheckoutView{}, err
	}
	request := UpdateRequest{}
	if setting.PeriodDays > 0 {
		request.PeriodDays = &setting.PeriodDays
	}
	if setting.TrafficGB > 0 {
		request.TrafficGB = &setting.TrafficGB
	}
	if setting.DeviceQuantity > 0 {
		session, found, sessionErr := service.store.Checkout(ctx, customerID)
		if sessionErr != nil {
			return CheckoutView{}, sessionErr
		}
		if found {
			if err = service.store.SetCheckoutAddonQuantity(ctx, session.ID, setting.DeviceAddonVersion, setting.DeviceQuantity); err != nil {
				return CheckoutView{}, err
			}
		}
	}
	return service.Update(ctx, customerID, locale, request)
}
