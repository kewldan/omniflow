package referralpayout

import (
	"reflect"
	"testing"
)

func TestAssessAnOrdinaryPartner(t *testing.T) {
	t.Parallel()
	got := Assess(Facts{
		EarnedMinor: 300_00, Referrals: 12, PayingReferrals: 7, LargestSourceMinor: 60_00,
		RewardsLastWeek: 2, OwnPurchases: 3, OwnTopUpsMinor: 500_00, AccountAgeDays: 200, MinimumMinor: 500_00,
	})
	if got.Score != 0 || got.Level != "low" || len(got.Flags) != 0 {
		t.Fatalf("assessment = %+v", got)
	}
}

func TestAssessTheLaunderingShape(t *testing.T) {
	t.Parallel()
	got := Assess(Facts{
		EarnedMinor: 1000_00, Referrals: 1, PayingReferrals: 0, OwnTopUpsMinor: 5000_00,
		AccountAgeDays: 3, RewardsLastWeek: 25, MinimumMinor: 500_00,
	})
	want := []string{"no_paying_referrals", "reward_burst", "topped_up_never_spent", "young_account"}
	if got.Score != 100 || got.Level != "critical" || !reflect.DeepEqual(got.Flags, want) {
		t.Fatalf("assessment = %+v", got)
	}
}

func TestAssessLevels(t *testing.T) {
	t.Parallel()
	for score, want := range map[int]string{0: "low", 29: "low", 30: "medium", 50: "high", 69: "high", 70: "critical"} {
		if got := level(score); got != want {
			t.Errorf("level(%d) = %s, want %s", score, got, want)
		}
	}
	single := Assess(Facts{EarnedMinor: 3000_00, PayingReferrals: 1, LargestSourceMinor: 3000_00, MinimumMinor: 500_00, AccountAgeDays: 90, OwnPurchases: 1})
	if single.Score != 20 || single.Flags[0] != "single_source" {
		t.Fatalf("single source = %+v", single)
	}
}

func TestAvailable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		wallet, earned, requested int64
		only                      bool
		want                      int64
	}{
		{wallet: 1000, earned: 400, requested: 0, only: true, want: 400},
		{wallet: 300, earned: 400, requested: 0, only: true, want: 300},
		{wallet: 1000, earned: 400, requested: 400, only: true, want: 0},
		{wallet: 1000, earned: 400, requested: 500, only: true, want: 0},
		{wallet: 1000, earned: 0, requested: 0, only: false, want: 1000},
		{wallet: -5, earned: 100, requested: 0, only: false, want: 0},
	}
	for _, testCase := range cases {
		if got := Available(testCase.wallet, testCase.earned, testCase.requested, testCase.only); got != testCase.want {
			t.Errorf("Available(%+v) = %d", testCase, got)
		}
	}
}
