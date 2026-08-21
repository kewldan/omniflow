package commerce

import (
	"errors"
	"time"
)

var ErrReferralNotQualified = errors.New("referral is not qualified for a reward")

// ReferralProgram mirrors the operator-configured referral_programs row.
type ReferralProgram struct {
	Enabled             bool
	Currency            string
	InviterRewardMinor  int64
	InviteeRewardMinor  int64
	Qualification       string
	InviterRewardCap    *int
	AttributionValidity time.Duration
	RewardExpiry        *time.Duration
}

// ReferralAttribution is one immutable inviter/invitee pair plus the state that
// decides whether the pair has qualified.
type ReferralAttribution struct {
	AttributedAt time.Time
	// OrderState is the state of the order being evaluated for qualification.
	OrderState OrderState
	// OrderPaidMinor is the amount actually settled, so a fully wallet-funded
	// order does not mint new value out of a referral.
	OrderPaidMinor int64
	OrderCurrency  string
	// InviterRewardCount counts rewards the inviter has already received.
	InviterRewardCount int
	// GrantedRoles marks rewards already recorded for this attribution so the
	// caller can replay qualification without granting twice.
	GrantedRoles map[string]bool
}

// ReferralReward is one wallet credit to record through the ledger.
type ReferralReward struct {
	Role      string
	Amount    Money
	ExpiresAt *time.Time
}

// SubscriptionOperation reports whether an order operation buys subscription
// time. Only these count as a customer's "first order" — for qualifying a
// referral, and for deciding whether a promotion's "new customer" has bought
// before. A top-up moves money into a wallet, a goods order buys something
// else, a gift is for somebody else, and a code was paid for by a distributor:
// none of them is the purchase a referral or a welcome offer is about, so none
// of them may qualify one or, just as importantly, block one.
func SubscriptionOperation(operation string) bool {
	switch operation {
	case "purchase", "extension", "renewal", "upgrade", "downgrade":
		return true
	default:
		return false
	}
}

// QualifyReferral decides which rewards a referral attribution has earned. It is
// deterministic and returns an empty slice — never an error — when a reward was
// already granted, so a retried job is a no-op instead of a duplicate credit.
func QualifyReferral(now time.Time, program ReferralProgram, attribution ReferralAttribution) ([]ReferralReward, error) {
	if !program.Enabled {
		return nil, nil
	}
	if program.Qualification != "first_paid_order" {
		return nil, ErrReferralNotQualified
	}
	switch attribution.OrderState {
	case OrderPaid, OrderFulfilled:
	default:
		return nil, nil
	}
	if attribution.OrderPaidMinor <= 0 {
		return nil, nil
	}
	if program.AttributionValidity > 0 && now.Sub(attribution.AttributedAt) > program.AttributionValidity {
		return nil, nil
	}
	var expiresAt *time.Time
	if program.RewardExpiry != nil && *program.RewardExpiry > 0 {
		expiry := now.Add(*program.RewardExpiry)
		expiresAt = &expiry
	}
	rewards := make([]ReferralReward, 0, 2)
	granted := attribution.GrantedRoles
	if program.InviterRewardMinor > 0 && !granted["inviter"] {
		if program.InviterRewardCap == nil || attribution.InviterRewardCount < *program.InviterRewardCap {
			amount, err := NewMoney(program.InviterRewardMinor, program.Currency)
			if err != nil {
				return nil, err
			}
			rewards = append(rewards, ReferralReward{Role: "inviter", Amount: amount, ExpiresAt: expiresAt})
		}
	}
	if program.InviteeRewardMinor > 0 && !granted["invitee"] {
		amount, err := NewMoney(program.InviteeRewardMinor, program.Currency)
		if err != nil {
			return nil, err
		}
		rewards = append(rewards, ReferralReward{Role: "invitee", Amount: amount, ExpiresAt: expiresAt})
	}
	return rewards, nil
}

// ReferralProgress is the customer-facing summary shown in the bot.
type ReferralProgress struct {
	Invited        int64
	Qualified      int64
	RewardedMinor  int64
	Currency       string
	RemainingSlots *int
}

// Progress builds the customer-facing referral summary, including how many more
// qualified referrals the inviter may still be rewarded for.
func (program ReferralProgram) Progress(invited, qualified, rewardedMinor int64, rewardCount int) ReferralProgress {
	progress := ReferralProgress{Invited: invited, Qualified: qualified, RewardedMinor: rewardedMinor, Currency: program.Currency}
	if program.InviterRewardCap != nil {
		remaining := max(*program.InviterRewardCap-rewardCount, 0)
		progress.RemainingSlots = &remaining
	}
	return progress
}
