package accountcheckout

import (
	"context"
	"errors"
	"time"

	"github.com/omniflow/omniflow/internal/commercepg"
)

// ErrCodeRefused is every refusal of a code, whatever the reason: telling a
// stranger "no such code" apart from "already used" would tell them which
// codes exist.
var ErrCodeRefused = errors.New("code was not accepted")

// RedeemedCode is what a code gave the customer.
type RedeemedCode struct {
	// Kind is subscription (days, a trial, a gifted or wholesale plan) or
	// wallet_credit.
	Kind        string
	EndsAt      *time.Time
	CreditMinor int64
	Currency    string
	// TrafficBytes is what a traffic code added.
	TrafficBytes int64
}

// RedeemCode applies a code outside checkout, the way the bot's "redeem a
// code" does: a gift code, then a wholesale access code, then a promo code
// that rewards on its own (days, a trial, wallet credit). A discount promo
// code is refused here; it belongs on the checkout screen.
func (service *Service) RedeemCode(ctx context.Context, customerID, code string) (RedeemedCode, error) {
	if gift, err := service.orders.ClaimGift(ctx, code, customerID); err == nil {
		result := RedeemedCode{Kind: gift.Kind, CreditMinor: gift.CreditMinor.Int64, Currency: gift.Currency}
		if result.Kind != "wallet_credit" {
			result.Kind = "subscription"
		}
		return result, nil
	} else if !errors.Is(err, commercepg.ErrGiftNotFound) {
		return RedeemedCode{}, ErrCodeRefused
	}
	if redeemed, err := service.orders.RedeemAccessCode(ctx, code, customerID); err == nil {
		endsAt := redeemed.EndsAt
		return RedeemedCode{Kind: "subscription", EndsAt: &endsAt}, nil
	}
	reward, err := service.orders.RedeemRewardPromo(ctx, code, customerID)
	if err != nil {
		return RedeemedCode{}, ErrCodeRefused
	}
	switch reward.Kind {
	case commercepg.RewardWalletCredit:
		return RedeemedCode{Kind: "wallet_credit", CreditMinor: reward.CreditMinor, Currency: reward.Currency}, nil
	case commercepg.RewardTraffic:
		return RedeemedCode{Kind: "traffic", TrafficBytes: reward.TrafficBytes}, nil
	case commercepg.RewardLoyaltyTier:
		return RedeemedCode{Kind: "loyalty_tier"}, nil
	}
	endsAt := reward.EndsAt
	return RedeemedCode{Kind: "subscription", EndsAt: &endsAt, CreditMinor: reward.CreditMinor, Currency: reward.Currency}, nil
}
