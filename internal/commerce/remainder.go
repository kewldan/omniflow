package commerce

import (
	"errors"
	"math/bits"
	"time"
)

// RemainderCredit is the unused value of something the customer paid for,
// handed back when a change takes it away before its period ends.
type RemainderCredit struct {
	// CreditMinor is what is handed back. It never exceeds BasisMinor.
	CreditMinor int64
	// BasisMinor is what the customer actually paid for the thing credited.
	BasisMinor int64
	// Remaining is the time it still had, and Period the time the payment
	// bought; the credit is BasisMinor × min(Remaining, Period) ÷ Period.
	Remaining time.Duration
	Period    time.Duration
}

// CreditRemainder prices the unused remainder of a paid period: what was paid,
// scaled by the share of the bought time still to run, rounded down.
//
// The rules, each one a reason a credit could otherwise exceed what the
// customer is owed:
//
//   - Rounding is down. A credit is money leaving the platform, and a minor
//     unit rounded up on every change would add up to a refund nobody paid.
//   - Time beyond the bought period is not credited. An entitlement that
//     carried earlier time forward (an `extend` upgrade, a renewal of a
//     subscription with days left, a gift or a code) runs longer than its
//     order paid for; the earliest time is used first, so whatever is left is
//     the bought time, up to the bought period and never more.
//   - A period that has not started, has ended, or was never paid for credits
//     nothing.
//
// The multiplication is done in 128 bits, so a long period and a large price
// cannot overflow into a nonsensical figure.
func CreditRemainder(paidMinor int64, period time.Duration, now, startsAt, effectiveEndsAt time.Time) RemainderCredit {
	credit := RemainderCredit{Period: period}
	if paidMinor <= 0 || period <= 0 || now.Before(startsAt) || !effectiveEndsAt.After(now) {
		return credit
	}
	credit.BasisMinor = paidMinor
	credit.Remaining = effectiveEndsAt.Sub(now)
	counted := min(credit.Remaining, period)
	credit.CreditMinor = scaleRoundingDown(paidMinor, int64(counted/time.Second), int64(period/time.Second))
	return credit
}

// scaleRoundingDown computes value × numerator ÷ denominator, rounding down,
// with the intermediate product held in 128 bits. It never returns more than
// value.
func scaleRoundingDown(value, numerator, denominator int64) int64 {
	if value <= 0 || numerator <= 0 || denominator <= 0 {
		return 0
	}
	if numerator >= denominator {
		return value
	}
	high, low := bits.Mul64(uint64(value), uint64(numerator))
	quotient, _ := bits.Div64(high, low, uint64(denominator))
	return min(int64(quotient), value)
}

// ProratesRemainder reports whether a plan change throws the current period
// away and so, with proration on, hands its remainder back: an upgrade that
// replaces the period and a downgrade that takes effect now. An `extend`
// upgrade keeps the remaining time and an `at_expiry` downgrade waits for it,
// so neither has anything to credit.
func ProratesRemainder(operation, upgradePolicy, downgradePolicy string) bool {
	switch operation {
	case "upgrade":
		return upgradePolicy == "replace"
	case "downgrade":
		return downgradePolicy == "immediate"
	default:
		return false
	}
}

// ApplyCredit splits a credit against what an order owes. Applied is the part
// the order spends, and ToWallet the part that stays in the wallet because the
// new price is lower than what was handed back.
func ApplyCredit(creditMinor, dueMinor int64) (applied, toWallet int64) {
	if creditMinor <= 0 {
		return 0, 0
	}
	applied = min(creditMinor, max(dueMinor, 0))
	return applied, creditMinor - applied
}

// PackBasis is what one add-on line of an order actually cost the customer:
// the line's charge, scaled down by whatever share of the order was not paid
// (a discount, a refund), and never more than the charge. A line on an order
// that took nothing has nothing to hand back.
func PackBasis(chargedMinor, orderPaidMinor, orderSubtotalMinor int64) int64 {
	if chargedMinor <= 0 || orderPaidMinor <= 0 || orderSubtotalMinor <= 0 {
		return 0
	}
	if orderPaidMinor >= orderSubtotalMinor {
		return chargedMinor
	}
	return scaleRoundingDown(chargedMinor, orderPaidMinor, orderSubtotalMinor)
}

// CreditPack prices the unused remainder of an add-on pack that counted from
// appliedAt and would have counted until endsAt: its basis scaled by the share
// of that span still to run, rounded down.
func CreditPack(basisMinor int64, now, appliedAt, endsAt time.Time) RemainderCredit {
	return CreditRemainder(basisMinor, endsAt.Sub(appliedAt), now, appliedAt, endsAt)
}

var (
	// ErrTrafficChangeUnavailable refuses a traffic package change on a
	// subscription with unlimited traffic, a paused or lapsed one, or one
	// whose plan offers no traffic packages.
	ErrTrafficChangeUnavailable = errors.New("traffic_change_unavailable")
	// ErrCapacityChangeSame refuses a change that would leave the capacity
	// exactly where it is.
	ErrCapacityChangeSame = errors.New("capacity_change_same")
)

var (
	// ErrDeviceReductionUnavailable refuses a device reduction on a
	// subscription with unlimited devices, one with no bought slots to give
	// back, or a target below what the plan itself includes.
	ErrDeviceReductionUnavailable = errors.New("device_reduction_unavailable")
	// ErrDevicesOverLimit refuses a device reduction while more devices are
	// connected than the new limit allows. The customer disconnects some
	// first; Remnawave holds the devices and Omniflow does not pick which go.
	ErrDevicesOverLimit = errors.New("devices_over_limit")
)

// ShareOf is part of a whole amount, rounded down: what `part` units of
// `total` are worth when all of them are worth `amountMinor`.
func ShareOf(amountMinor int64, part, total int) int64 {
	if part <= 0 || total <= 0 {
		return 0
	}
	return scaleRoundingDown(amountMinor, int64(part), int64(total))
}

// ErrSquadRemovalUnavailable refuses removing a squad add-on the
// subscription does not hold, or one already removed.
var ErrSquadRemovalUnavailable = errors.New("squad_removal_unavailable")
