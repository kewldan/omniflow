// Package engagement runs the fortune wheel and polls with rewards, as
// Bedolaga's wheel_service and poll_service do.
//
// The wheel is a weighted draw from crypto/rand over an operator's prize
// table whose expected payout is held under a target return-to-player
// percentage of the spin cost. Spins are limited per customer per day,
// refused for accounts younger than a minimum age, charged to the wallet and
// paid out through the ledger or a subscription extension in one serializable
// transaction, and every spin is a row an operator can audit.
package engagement

import (
	"crypto/rand"
	"errors"
	"math/big"
)

// Prize kinds.
const (
	KindWallet  = "wallet"
	KindDays    = "days"
	KindNothing = "nothing"
)

// Prize is one segment of the wheel.
type Prize struct {
	ID      string `json:"id,omitempty"`
	LabelRU string `json:"labelRu"`
	LabelEN string `json:"labelEn"`
	Kind    string `json:"kind"`
	Value   int64  `json:"value"`
	Weight  int    `json:"weight"`
	Active  bool   `json:"active"`
}

// ErrInvalidPrize reports a prize the table cannot hold.
var ErrInvalidPrize = errors.New("the prize is not valid")

// Validate checks one prize.
func (prize Prize) Validate() error {
	labels := len([]rune(prize.LabelRU)) >= 1 && len([]rune(prize.LabelRU)) <= 60 &&
		len([]rune(prize.LabelEN)) >= 1 && len([]rune(prize.LabelEN)) <= 60
	value := (prize.Kind == KindNothing && prize.Value == 0) || (prize.Kind == KindWallet && prize.Value > 0) ||
		(prize.Kind == KindDays && prize.Value >= 1 && prize.Value <= 365)
	if !labels || !value || prize.Weight < 1 || prize.Weight > 1_000_000 {
		return ErrInvalidPrize
	}
	return nil
}

// ExpectedValue is what one spin pays out on average, in minor units, with a
// day valued at dayValue.
func ExpectedValue(prizes []Prize, dayValue int64) float64 {
	var total, weighted float64
	for _, prize := range prizes {
		if !prize.Active {
			continue
		}
		total += float64(prize.Weight)
		switch prize.Kind {
		case KindWallet:
			weighted += float64(prize.Weight) * float64(prize.Value)
		case KindDays:
			weighted += float64(prize.Weight) * float64(prize.Value*dayValue)
		}
	}
	if total == 0 {
		return 0
	}
	return weighted / total
}

// RTPPercent is the expected payout as a percentage of the spin cost, or 0
// for a free spin.
func RTPPercent(prizes []Prize, dayValue, cost int64) float64 {
	if cost <= 0 {
		return 0
	}
	return ExpectedValue(prizes, dayValue) * 100 / float64(cost)
}

// Draw picks an active prize with probability proportional to its weight.
// The randomness is crypto/rand: a customer must not be able to predict the
// next result from the last ones.
func Draw(prizes []Prize) (Prize, error) {
	total := int64(0)
	for _, prize := range prizes {
		if prize.Active {
			total += int64(prize.Weight)
		}
	}
	if total == 0 {
		return Prize{}, errors.New("the wheel has no active prizes")
	}
	pick, err := rand.Int(rand.Reader, big.NewInt(total))
	if err != nil {
		return Prize{}, err
	}
	point := pick.Int64()
	for _, prize := range prizes {
		if !prize.Active {
			continue
		}
		if point < int64(prize.Weight) {
			return prize, nil
		}
		point -= int64(prize.Weight)
	}
	return Prize{}, errors.New("draw fell off the wheel")
}
