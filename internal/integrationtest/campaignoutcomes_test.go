//go:build integration

package integrationtest

import (
	"context"
	"testing"

	"github.com/omniflow/omniflow/internal/panelpg"
)

// The panel's breakdown says why recipients were skipped or failed, grouped by
// reason, with counts only.
func TestCampaignOutcomesAreGroupedByReason(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	operations := newOperations(t, harness)
	actor := harness.operator(ctx, t, "campaign-outcomes@omniflow.test")
	campaignID := seedCampaign(ctx, t, harness, operations, actor)

	outcomes := []struct{ status, reason, code string }{
		{"sent", "", ""}, {"sent", "", ""}, {"sent", "", ""},
		{"suppressed", "quiet_hours", ""}, {"suppressed", "quiet_hours", ""},
		{"suppressed", "no_telegram", "account_inactive"},
		{"failed", "", "send_failed"},
		{"queued", "", ""},
	}
	for _, outcome := range outcomes {
		customerID := harness.customer(ctx, t)
		if _, err := harness.pool.Exec(ctx, `INSERT INTO campaign_recipients
			(campaign_id, user_id, status, suppression_reason, error_code)
			VALUES ($1::uuid, $2::uuid, $3, NULLIF($4, ''), NULLIF($5, ''))`,
			campaignID, customerID, outcome.status, outcome.reason, outcome.code); err != nil {
			t.Fatalf("seed recipient: %v", err)
		}
	}

	got, err := operations.CampaignOutcomes(ctx, campaignID)
	if err != nil {
		t.Fatalf("outcomes: %v", err)
	}
	want := []panelpg.CampaignOutcome{
		{Status: "sent", Recipients: 3},
		{Status: "suppressed", Reason: "quiet_hours", Recipients: 2},
	}
	if len(got) != 5 {
		t.Fatalf("outcomes = %+v, want five groups", got)
	}
	for index, expected := range want {
		if got[index] != expected {
			t.Fatalf("group %d = %+v, want %+v", index, got[index], expected)
		}
	}
	found := map[panelpg.CampaignOutcome]bool{}
	for _, outcome := range got {
		found[outcome] = true
	}
	for _, expected := range []panelpg.CampaignOutcome{
		{Status: "failed", ErrorCode: "send_failed", Recipients: 1},
		{Status: "suppressed", Reason: "no_telegram", ErrorCode: "account_inactive", Recipients: 1},
		{Status: "queued", Recipients: 1},
	} {
		if !found[expected] {
			t.Fatalf("missing %+v in %+v", expected, got)
		}
	}

	if _, err := operations.CampaignOutcomes(ctx, "not-a-uuid"); err == nil {
		t.Fatal("a malformed campaign identifier was accepted")
	}
}
