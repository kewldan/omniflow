package commerce

import (
	"errors"
	"math/big"
	"time"
)

// ErrTrafficUnavailable refuses a traffic volume the plan version does not
// sell.
var ErrTrafficUnavailable = errors.New("traffic_unavailable")

// GiB is one gigabyte as Omniflow counts traffic everywhere: 2^30 bytes.
const GiB int64 = 1 << 30

// TrafficOffer is what a plan version sells beside its own traffic
// allowance, in one currency: a volume the customer chooses between MinGB
// and MaxGB at PricePerGBMinor for each gigabyte. A zero MinGB means the
// version sells no custom volume.
type TrafficOffer struct {
	MinGB, MaxGB    int
	PricePerGBMinor int64
}

// Custom reports whether the version sells a volume of the customer's own
// choosing.
func (offer TrafficOffer) Custom() bool {
	return offer.MinGB > 0 && offer.PricePerGBMinor > 0
}

// ResolveTraffic prices a chosen volume of gigabytes on a plan version and
// returns what it adds to the period's price and the allowance in bytes the
// entitlement carries.
//
// The price per gigabyte is for the version's own duration (`own`); a period
// of another length (`period`) scales it in proportion, rounded up to a whole
// minor unit so a short period never sells traffic for nothing. The
// arithmetic is exact: a long period and a large volume cannot overflow it.
func ResolveTraffic(gigabytes int, offer TrafficOffer, period, own time.Duration) (int64, int64, error) {
	if gigabytes <= 0 || !offer.Custom() || gigabytes < offer.MinGB || gigabytes > offer.MaxGB {
		return 0, 0, ErrTrafficUnavailable
	}
	amount, err := ScaleToPeriod(new(big.Int).Mul(big.NewInt(offer.PricePerGBMinor), big.NewInt(int64(gigabytes))), period, own)
	if err != nil {
		return 0, 0, err
	}
	return amount, int64(gigabytes) * GiB, nil
}

// ScaleToPeriod scales a price set for the version's own duration to a
// period of another length, rounding up. An unknown own duration leaves the
// price as it is.
func ScaleToPeriod(price *big.Int, period, own time.Duration) (int64, error) {
	if price.Sign() < 0 {
		return 0, ErrInvalidAmount
	}
	scaled := new(big.Int).Set(price)
	if own > 0 && period > 0 && period != own {
		scaled.Mul(scaled, big.NewInt(int64(period/time.Second)))
		divisor := big.NewInt(int64(own / time.Second))
		quotient, remainder := new(big.Int).QuoRem(scaled, divisor, new(big.Int))
		if remainder.Sign() > 0 {
			quotient.Add(quotient, big.NewInt(1))
		}
		scaled = quotient
	}
	if !scaled.IsInt64() || scaled.Int64() > 1<<62 {
		return 0, ErrInvalidAmount
	}
	return scaled.Int64(), nil
}
