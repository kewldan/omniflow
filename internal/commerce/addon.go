package commerce

import (
	"errors"
	"math/bits"
	"time"
)

// Proration decides what a mid-period add-on costs. Every rule is explicit:
// Omniflow never guesses a partial price from the remaining time unless the
// add-on version says so.
type Proration string

const (
	// ProrationFullPrice charges the catalog price whatever the remaining time
	// is. It suits capacity that does not decay, such as extra device slots.
	ProrationFullPrice Proration = "full_price"
	// ProrationRemainingPeriod charges the catalog price scaled by the fraction
	// of the current period that is left, rounded up to the next minor unit.
	ProrationRemainingPeriod Proration = "remaining_period"
	// ProrationDailyRate treats the catalog price as one full period, derives a
	// per-day rate rounded up, and charges it for each whole or partial day that
	// remains.
	ProrationDailyRate Proration = "daily_rate"
)

var (
	ErrUnsupportedProration = errors.New("unsupported proration rule")
	ErrAddonQuantity        = errors.New("add-on quantity is out of range")
)

// AddonCharge is a priced add-on line ready to be written to an order.
type AddonCharge struct {
	UnitAmountMinor int64
	Quantity        int
	ChargedMinor    int64
	Proration       Proration
}

// PriceAddon computes what one add-on line costs for a subscription period that
// runs from startsAt to endsAt, purchased at now.
//
// A purchase made before the period starts, or against a period with no
// remaining time, falls back to the full catalog price: charging zero for an
// add-on that will still be delivered would give it away.
func PriceAddon(unitAmountMinor int64, quantity, maxQuantity int, rule Proration, now, startsAt, endsAt time.Time) (AddonCharge, error) {
	if unitAmountMinor < 0 {
		return AddonCharge{}, ErrInvalidAmount
	}
	if quantity <= 0 || maxQuantity > 0 && quantity > maxQuantity {
		return AddonCharge{}, ErrAddonQuantity
	}
	total := unitAmountMinor * int64(quantity)
	if unitAmountMinor > 0 && total/int64(quantity) != unitAmountMinor {
		return AddonCharge{}, ErrInvalidAmount
	}
	charge := AddonCharge{UnitAmountMinor: unitAmountMinor, Quantity: quantity, ChargedMinor: total, Proration: rule}
	switch rule {
	case ProrationFullPrice:
		return charge, nil
	case ProrationRemainingPeriod, ProrationDailyRate:
	default:
		return AddonCharge{}, ErrUnsupportedProration
	}
	period := endsAt.Sub(startsAt)
	remaining := endsAt.Sub(now)
	if period <= 0 || remaining <= 0 || remaining >= period {
		return charge, nil
	}
	if rule == ProrationRemainingPeriod {
		charge.ChargedMinor = scaleRoundingUp(total, int64(remaining/time.Second), int64(period/time.Second))
		return charge, nil
	}
	periodDays := divideRoundingUp(int64(period), int64(24*time.Hour))
	remainingDays := divideRoundingUp(int64(remaining), int64(24*time.Hour))
	if periodDays <= 0 {
		return charge, nil
	}
	charge.ChargedMinor = min(divideRoundingUp(total, periodDays)*remainingDays, total)
	return charge, nil
}

// divideRoundingUp rounds a non-negative division away from zero so a prorated
// charge never rounds down to nothing the customer did not pay for.
func divideRoundingUp(numerator, denominator int64) int64 {
	if denominator <= 0 || numerator <= 0 {
		return 0
	}
	result := numerator / denominator
	if numerator%denominator != 0 {
		result++
	}
	return result
}

// scaleRoundingUp computes value × numerator ÷ denominator, rounding up, with
// the intermediate product held in 128 bits. A long period and a large price
// would overflow int64 in the obvious formulation, which would silently produce
// a nonsensical charge.
func scaleRoundingUp(value, numerator, denominator int64) int64 {
	if value <= 0 || numerator <= 0 || denominator <= 0 {
		return 0
	}
	if numerator >= denominator {
		return value
	}
	high, low := bits.Mul64(uint64(value), uint64(numerator))
	quotient, remainder := bits.Div64(high, low, uint64(denominator))
	if remainder != 0 {
		quotient++
	}
	return min(int64(quotient), value)
}

