//go:build integration

package integrationtest

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/omniflow/omniflow/internal/campaigns"
	"github.com/omniflow/omniflow/internal/panelpg"
)

// A broadcast's placeholders are filled per recipient: their first name
// (escaped, because it is their own text), the days left and plan of their
// latest subscription, their balance, and their own invite link. A template
// declaring a placeholder nothing fills is refused when it is saved.
func TestBroadcastVariablesAreFilledPerRecipient(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	flow := newBotFlow(ctx, t, harness)
	operations := newOperations(t, harness)
	actor := harness.operator(ctx, t, "variables@omniflow.test")

	if _, err := operations.SaveTemplate(ctx, panelpg.MessageTemplate{
		Code: "unknown-variable", Class: "marketing", BodyEN: "Hi {{nickname}}", BodyRU: "Привет {{nickname}}",
		Variables: []string{"nickname"},
	}, actor); err == nil {
		t.Fatal("a template declaring a variable nothing fills was saved")
	}

	customer := consentingCustomer(ctx, t, flow, 703001, "en")
	if err := flow.store.RememberTelegramName(ctx, 703001, "Ann <b>"); err != nil {
		t.Fatal(err)
	}
	harness.seedWallet(ctx, t, customer, "variables-wallet", 12345)
	versionID := harness.catalog(ctx, t, "variables-plan", 30000)
	subscription := harness.seedSubscription(ctx, t, customer, 1)
	harness.seedSubscriptionEntitlement(ctx, t, customer, subscription, versionID, "active", "10 days 1 hour")
	flow.app.SetBotUsername("omniflow_test_bot")

	template, err := operations.SaveTemplate(ctx, panelpg.MessageTemplate{
		Code: "variables", Class: "marketing", SubjectEN: "For {{name}}", SubjectRU: "Для {{name}}",
		BodyEN:    "Hi {{name}}, {{days_left}} days of {{plan}} left, balance {{balance}}. Invite: {{referral_link}}",
		BodyRU:    "Привет {{name}}",
		Variables: []string{"name", "days_left", "plan", "balance", "referral_link"},
	}, actor)
	if err != nil {
		t.Fatalf("save template: %v", err)
	}
	segment, err := operations.SaveSegment(ctx, panelpg.AudienceSegment{
		Code: "variables", NameEN: "en", NameRU: "en", Filters: map[string]any{"locale": "en"},
	}, actor)
	if err != nil {
		t.Fatalf("save segment: %v", err)
	}
	campaign, err := operations.CreateCampaign(ctx, "Variables", template.ID, segment.ID, actor)
	if err != nil {
		t.Fatalf("create campaign: %v", err)
	}
	if _, err = operations.SetCampaignState(ctx, campaign.ID, "running", nil, actor); err != nil {
		t.Fatalf("start campaign: %v", err)
	}
	campaigns.New(harness.pool, slog.New(slog.DiscardHandler), campaigns.Config{}).RunOnce(ctx)
	notifier := flow.notifier()
	notifier.SetBotUsername("omniflow_test_bot")
	notifier.RunOnce(ctx)

	body := flow.telegram.lastTo(t, 703001)
	for _, want := range []string{
		"For Ann &lt;b&gt;", "Hi Ann &lt;b&gt;, 10 days of variables-plan left",
		"https://t.me/omniflow_test_bot?start=ref_",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("message %q lacks %q", body, want)
		}
	}
	if !strings.Contains(body, "123.45") && !strings.Contains(body, "123,45") {
		t.Fatalf("message %q lacks the balance", body)
	}
	if strings.Contains(body, "{{") {
		t.Fatalf("message %q kept a placeholder", body)
	}
}
