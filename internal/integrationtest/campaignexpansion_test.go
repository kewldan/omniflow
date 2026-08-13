//go:build integration

package integrationtest

import (
	"context"
	"log/slog"
	"testing"

	"github.com/omniflow/omniflow/internal/campaigns"
	"github.com/omniflow/omniflow/internal/panelpg"
)

// A segment that binds a value has to expand.
//
// It did not. The expansion statement passed the campaign identifier as $1 and
// appended the segment's own arguments after it, while the compiled filter
// numbers its placeholders from $1 as well. The two collided, PostgreSQL
// rejected the bind, and the pass logged a failure and queued nobody — which is
// indistinguishable, from the panel, from a segment that matches nobody. Only a
// segment binding no values ever worked, and every filter an operator would
// reach for binds one.
func TestACampaignExpandsASegmentThatBindsAValue(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	operations := newOperations(t, harness)
	actor := harness.operator(ctx, t, "expansion@omniflow.test")

	// Two customers, one in each locale. The segment names one of them, which is
	// what puts a bound value in the compiled filter.
	russian := harness.customer(ctx, t)
	if _, err := harness.pool.Exec(ctx,
		`UPDATE users SET locale = 'ru' WHERE id = $1::uuid`, russian); err != nil {
		t.Fatalf("set a locale: %v", err)
	}
	english := harness.customer(ctx, t)
	if _, err := harness.pool.Exec(ctx,
		`UPDATE users SET locale = 'en' WHERE id = $1::uuid`, english); err != nil {
		t.Fatalf("set a locale: %v", err)
	}

	template, err := operations.SaveTemplate(ctx, panelpg.MessageTemplate{
		Code: "expansion", Class: "marketing",
		SubjectEN: "Subject", SubjectRU: "Тема",
		BodyEN: "Body", BodyRU: "Текст", Variables: []string{},
	}, actor)
	if err != nil {
		t.Fatalf("save template: %v", err)
	}
	segment, err := operations.SaveSegment(ctx, panelpg.AudienceSegment{
		Code: "russian-speakers", NameEN: "Russian speakers", NameRU: "Русскоязычные",
		Filters: map[string]any{"locale": "ru"},
	}, actor)
	if err != nil {
		t.Fatalf("save segment: %v", err)
	}
	if segment.Size != 1 {
		t.Fatalf("the segment counts %d customers, want 1", segment.Size)
	}

	campaign, err := operations.CreateCampaign(ctx, "Expansion", template.ID, segment.ID, actor)
	if err != nil {
		t.Fatalf("create campaign: %v", err)
	}
	if _, err = operations.SetCampaignState(ctx, campaign.ID, "running", nil, actor); err != nil {
		t.Fatalf("start the campaign: %v", err)
	}

	campaigns.New(harness.pool, slog.New(slog.DiscardHandler), campaigns.Config{}).
		RunOnce(ctx)

	var queued int
	if err = harness.pool.QueryRow(ctx,
		`SELECT count(*) FROM campaign_recipients WHERE campaign_id = $1::uuid`,
		campaign.ID).Scan(&queued); err != nil {
		t.Fatalf("count recipients: %v", err)
	}
	if queued != 1 {
		t.Fatalf("the expansion queued %d recipients, want the one the segment names", queued)
	}

	var recipient string
	if err = harness.pool.QueryRow(ctx,
		`SELECT user_id::text FROM campaign_recipients WHERE campaign_id = $1::uuid`,
		campaign.ID).Scan(&recipient); err != nil {
		t.Fatalf("read the recipient: %v", err)
	}
	if recipient != russian {
		t.Fatalf("queued %s, want the Russian-speaking customer %s", recipient, russian)
	}
}
