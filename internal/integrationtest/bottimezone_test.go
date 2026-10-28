//go:build integration

package integrationtest

import (
	"context"
	"strings"
	"testing"
)

func customerTimezone(ctx context.Context, t *testing.T, harness *harness, customerID string) string {
	t.Helper()
	var zone string
	if err := harness.pool.QueryRow(ctx, `SELECT timezone FROM users WHERE id = $1::uuid`, customerID).Scan(&zone); err != nil {
		t.Fatalf("read timezone: %v", err)
	}
	return zone
}

// TestBotCustomerChoosesTheQuietHoursZone sets the zone quiet hours are counted
// in from the bot. Nothing in the bot could set it before, so every Telegram
// customer's quiet hours ran in UTC. The choice lands in the same column the
// web panel writes, and a zone outside the offered list is refused.
func TestBotCustomerChoosesTheQuietHoursZone(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	flow := newBotFlow(ctx, t, harness)
	const account = int64(706001)
	customerID := flow.customerOf(ctx, t, account)

	flow.tap(ctx, account, 70, "act:tz-menu")
	if screen := flow.telegram.lastScreen(t).Fields["text"]; !strings.Contains(screen, "UTC") {
		t.Fatalf("the picker does not name the current zone: %q", screen)
	}
	flow.tap(ctx, account, 70, "act:tz:Asia/Novosibirsk")
	if zone := customerTimezone(ctx, t, harness, customerID); zone != "Asia/Novosibirsk" {
		t.Fatalf("the zone is %q after choosing Novosibirsk", zone)
	}
	if screen := flow.telegram.lastScreen(t).Fields["text"]; !strings.Contains(screen, "Novosibirsk") {
		t.Fatalf("quiet hours do not name the chosen zone: %q", screen)
	}

	flow.tap(ctx, account, 70, "act:tz:Mars/Olympus")
	if zone := customerTimezone(ctx, t, harness, customerID); zone != "Asia/Novosibirsk" {
		t.Fatalf("a zone outside the list was stored: %q", zone)
	}
}
