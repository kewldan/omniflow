package commerce

import (
	"errors"
	"testing"
	"time"
)

func TestAPeriodIsPricedFromItsListOrByTheDay(t *testing.T) {
	t.Parallel()
	options := []PeriodOption{
		{Days: 90, AmountMinor: 79900, ForPurchase: true, ForRenewal: true},
		{Days: 180, AmountMinor: 149900, ForRenewal: true},
	}
	custom := CustomDays{Min: 7, Max: 60, PricePerDayMinor: 1000}
	cases := []struct {
		name      string
		days      int
		operation string
		amount    int64
		err       error
	}{
		{"a listed period", 90, "purchase", 79900, nil},
		{"a renewal-only period on a purchase", 180, "purchase", 0, ErrPeriodUnavailable},
		{"a renewal-only period on a renewal", 180, "renewal", 149900, nil},
		{"custom days", 10, "purchase", 10000, nil},
		{"custom days past the maximum", 61, "extension", 0, ErrPeriodUnavailable},
		{"no days", 0, "purchase", 0, ErrPeriodUnavailable},
	}
	for _, testCase := range cases {
		amount, duration, err := ResolvePeriod(testCase.days, testCase.operation, options, custom)
		if !errors.Is(err, testCase.err) || amount != testCase.amount {
			t.Fatalf("%s: (%d, %v), want (%d, %v)", testCase.name, amount, err, testCase.amount, testCase.err)
		}
		if err == nil && duration != time.Duration(testCase.days)*24*time.Hour {
			t.Fatalf("%s: duration %v", testCase.name, duration)
		}
	}
}
