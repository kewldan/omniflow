//go:build integration

package integrationtest

import (
	"context"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/audience"
)

// Every new segment filter compiles to SQL the database accepts, and the
// explicit selection and the wallet filter select the customers they name.
func TestNewSegmentFiltersRunAgainstTheSchema(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	rich := harness.customer(ctx, t)
	poor := harness.customer(ctx, t)
	harness.seedWallet(ctx, t, rich, "segment-wallet", 5000)

	count := func(filters map[string]any) int {
		t.Helper()
		query, err := audience.Compile(filters, time.Now())
		if err != nil {
			t.Fatalf("compile %v: %v", filters, err)
		}
		var n int
		if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM users u WHERE `+query.Where, query.Args...).Scan(&n); err != nil {
			t.Fatalf("run %v: %v", filters, err)
		}
		return n
	}
	for _, filters := range []map[string]any{
		{"trial": "active"}, {"trial": "used"}, {"trial": "never"},
		{"trafficUsedPercent": float64(80)},
		{"registeredWithinDays": float64(7)}, {"registeredBeforeDays": float64(30)},
		{"activeWithinDays": float64(30)},
		{"referrals": "inviter"}, {"referrals": "invitee"},
		{"adCampaign": "stars_elite"},
		{"signInMethod": "telegram"}, {"signInMethod": "email"}, {"signInMethod": "vkid"}, {"signInMethod": "oidc"},
		{"zeroBalance": true},
	} {
		count(filters)
	}
	if n := count(map[string]any{"zeroBalance": false, "customerIds": []any{rich, poor}}); n != 1 {
		t.Fatalf("rich among the selection = %d, want 1", n)
	}
	if n := count(map[string]any{"zeroBalance": true, "customerIds": []any{rich, poor}}); n != 1 {
		t.Fatalf("poor among the selection = %d, want 1", n)
	}
	if _, err := audience.Compile(map[string]any{"customerIds": []any{"not-a-uuid"}}, time.Now()); err == nil {
		t.Fatal("a malformed customer ID was accepted")
	}
}
