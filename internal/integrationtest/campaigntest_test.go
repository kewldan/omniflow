//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"testing"

	"github.com/omniflow/omniflow/internal/panelpg"
)

// seedCampaign creates a template, a segment, and a campaign that points at
// both, and returns the campaign's identifier.
func seedCampaign(
	ctx context.Context, t *testing.T, harness *harness, operations *panelpg.Service, actor panelpg.Actor,
) string {
	t.Helper()

	template, err := operations.SaveTemplate(ctx, panelpg.MessageTemplate{
		Code: "spring_offer", Class: "marketing",
		SubjectEN: "Twenty percent off", SubjectRU: "Скидка двадцать процентов",
		BodyEN: "Hello {{name}}, your plan renews soon.",
		BodyRU: "Здравствуйте, {{name}}, подписка скоро продлится.",
		// Every placeholder has to be declared. That validation is what stops a
		// template reaching a customer with a variable nothing fills.
		Variables: []string{"name"},
	}, actor)
	if err != nil {
		t.Fatalf("save template: %v", err)
	}

	segment, err := operations.SaveSegment(ctx, panelpg.AudienceSegment{
		Code: "everyone", NameEN: "Everyone", NameRU: "Все",
		Filters: map[string]any{},
	}, actor)
	if err != nil {
		t.Fatalf("save segment: %v", err)
	}

	campaign, err := operations.CreateCampaign(ctx, "Spring offer", template.ID, segment.ID, actor)
	if err != nil {
		t.Fatalf("create campaign: %v", err)
	}
	return campaign.ID
}

// The reason `campaign_test_sends` is a table of its own: a preview must not
// touch anything the counters are derived from, and must not consume a place in
// the audience. Both would be discovered only after the campaign had run.
func TestACampaignTestSendLeavesTheAudienceAndTheCountersAlone(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	operations := newOperations(t, harness)
	actor := harness.operator(ctx, t, "campaigns@omniflow.test")
	campaignID := seedCampaign(ctx, t, harness, operations, actor)

	test, err := operations.SendCampaignTest(ctx, campaignID, "ru", actor)
	if err != nil {
		t.Fatalf("send a test: %v", err)
	}
	if test.Status != "queued" || test.Locale != "ru" {
		t.Fatalf("test = %+v, want a queued Russian preview", test)
	}

	var recipients int
	if err = harness.pool.QueryRow(ctx,
		`SELECT count(*) FROM campaign_recipients WHERE campaign_id = $1::uuid`,
		campaignID).Scan(&recipients); err != nil {
		t.Fatalf("count recipients: %v", err)
	}
	if recipients != 0 {
		t.Fatalf("the preview created %d recipient rows, want 0", recipients)
	}

	campaigns, err := operations.Campaigns(ctx, 10)
	if err != nil {
		t.Fatalf("list campaigns: %v", err)
	}
	for _, campaign := range campaigns {
		if campaign.ID != campaignID {
			continue
		}
		if campaign.Queued+campaign.Sent+campaign.Failed+campaign.Suppressed != 0 {
			t.Fatalf("the preview moved the counters: %+v", campaign)
		}
	}
}

// A preview is for a decision that is still open. A campaign that has completed
// or been cancelled will not be sent again, and putting its copy into the
// operator group would read as though it might.
func TestACampaignTestSendIsRefusedOnceTheDecisionIsClosed(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	operations := newOperations(t, harness)
	actor := harness.operator(ctx, t, "campaigns-closed@omniflow.test")
	campaignID := seedCampaign(ctx, t, harness, operations, actor)

	if _, err := operations.SetCampaignState(ctx, campaignID, "cancelled", nil, actor); err != nil {
		t.Fatalf("cancel the campaign: %v", err)
	}
	if _, err := operations.SendCampaignTest(ctx, campaignID, "en", actor); !errors.Is(err, panelpg.ErrRejected) {
		t.Fatalf("err = %v, want ErrRejected", err)
	}

	var queued int
	if err := harness.pool.QueryRow(ctx,
		`SELECT count(*) FROM campaign_test_sends WHERE campaign_id = $1::uuid`,
		campaignID).Scan(&queued); err != nil {
		t.Fatalf("count previews: %v", err)
	}
	if queued != 0 {
		t.Fatalf("a refused preview left %d rows behind, want 0", queued)
	}
}

// Only the two supported languages are accepted, and an unknown campaign is a
// 404 rather than a row queued against nothing.
func TestACampaignTestSendValidatesWhatItWasAsked(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	operations := newOperations(t, harness)
	actor := harness.operator(ctx, t, "campaigns-validate@omniflow.test")
	campaignID := seedCampaign(ctx, t, harness, operations, actor)

	if _, err := operations.SendCampaignTest(ctx, campaignID, "de", actor); !errors.Is(err, panelpg.ErrValidaton) {
		t.Fatalf("err = %v, want ErrValidaton for an unsupported locale", err)
	}
	if _, err := operations.SendCampaignTest(
		ctx, "11111111-1111-1111-1111-111111111111", "en", actor,
	); !errors.Is(err, panelpg.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound for an unknown campaign", err)
	}
}

// The record is what the panel shows between queuing a preview and its arrival
// in Telegram, so it has to come back.
func TestCampaignTestSendsAreListedForTheirCampaign(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	operations := newOperations(t, harness)
	actor := harness.operator(ctx, t, "campaigns-list@omniflow.test")
	campaignID := seedCampaign(ctx, t, harness, operations, actor)

	for _, locale := range []string{"en", "ru"} {
		if _, err := operations.SendCampaignTest(ctx, campaignID, locale, actor); err != nil {
			t.Fatalf("send a %s test: %v", locale, err)
		}
	}

	tests, err := operations.CampaignTests(ctx, campaignID, 10)
	if err != nil {
		t.Fatalf("list previews: %v", err)
	}
	if len(tests) != 2 {
		t.Fatalf("listed %d previews, want 2", len(tests))
	}
	for _, test := range tests {
		if test.Status != "queued" || !test.Resolved.IsZero() {
			t.Fatalf("preview = %+v, want it still in flight", test)
		}
	}
}
