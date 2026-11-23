// Package referralpayout lets a customer withdraw what their referrals
// earned them, and lets an operator approve, reject, and mark the payout
// done.
//
// The rules follow Bedolaga's referral_withdrawal_service: a minimum amount, a
// cooldown between requests, one open request at a time, only the referral
// share of the wallet when the operator says so, and a risk score shown to the
// operator beside every request. The score is advice: nothing is approved or
// rejected automatically, because a false positive here withholds somebody's
// earnings and a false negative pays out laundered money, and both deserve a
// person's judgement.
package referralpayout

import "sort"

// Facts are what the score is computed from, gathered in one read.
type Facts struct {
	// EarnedMinor is every unreversed invite reward and commission in the
	// currency.
	EarnedMinor int64
	// Referrals is how many customers this one invited, and PayingReferrals
	// how many of them have a paid order.
	Referrals       int
	PayingReferrals int
	// LargestSourceMinor is the most any single referral produced.
	LargestSourceMinor int64
	// RewardsLastWeek counts invite rewards granted in the last seven days.
	RewardsLastWeek int
	// OwnTopUpsMinor is what the customer paid into their own wallet, and
	// OwnPurchases how many subscription orders they paid for themselves.
	OwnTopUpsMinor int64
	OwnPurchases   int
	// AccountAgeDays is how long the customer has existed.
	AccountAgeDays int
	// HeldAttributions counts this customer's referrals held or rejected by
	// the referral review, and SignalledReferrals those with abuse signals.
	HeldAttributions   int
	SignalledReferrals int
	// MinimumMinor is the withdrawal minimum, the scale "large" is judged on.
	MinimumMinor int64
}

// Assessment is the score and the reasons for it.
type Assessment struct {
	Score int      `json:"score"`
	Level string   `json:"level"`
	Flags []string `json:"flags"`
}

// Assess scores a withdrawal request from 0 (nothing unusual) to 100.
//
// Each flag is a pattern Bedolaga's analysis looks for, weighted the same way:
// referral income with no paying referrals is the strongest signal, money
// topped up and never spent the next, then a single source, a burst, a new
// account, and referrals the abuse review already questioned.
func Assess(facts Facts) Assessment {
	score := 0
	var flags []string
	add := func(flag string, weight int) {
		score += weight
		flags = append(flags, flag)
	}
	if facts.EarnedMinor > 0 && facts.PayingReferrals == 0 {
		add("no_paying_referrals", 50)
	}
	if facts.OwnTopUpsMinor > 0 && facts.OwnPurchases == 0 {
		add("topped_up_never_spent", 40)
	}
	if facts.PayingReferrals == 1 && facts.MinimumMinor > 0 && facts.LargestSourceMinor >= 5*facts.MinimumMinor {
		add("single_source", 20)
	}
	if facts.RewardsLastWeek > 20 {
		add("reward_burst", 15)
	}
	if facts.AccountAgeDays < 14 {
		add("young_account", 15)
	}
	if facts.HeldAttributions > 0 {
		add("questioned_referrals", 20)
	}
	if facts.SignalledReferrals > 0 {
		add("abuse_signals", 15)
	}
	score = min(score, 100)
	sort.Strings(flags)
	if flags == nil {
		flags = []string{}
	}
	return Assessment{Score: score, Level: level(score), Flags: flags}
}

func level(score int) string {
	switch {
	case score >= 70:
		return "critical"
	case score >= 50:
		return "high"
	case score >= 30:
		return "medium"
	default:
		return "low"
	}
}

// Available is what may be requested now: the wallet's available balance,
// capped by what referrals earned minus what was already requested when only
// the referral share may leave.
func Available(walletAvailable, earned, alreadyRequested int64, onlyReferral bool) int64 {
	if walletAvailable < 0 {
		walletAvailable = 0
	}
	if !onlyReferral {
		return walletAvailable
	}
	return max(min(walletAvailable, earned-alreadyRequested), 0)
}
