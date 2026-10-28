package customerauth

import "testing"

func TestBindingMatchesIssuerRequiresTheIssuerItWasMadeAt(t *testing.T) {
	cases := []struct {
		bound, asserted string
		want            bool
	}{
		{"https://accounts.example", "https://accounts.example", true},
		{"https://accounts.example", "https://attacker.example", false},
		// A trailing slash is a different issuer string; OIDC compares exactly.
		{"https://accounts.example", "https://accounts.example/", false},
		// A binding that predates issuer pinning and lost its provider row.
		{"", "https://accounts.example", false},
		{"", "", false},
	}
	for _, testCase := range cases {
		if got := BindingMatchesIssuer(testCase.bound, testCase.asserted); got != testCase.want {
			t.Fatalf("BindingMatchesIssuer(%q, %q) = %v, want %v",
				testCase.bound, testCase.asserted, got, testCase.want)
		}
	}
}
