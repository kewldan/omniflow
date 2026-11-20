package commerce

import "sort"

// CommissionTier is a rate an inviter earns once they have brought at least
// Referrals qualified customers.
type CommissionTier struct {
	Referrals int `json:"referrals"`
	BPS       int `json:"bps"`
}

// CommissionTerms are the programme's commission rules.
type CommissionTerms struct {
	// BPS is the share of every counted payment; FirstPaymentBPS, when set,
	// replaces it for the referee's first one.
	BPS             int
	FirstPaymentBPS *int
	// PaymentCap bounds how many of a referee's payments earn; nil is no cap.
	PaymentCap *int
	// MinimumPaymentMinor is the smallest payment that earns.
	MinimumPaymentMinor int64
	// Triggers name the payments that count: subscription and topup.
	Triggers []string
	Tiers    []CommissionTier
}

// InviterTerms are an operator's personal rates for one inviter.
type InviterTerms struct {
	BPS             *int
	FirstPaymentBPS *int
}

// CommissionPayment is one settled payment by a referee.
type CommissionPayment struct {
	// Trigger is subscription or topup.
	Trigger string
	// PaidMinor is what a provider settled, net of the wallet: commission is
	// never paid on money that was already a reward.
	PaidMinor int64
	// Index is this payment's position among the referee's earning payments,
	// counting from one.
	Index int
	// QualifiedReferrals is how many qualified referrals the inviter has.
	QualifiedReferrals int
}

// Configured reports whether the programme pays any commission at all.
func (terms CommissionTerms) Configured() bool {
	if terms.BPS > 0 || terms.FirstPaymentBPS != nil && *terms.FirstPaymentBPS > 0 {
		return true
	}
	for _, tier := range terms.Tiers {
		if tier.BPS > 0 {
			return true
		}
	}
	return false
}

// Commission is what one payment earns the inviter, rounded down, and the
// rate it was earned at. It is zero for a payment the rules do not count.
func Commission(terms CommissionTerms, personal InviterTerms, payment CommissionPayment) (int64, int) {
	if payment.PaidMinor <= 0 || payment.PaidMinor < terms.MinimumPaymentMinor || payment.Index < 1 {
		return 0, 0
	}
	if terms.PaymentCap != nil && payment.Index > *terms.PaymentCap {
		return 0, 0
	}
	counted := false
	for _, trigger := range terms.Triggers {
		if trigger == payment.Trigger {
			counted = true
		}
	}
	if !counted {
		return 0, 0
	}
	bps := terms.BPS
	tiers := append([]CommissionTier(nil), terms.Tiers...)
	sort.Slice(tiers, func(left, right int) bool { return tiers[left].Referrals < tiers[right].Referrals })
	for _, tier := range tiers {
		if payment.QualifiedReferrals >= tier.Referrals {
			bps = tier.BPS
		}
	}
	if personal.BPS != nil {
		bps = *personal.BPS
	}
	if payment.Index == 1 {
		switch {
		case personal.FirstPaymentBPS != nil:
			bps = *personal.FirstPaymentBPS
		case terms.FirstPaymentBPS != nil:
			bps = *terms.FirstPaymentBPS
		}
	}
	if bps <= 0 {
		return 0, 0
	}
	return payment.PaidMinor * int64(min(bps, 10_000)) / 10_000, bps
}
