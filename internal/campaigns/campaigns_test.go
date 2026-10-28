package campaigns

import "testing"

// A recipient is resolved at expansion when the bot could never message them:
// on the suppression list, or with no Telegram identity at all. Anyone else is
// left queued for the bot. The suppression list wins, because it is the
// operator's standing instruction and the reason they most need to see.
func TestExpansionSuppressionNamesTheReason(t *testing.T) {
	t.Parallel()
	cases := []struct {
		suppressed, reachable bool
		want                  string
	}{
		{suppressed: true, reachable: true, want: "suppressed"},
		{suppressed: true, reachable: false, want: "suppressed"},
		{suppressed: false, reachable: false, want: "no_telegram"},
		{suppressed: false, reachable: true, want: ""},
	}
	for _, testCase := range cases {
		if got := expansionSuppression(testCase.suppressed, testCase.reachable); got != testCase.want {
			t.Fatalf("expansionSuppression(%t, %t) = %q, want %q",
				testCase.suppressed, testCase.reachable, got, testCase.want)
		}
	}
}

func TestItoaMatchesDecimal(t *testing.T) {
	t.Parallel()
	for value, want := range map[int]string{0: "0", 7: "7", 5000: "5000", 12: "12"} {
		if got := itoa(value); got != want {
			t.Fatalf("itoa(%d) = %q, want %q", value, got, want)
		}
	}
}
