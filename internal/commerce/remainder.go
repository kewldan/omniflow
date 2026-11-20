package commerce

import (
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
