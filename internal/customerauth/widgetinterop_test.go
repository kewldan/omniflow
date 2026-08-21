package customerauth

import (
	"testing"
	"time"
)

// The browser suite signs a login-widget payload itself, in TypeScript, using
// `node:crypto`, and posts it the way the real widget does: `id` and
// `auth_date` as JSON numbers, the rest as strings. This asserts that the two
// implementations agree, all the way from the document on the wire.
//
// It matters because the alternative failure is silent in the worst direction:
// a suite that signed slightly differently would fail every run and be
// "fixed" by weakening the check it exists to exercise. And a suite that posted
// strings where the widget posts numbers would pass while every real browser
// was refused — which is precisely what happened before the handler decoded
// numbers. The fixture below is a payload produced by
// `apps/web/e2e/customer-journey.spec.ts` with the token CI configures,
// captured once.
func TestTheBrowserSuiteSignsAWidgetThisPackageAccepts(t *testing.T) {
	const (
		token    = "000000:e2e-telegram-token"
		authDate = 1786660859
		hash     = "d871ac22f5ffbc98995d87e0e0f98f0e5cef48efc384579ecc9203e60d93d88a"
	)
	raw := []byte(`{"auth_date":1786660859,"first_name":"Playwright","id":770001,"hash":"` + hash + `"}`)

	values, err := WidgetValuesFromJSON(raw)
	if err != nil {
		t.Fatalf("the widget's own document shape was refused: %v", err)
	}

	// The clock is pinned to the moment the fixture was signed, because the
	// widget's own freshness window is a separate rule from its signature and
	// only the signature is under test here.
	identity, err := VerifyLoginWidget(values, token, time.Unix(authDate, 0), TelegramMaxAge)
	if err != nil {
		t.Fatalf("the browser suite's signature was rejected: %v", err)
	}
	if identity.Subject() != "770001" {
		t.Fatalf("subject = %q, want 770001", identity.Subject())
	}
}