// AddonCapacity is what an add-on adds to an entitlement once it is fulfilled.
type AddonCapacity struct {
	TrafficBytes int64
	DeviceSlots  int
	SquadIDs     []string
}

// AddonApplies reports whether an add-on grants anything on a subscription
// whose traffic and device limits are limited or not.
//
// Capacity on top of "unlimited" is meaningless: settlement leaves an
// unlimited allowance unlimited, so a traffic pack on an unlimited plan used to
// be listed, charged, and recorded, and grant nothing. An add-on applies when
// at least one thing it carries lands — traffic on a limited allowance, slots
// on a limited device count, or any squad.
func AddonApplies(trafficBytes int64, deviceSlots, squadCount int, trafficLimited, devicesLimited bool) bool {
	return squadCount > 0 ||
		trafficBytes > 0 && trafficLimited ||
		deviceSlots > 0 && devicesLimited
}

// ErrAddonLimit refuses an add-on that would take a subscription past a cap:
// the installation's device ceiling, or the plan's cap on bought traffic.
var ErrAddonLimit = errors.New("addon_limit_reached")

// AddonLimits are the caps an add-on purchase is checked against. A nil cap
// is no cap.
type AddonLimits struct {
	// MaxDevices is the installation's device ceiling.
	MaxDevices *int
	// MaxBoughtTraffic caps the traffic add-ons may add to one entitlement;
	// BoughtTraffic is what is already bought there and still counting.
	MaxBoughtTraffic *int64
	BoughtTraffic    int64
}

// CheckAddonLimits refuses capacity that would cross a cap. `devices` is the
// subscription's device limit now, nil when unlimited; an unlimited device
// count has no ceiling to cross because no slot is added to it.
func CheckAddonLimits(limits AddonLimits, devices *int, addedSlots int, addedTraffic int64) error {
	if addedSlots > 0 && devices != nil && limits.MaxDevices != nil && *devices+addedSlots > *limits.MaxDevices {
		return ErrAddonLimit
	}
	if addedTraffic > 0 && limits.MaxBoughtTraffic != nil && limits.BoughtTraffic+addedTraffic > *limits.MaxBoughtTraffic {
		return ErrAddonLimit
	}
	return nil
}

// Capacity multiplies one add-on version's capacity by the purchased quantity.
// Squads are a set, so quantity never duplicates them.
func Capacity(trafficBytes int64, deviceSlots, quantity int, squadIDs []string) AddonCapacity {
	if quantity <= 0 {
		quantity = 1
	}
	capacity := AddonCapacity{SquadIDs: squadIDs}
	if trafficBytes > 0 {
		capacity.TrafficBytes = trafficBytes * int64(quantity)
	}
	if deviceSlots > 0 {
		capacity.DeviceSlots = deviceSlots * quantity
	}
	return capacity
}

// ErrTrafficResetUnavailable refuses a paid traffic reset the subscription's
// plan does not sell, or one on unlimited traffic, which has no counter worth
// paying to restart.
var ErrTrafficResetUnavailable = errors.New("traffic_reset_unavailable")

// TrafficResetPrice is what restarting the traffic counter costs: a share of
// the plan version's own price, in basis points, rounded up so a cheap plan
// never resets for nothing.
func TrafficResetPrice(planAmountMinor int64, bps int) (int64, error) {
	if bps <= 0 || planAmountMinor < 0 {
		return 0, ErrTrafficResetUnavailable
	}
	if planAmountMinor > (1<<62)/int64(bps) {
		return 0, ErrInvalidAmount
	}
	return divideRoundingUp(planAmountMinor*int64(bps), 10_000), nil
}
