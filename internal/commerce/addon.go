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
