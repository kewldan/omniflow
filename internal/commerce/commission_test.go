package commerce

import "testing"

func TestCommissionFollowsRatesTiersCapsAndTriggers(t *testing.T) {
	first, cap := 2000, 3
	terms := CommissionTerms{
		BPS: 1000, FirstPaymentBPS: &first, PaymentCap: &cap, MinimumPaymentMinor: 5000,
		Triggers: []string{"subscription"},
		Tiers:    []CommissionTier{{Referrals: 10, BPS: 1500}, {Referrals: 3, BPS: 1200}},
	}
	cases := []struct {
		name    string
		payment CommissionPayment
		want    int64
	}{
		{"first payment", CommissionPayment{Trigger: "subscription", PaidMinor: 10000, Index: 1}, 2000},
		{"later payment", CommissionPayment{Trigger: "subscription", PaidMinor: 10000, Index: 2}, 1000},
		{"tier by referrals", CommissionPayment{Trigger: "subscription", PaidMinor: 10000, Index: 2, QualifiedReferrals: 4}, 1200},
		{"highest tier", CommissionPayment{Trigger: "subscription", PaidMinor: 10000, Index: 2, QualifiedReferrals: 12}, 1500},
		{"past the cap", CommissionPayment{Trigger: "subscription", PaidMinor: 10000, Index: 4}, 0},
		{"under the minimum", CommissionPayment{Trigger: "subscription", PaidMinor: 4999, Index: 2}, 0},
		{"not a trigger", CommissionPayment{Trigger: "topup", PaidMinor: 10000, Index: 2}, 0},
		{"rounds down", CommissionPayment{Trigger: "subscription", PaidMinor: 9999, Index: 2}, 999},
	}
	for _, tc := range cases {
		if got, _ := Commission(terms, InviterTerms{}, tc.payment); got != tc.want {
			t.Errorf("%s: %d, want %d", tc.name, got, tc.want)
		}
	}
	personal := 3000
	if got, bps := Commission(terms, InviterTerms{BPS: &personal}, CommissionPayment{Trigger: "subscription", PaidMinor: 10000, Index: 2, QualifiedReferrals: 12}); got != 3000 || bps != 3000 {
		t.Errorf("personal rate: %d at %d", got, bps)
	}
	if (CommissionTerms{}).Configured() {
		t.Error("an empty programme pays commission")
	}
}
