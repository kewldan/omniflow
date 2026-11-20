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

// Traffic modes, as Bedolaga's TRAFFIC_SELECTION_MODE: the version's own
// allowance with no top-ups, the same with top-ups, or a volume the customer
// chooses at checkout (and may top up later).
const (
	TrafficFixed          = "fixed"
	TrafficFixedWithTopUp = "fixed_with_topup"
	TrafficSelectable     = "selectable"
)

// TrafficPackage is one volume a selectable version is sold with and its
// price for the version's own duration.
type TrafficPackage struct {
	GB         int
	PriceMinor int64
}

// TrafficOffer is what a plan version sells beside its own traffic
// allowance, in one currency: listed packages, and a volume the customer
// chooses between MinGB and MaxGB at PricePerGBMinor for each gigabyte. A
// zero MinGB means the version sells no custom volume.
type TrafficOffer struct {
	Packages        []TrafficPackage
	MinGB, MaxGB    int
	PricePerGBMinor int64
}

// Custom reports whether the version sells a volume of the customer's own
// choosing.
func (offer TrafficOffer) Custom() bool {
	return offer.MinGB > 0 && offer.PricePerGBMinor > 0
}

// Any reports whether the version sells any traffic choice at all.
func (offer TrafficOffer) Any() bool {
	return len(offer.Packages) > 0 || offer.Custom()
}

// ResolveTraffic prices a chosen volume of gigabytes on a plan version and
// returns what it adds to the period's price and the allowance in bytes the
// entitlement carries. A listed package of that volume wins; otherwise a
// custom volume inside the range is priced per gigabyte.
//
// Prices are for the version's own duration (`own`); a period of another
// length (`period`) scales them in proportion, rounded up to a whole minor
// unit so a short period never sells traffic for nothing. The arithmetic is
// exact: a long period and a large volume cannot overflow it.
func ResolveTraffic(gigabytes int, offer TrafficOffer, period, own time.Duration) (int64, int64, error) {
	if gigabytes <= 0 {
		return 0, 0, ErrTrafficUnavailable
	}
	for _, pack := range offer.Packages {
		if pack.GB == gigabytes {
			amount, err := ScaleToPeriod(big.NewInt(pack.PriceMinor), period, own)
			if err != nil {
				return 0, 0, err
			}
			return amount, int64(gigabytes) * GiB, nil
		}
	}
	if !offer.Custom() || gigabytes < offer.MinGB || gigabytes > offer.MaxGB {
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
