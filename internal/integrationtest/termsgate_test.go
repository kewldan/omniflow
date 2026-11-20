//go:build integration

package integrationtest

import (
	"context"
	"strings"
	"testing"

	"github.com/omniflow/omniflow/internal/termsgate"
)

// With the terms required, the bot shows the gate instead of the menu until
// the customer accepts, the acceptance is a terms consent of the current
// version, and raising the version asks again.
func TestTermsGateHoldsTheBotUntilAccepted(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	flow := newBotFlow(ctx, t, harness)
	gate := termsgate.New(harness.pool)
	if _, err := harness.pool.Exec(ctx, `UPDATE installation_settings SET document = '{"requireTerms": true, "termsVersion": "2026-11"}' WHERE section = 'marketing'`); err != nil {
		t.Fatal(err)
	}
	flow.say(ctx, 713001, "/start")
	gateScreen := flow.telegram.lastTo(t, 713001)
	if !strings.Contains(gateScreen, "Terms of service") {
		t.Fatalf("start showed %q, want the terms gate", gateScreen)
	}
	customer := flow.customerOf(ctx, t, 713001)
	if blocked, _, err := gate.Blocked(ctx, customer); err != nil || !blocked {
		t.Fatalf("blocked = %v (%v), want true before accepting", blocked, err)
	}

	flow.tap(ctx, 713001, 1, "act:terms-accept")
	if blocked, _, err := gate.Blocked(ctx, customer); err != nil || blocked {
		t.Fatalf("blocked = %v (%v), want false after accepting", blocked, err)
	}
	var source, version string
	if err := harness.pool.QueryRow(ctx, `SELECT source, policy_version FROM consent_records
		WHERE user_id = $1::uuid AND purpose = 'terms' AND granted`, customer).Scan(&source, &version); err != nil {
		t.Fatalf("consent: %v", err)
	}
	if source != "telegram_bot" || version != "2026-11" {
		t.Fatalf("consent = %s/%s", source, version)
	}

	if _, err := harness.pool.Exec(ctx, `UPDATE installation_settings SET document = '{"requireTerms": true, "termsVersion": "2027-01"}' WHERE section = 'marketing'`); err != nil {
		t.Fatal(err)
	}
	if blocked, _, _ := gate.Blocked(ctx, customer); !blocked {
		t.Fatal("a new version did not ask again")
	}
}
