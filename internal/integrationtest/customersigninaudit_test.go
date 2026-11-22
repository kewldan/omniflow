//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/customerauthpg"
)

// TestRefusedTelegramSignInIsAudited: a Mini App sign-in whose launch data
// does not verify, and a widget sign-in whose data is too old, each leave a
// customer.login failure with the method and the reason, and nothing the
// customer sent.
func TestRefusedTelegramSignInIsAudited(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	clock := time.Now().UTC()
	identity := newCustomerService(t, harness, func() time.Time { return clock })

	_, err := identity.SignInWithMiniApp(ctx, "user=%7B%22id%22%3A1%7D&auth_date=1&hash=00",
		customerauthpg.RequestContext{RequestID: "miniapp-refused"})
	if !errors.Is(err, customerauthpg.ErrSignInRejected) {
		t.Fatalf("forged launch data gave %v", err)
	}
	_, err = identity.SignInWithTelegram(ctx, signedWidget(700900, clock.Add(-time.Hour)),
		customerauthpg.RequestContext{RequestID: "widget-stale"})
	if !errors.Is(err, customerauthpg.ErrSignInRejected) {
		t.Fatalf("stale widget data gave %v", err)
	}

	rows, err := harness.pool.Query(ctx, `
		SELECT request_id, actor_type, metadata->>'method', reason, metadata::text
		FROM audit_events WHERE action = 'customer.login' AND outcome = 'failure'
		ORDER BY occurred_at`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string][3]string{}
	for rows.Next() {
		var requestID, actor, method, reason, metadata string
		if err = rows.Scan(&requestID, &actor, &method, &reason, &metadata); err != nil {
			t.Fatal(err)
		}
		if actor != "anonymous" {
			t.Fatalf("actor %q, want anonymous", actor)
		}
		got[requestID] = [3]string{method, reason, metadata}
	}
	if entry := got["miniapp-refused"]; entry[0] != "telegram_miniapp" || entry[1] != "bad_signature" {
		t.Fatalf("mini app refusal audited as %v", entry)
	}
	if entry := got["widget-stale"]; entry[0] != "telegram_widget" || entry[1] != "stale" {
		t.Fatalf("widget refusal audited as %v", entry)
	}
}
