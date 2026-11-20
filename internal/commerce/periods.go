package commerce

import (
	"errors"
	"time"
)

// ErrPeriodUnavailable refuses a billing period the plan version does not
// sell for this kind of order.
var ErrPeriodUnavailable = errors.New("period_unavailable")

// PeriodOption is one more period a plan version is sold for, beside its own
// duration, priced in one currency.
type PeriodOption struct {
	Days        int
	AmountMinor int64
	ForPurchase bool
	ForRenewal  bool
}

// CustomDays is a number of days the customer chooses, between Min and Max,
// at a price per day. A zero Min means the version sells no custom period.
type CustomDays struct {
	Min, Max         int
	PricePerDayMinor int64
}

// RenewsPeriod reports whether an operation renews the plan the customer
// holds, which is what the renewal-only periods are for.
func RenewsPeriod(operation string) bool {
	return operation == "extension" || operation == "renewal"
}

// ResolvePeriod prices a chosen number of days on a plan version: a listed
// period offered for this kind of order wins, then a custom period inside the
// version's bounds. Anything else is refused rather than priced at a guess.
func ResolvePeriod(days int, operation string, options []PeriodOption, custom CustomDays) (int64, time.Duration, error) {
	if days <= 0 {
		return 0, 0, ErrPeriodUnavailable
	}
	renewal := RenewsPeriod(operation)
	for _, option := range options {
		if option.Days != days {
			continue
		}
		if renewal && option.ForRenewal || !renewal && option.ForPurchase {
			return option.AmountMinor, time.Duration(days) * 24 * time.Hour, nil
		}
	}
	if custom.Min > 0 && custom.PricePerDayMinor > 0 && days >= custom.Min && days <= custom.Max {
		if custom.PricePerDayMinor > (1<<62)/int64(days) {
			return 0, 0, ErrInvalidAmount
		}
		return custom.PricePerDayMinor * int64(days), time.Duration(days) * 24 * time.Hour, nil
	}
	return 0, 0, ErrPeriodUnavailable
}
