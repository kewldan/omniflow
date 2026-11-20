//go:build integration

package integrationtest

import (
	"context"
	"testing"

	"github.com/omniflow/omniflow/internal/blocklist"
	"github.com/omniflow/omniflow/internal/panelpg"
)

// A screen flags a listed customer once, never an operator, and leaves the
// match alone when it runs again.
func TestBlocklistScreenSkipsOperators(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service := newOperations(t, harness)
	actor := harness.operator(ctx, t, "blocklist-screen@example.test")
	service.SetOperatorTelegramIDs([]int64{7001})

	source, err := service.SaveBlocklistSource(ctx, panelpg.BlocklistSourceInput{
		Slug: "screen", DisplayName: "Screen", SubjectKind: blocklist.SubjectTelegramID,
		URL: "https://lists.example.test/screen.txt", Enabled: true, RefreshInterval: 3600,
	}, actor)
	if err != nil {
		t.Fatalf("save source: %v", err)
	}
	var entries []blocklist.Entry
	for _, value := range []string{"7001", "7002"} {
		fingerprint, fingerprintErr := blocklist.Fingerprint(blocklist.SubjectTelegramID, value)
		if fingerprintErr != nil {
			t.Fatal(fingerprintErr)
		}
		entries = append(entries, blocklist.Entry{Fingerprint: fingerprint})
	}
	if err = service.ReplaceBlocklistEntries(ctx, source.ID, entries); err != nil {
		t.Fatalf("replace entries: %v", err)
	}

	operator := harness.customer(ctx, t)
	listed := harness.customer(ctx, t)
	clean := harness.customer(ctx, t)
	for customer, telegramID := range map[string]string{operator: "7001", listed: "7002", clean: "7003"} {
		if _, err = harness.pool.Exec(ctx, `INSERT INTO identities (user_id, provider, provider_subject, verified_at)
			VALUES ($1::uuid, 'telegram', $2, now())`, customer, telegramID); err != nil {
			t.Fatalf("identity: %v", err)
		}
	}

	result, err := service.ScreenCustomersFor(ctx, "", actor)
	if err != nil {
		t.Fatalf("screen: %v", err)
	}
	if result.NewMatches != 1 || result.SkippedOperators != 1 {
		t.Fatalf("unexpected screen result: %+v", result)
	}
	if matches, _ := service.CustomerMatches(ctx, operator); len(matches) != 0 {
		t.Fatalf("an operator was flagged: %+v", matches)
	}
	if matches, _ := service.CustomerMatches(ctx, listed); len(matches) != 1 {
		t.Fatalf("the listed customer was not flagged: %+v", matches)
	}

	// Checking one customer again records nothing new.
	again, err := service.ScreenCustomersFor(ctx, listed, actor)
	if err != nil {
		t.Fatalf("screen one: %v", err)
	}
	if again.NewMatches != 0 || again.Checked != 1 {
		t.Fatalf("a repeat screen changed the queue: %+v", again)
	}
	// CheckCustomer applies the same exemption.
	direct, err := service.CheckCustomer(ctx, operator, []panelpg.Identifier{{Kind: blocklist.SubjectTelegramID, Value: "7001"}})
	if err != nil || len(direct) != 0 {
		t.Fatalf("CheckCustomer flagged an operator: %v %+v", err, direct)
	}
}
