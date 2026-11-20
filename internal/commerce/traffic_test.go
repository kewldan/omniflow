package commerce

import (
	"errors"
	"testing"
	"time"
)

func TestResolveTraffic(t *testing.T) {
	day := 24 * time.Hour
	offer := TrafficOffer{MinGB: 10, MaxGB: 500, PricePerGBMinor: 300}
	cases := []struct {
		name      string
		gigabytes int
		offer     TrafficOffer
		period    time.Duration
		amount    int64
		err       error
	}{
		{name: "own period", gigabytes: 50, offer: offer, period: 30 * day, amount: 15_000},
		{name: "longer period scales up", gigabytes: 50, offer: offer, period: 90 * day, amount: 45_000},
		{name: "shorter period rounds up", gigabytes: 10, offer: offer, period: 7 * day, amount: 700},
		{name: "below the range", gigabytes: 9, offer: offer, period: 30 * day, err: ErrTrafficUnavailable},
		{name: "above the range", gigabytes: 501, offer: offer, period: 30 * day, err: ErrTrafficUnavailable},
		{name: "nothing chosen", gigabytes: 0, offer: offer, period: 30 * day, err: ErrTrafficUnavailable},
		{name: "not sold", gigabytes: 50, offer: TrafficOffer{}, period: 30 * day, err: ErrTrafficUnavailable},
		{
			name: "a package wins over the range", gigabytes: 50, period: 60 * day, amount: 2_000,
			offer: TrafficOffer{Packages: []TrafficPackage{{GB: 50, PriceMinor: 1_000}}, MinGB: 10, MaxGB: 500, PricePerGBMinor: 300},
		},
		{
			name: "a package without a range", gigabytes: 25, period: 30 * day, amount: 0,
			offer: TrafficOffer{Packages: []TrafficPackage{{GB: 25, PriceMinor: 0}}},
		},
		{
			name: "a volume that is no package", gigabytes: 30, period: 30 * day, err: ErrTrafficUnavailable,
			offer: TrafficOffer{Packages: []TrafficPackage{{GB: 25, PriceMinor: 0}}},
		},
		{
			name: "overflow is refused", gigabytes: 1_000_000,
			offer:  TrafficOffer{MinGB: 1, MaxGB: 1_000_000, PricePerGBMinor: 1 << 50},
			period: 3650 * day, err: ErrInvalidAmount,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			amount, bytes, err := ResolveTraffic(test.gigabytes, test.offer, test.period, 30*day)
			if !errors.Is(err, test.err) {
				t.Fatalf("error = %v, want %v", err, test.err)
			}
			if test.err != nil {
				return
			}
			if amount != test.amount {
				t.Fatalf("amount = %d, want %d", amount, test.amount)
			}
			if bytes != int64(test.gigabytes)*GiB {
				t.Fatalf("bytes = %d", bytes)
			}
		})
	}
}
