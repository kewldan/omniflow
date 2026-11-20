package commerce

import (
	"math"
	"testing"
	"time"
)

func TestCreditRemainderIsPaidValueOfTheTimeLeftRoundedDown(t *testing.T) {
	day := 24 * time.Hour
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(30 * day)
	cases := []struct {
		name   string
		paid   int64
		period time.Duration
		now    time.Time
		want   int64
	}{
		{"half the period left", 30000, 30 * day, start.Add(15 * day), 15000},
		{"rounding is down", 10000, 30 * day, start.Add(20 * day), 3333},
		{"not started yet", 30000, 30 * day, start.Add(-time.Hour), 0},
		{"already over", 30000, 30 * day, end.Add(time.Second), 0},
		{"never paid", 0, 30 * day, start.Add(time.Hour), 0},
		{"no period", 30000, 0, start.Add(time.Hour), 0},
	}
	for _, testCase := range cases {
		got := CreditRemainder(testCase.paid, testCase.period, testCase.now, start, end)
		if got.CreditMinor != testCase.want {
			t.Errorf("%s: credit = %d, want %d", testCase.name, got.CreditMinor, testCase.want)
		}
		if got.CreditMinor > testCase.paid {
			t.Errorf("%s: credit %d exceeds what was paid", testCase.name, got.CreditMinor)
		}
	}
}

// An entitlement that carried earlier time forward runs longer than its order
// paid for. Only the bought period is credited, never the carried time.
func TestCreditRemainderNeverCreditsCarriedTime(t *testing.T) {
	day := 24 * time.Hour
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	// Thirty days bought on top of ten carried over: forty days of entitlement.
	got := CreditRemainder(30000, 30*day, start, start, start.Add(40*day))
	if got.CreditMinor != 30000 {
		t.Fatalf("credit = %d, want exactly what was paid", got.CreditMinor)
	}
	got = CreditRemainder(30000, 30*day, start.Add(25*day), start, start.Add(40*day))
	if got.CreditMinor != 15000 {
		t.Fatalf("credit = %d, want the fifteen bought days left", got.CreditMinor)
	}
}

func TestCreditRemainderDoesNotOverflow(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	period := 3650 * 24 * time.Hour
	got := CreditRemainder(math.MaxInt64/2, period, start.Add(period/2), start, start.Add(period))
	if got.CreditMinor <= 0 || got.CreditMinor > math.MaxInt64/2 {
		t.Fatalf("credit = %d", got.CreditMinor)
	}
}

func TestProratesRemainderOnlyWhenTheRemainderIsThrownAway(t *testing.T) {
	cases := map[[3]string]bool{
		{"upgrade", "replace", ""}:           true,
		{"upgrade", "extend", ""}:            false,
		{"downgrade", "", "immediate"}:       true,
		{"downgrade", "", "at_expiry"}:       false,
		{"extension", "replace", ""}:         false,
		{"purchase", "replace", "immediate"}: false,
	}
	for input, want := range cases {
		if got := ProratesRemainder(input[0], input[1], input[2]); got != want {
			t.Errorf("%v = %v, want %v", input, got, want)
		}
	}
}

func TestApplyCreditSplitsWhatTheOrderSpendsFromWhatStays(t *testing.T) {
	if applied, rest := ApplyCredit(15000, 50000); applied != 15000 || rest != 0 {
		t.Fatalf("upgrade: %d, %d", applied, rest)
	}
	if applied, rest := ApplyCredit(15000, 5000); applied != 5000 || rest != 10000 {
		t.Fatalf("downgrade: %d, %d", applied, rest)
	}
	if applied, rest := ApplyCredit(0, 5000); applied != 0 || rest != 0 {
		t.Fatalf("nothing: %d, %d", applied, rest)
	}
}
