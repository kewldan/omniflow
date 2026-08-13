package customerauth

import (
	"net/url"
	"testing"
	"time"
)

// The browser suite signs a login-widget payload itself, in TypeScript, using
// `node:crypto`. This asserts that the two implementations agree.
//
// It matters because the alternative failure is silent in the worst direction:
// a suite that signed slightly differently would fail every run and be
// "fixed" by weakening the check it exists to exercise. The fixture below is a
// payload produced by `apps/web/e2e/customer-journey.spec.ts` with the token CI
// configures, captured once.
func TestTheBrowserSuiteSignsAWidgetThisPackageAccepts(t *testing.T) {
	const (
		token    = "000000:e2e-telegram-token"
		authDate = 1786660859
		hash     = "d871ac22f5ffbc98995d87e0e0f98f0e5cef48efc384579ecc9203e60d93d88a"
	)
	values := url.Values{
		"auth_date":  {"1786660859"},
		"first_name": {"Playwright"},
		"id":         {"770001"},
		"hash":       {hash},
	}

	// The clock is pinned to the moment the fixture was signed, because the
	// widget's own freshness window is a separate rule from its signature and
	// only the signature is under test here.
	identity, err := VerifyLoginWidget(values, token, time.Unix(authDate, 0), TelegramMaxAge)
	if err != nil {
		t.Fatalf("the browser suite's signature was rejected: %v", err)
	}
	if identity.Subject() == "" {
		t.Fatal("a verified widget produced no subject")
	}
}
