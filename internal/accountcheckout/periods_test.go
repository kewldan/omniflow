package accountcheckout

import "testing"

// The catalogue and the checkout agree on which listed periods an operation
// may buy: a renewal-only period renews, a purchase-only one starts or changes
// a plan.
func TestPeriodSoldFor(t *testing.T) {
	cases := []struct {
		operation               string
		forPurchase, forRenewal bool
		want                    bool
	}{
		{"purchase", true, false, true},
		{"purchase", false, true, false},
		{"upgrade", true, false, true},
		{"downgrade", false, true, false},
		{"extension", false, true, true},
		{"extension", true, false, false},
		{"renewal", true, true, true},
	}
	for _, tc := range cases {
		if got := periodSoldFor(tc.operation, tc.forPurchase, tc.forRenewal); got != tc.want {
			t.Errorf("periodSoldFor(%q, purchase=%v, renewal=%v) = %v, want %v",
				tc.operation, tc.forPurchase, tc.forRenewal, got, tc.want)
		}
	}
}
