//go:build integration

package integrationtest

import (
	"context"
	"log/slog"
	"testing"

	"github.com/omniflow/omniflow/internal/campaigns"
	"github.com/omniflow/omniflow/internal/panelpg"
)

// campaignFor creates and starts a marketing campaign to the customers of one
// locale.
func campaignFor(
	ctx context.Context, t *testing.T, operations *panelpg.Service, actor panelpg.Actor, locale string,
) string {
	t.Helper()
	template, err := operations.SaveTemplate(ctx, panelpg.MessageTemplate{
		Code: "fair-" + locale, Class: "marketing", SubjectEN: "Hi", SubjectRU: "Привет",
		BodyEN: "Body", BodyRU: "Текст", Variables: []string{},
	}, actor)
	if err != nil {
		t.Fatalf("save template: %v", err)
	}
	segment, err := operations.SaveSegment(ctx, panelpg.AudienceSegment{
		Code: "fair-" + locale, NameEN: locale, NameRU: locale, Filters: map[string]any{"locale": locale},
	}, actor)
	if err != nil {
		t.Fatalf("save segment: %v", err)
	}
	campaign, err := operations.CreateCampaign(ctx, "Fair "+locale, template.ID, segment.ID, actor)
	if err != nil {
		t.Fatalf("create campaign: %v", err)
	}
	if _, err = operations.SetCampaignState(ctx, campaign.ID, "running", nil, actor); err != nil {
		t.Fatalf("start campaign: %v", err)
	}
	return campaign.ID
}

func consentingCustomer(ctx context.Context, t *testing.T, flow *botFlow, telegramID int64, locale string) string {
	t.Helper()
	customerID := flow.customerOf(ctx, t, telegramID)
	if _, err := flow.harness.pool.Exec(ctx, `UPDATE users SET locale = $2 WHERE id = $1::uuid`, customerID, locale); err != nil {
		t.Fatalf("set locale: %v", err)
	}
	if _, err := flow.harness.pool.Exec(ctx, `INSERT INTO bot_preferences (user_id, marketing_enabled)
		VALUES ($1::uuid, true) ON CONFLICT (user_id) DO UPDATE SET marketing_enabled = true`, customerID); err != nil {
		t.Fatalf("record consent: %v", err)
	}
	return customerID
}

func campaignOutcomes(ctx context.Context, t *testing.T, harness *harness, campaignID string) map[string]string {
	t.Helper()
	rows, err := harness.pool.Query(ctx, `SELECT user_id::text,
		status || COALESCE('/' || suppression_reason, '') || COALESCE('/' || error_code, '')
		FROM campaign_recipients WHERE campaign_id = $1::uuid`, campaignID)
	if err != nil {
		t.Fatalf("read recipients: %v", err)
	}
	defer rows.Close()
	outcomes := map[string]string{}
	for rows.Next() {
		var user, outcome string
		if err := rows.Scan(&user, &outcome); err != nil {
			t.Fatalf("scan recipient: %v", err)
		}
		outcomes[user] = outcome
	}
	return outcomes
}

func campaignStatus(ctx context.Context, t *testing.T, harness *harness, campaignID string) string {
	t.Helper()
	var status string
	if err := harness.pool.QueryRow(ctx, `SELECT status FROM campaigns WHERE id = $1::uuid`, campaignID).Scan(&status); err != nil {
		t.Fatalf("read campaign: %v", err)
	}
	return status
}

// TestCampaignWithUnreachableRecipientsCompletes runs a campaign whose audience
// includes customers the bot cannot message. They used to be queued and never
// read — the delivery query joined the Telegram recipient — so their rows
// stayed `queued`, the campaign stayed `running` for ever, and because every
// campaign shared one queue ordered by queue time, other campaigns stalled
// behind them. Now they are resolved at expansion or at delivery, each with
// its reason, and the campaign completes.
func TestCampaignWithUnreachableRecipientsCompletes(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	flow := newBotFlow(ctx, t, harness)
	operations := newOperations(t, harness)
	actor := harness.operator(ctx, t, "fairness@omniflow.test")

	noTelegram := harness.customer(ctx, t)
	if _, err := harness.pool.Exec(ctx, `UPDATE users SET locale = 'ru' WHERE id = $1::uuid`, noTelegram); err != nil {
		t.Fatalf("set locale: %v", err)
	}
	reachable := consentingCustomer(ctx, t, flow, 702001, "ru")
	later := consentingCustomer(ctx, t, flow, 702002, "ru")
	blocked := consentingCustomer(ctx, t, flow, 702003, "ru")
	setDeliveryStatus(ctx, t, harness, blocked, "blocked")
	russian := campaignFor(ctx, t, operations, actor, "ru")

	runner := campaigns.New(harness.pool, slog.New(slog.DiscardHandler), campaigns.Config{})
	runner.RunOnce(ctx)
	// Suspended after the audience was expanded: still queued, and must not
	// stay so.
	if _, err := harness.pool.Exec(ctx, `UPDATE users SET status = 'suspended' WHERE id = $1::uuid`, later); err != nil {
		t.Fatalf("suspend customer: %v", err)
	}

	flow.notifier().RunOnce(ctx)
	runner.RunOnce(ctx)

	outcomes := campaignOutcomes(ctx, t, harness, russian)
	want := map[string]string{
		noTelegram: "suppressed/no_telegram",
		reachable:  "sent",
		later:      "suppressed/no_telegram/account_inactive",
		blocked:    "suppressed/delivery_blocked",
	}
	for customer, outcome := range want {
		if outcomes[customer] != outcome {
			t.Fatalf("recipient outcomes = %v, want %v", outcomes, want)
		}
	}
	if status := campaignStatus(ctx, t, harness, russian); status != "completed" {
		t.Fatalf("the campaign is %q with every recipient resolved, want completed", status)
	}
}

// TestCampaignDrainsPastOneBatch runs a campaign larger than one delivery
// batch. Delivery used to take 200 recipients per fifteen-minute pass, so the
// rest waited for the next pass; the broadcast drain now reads the next batch
// as soon as the last one is sent, paced only by the sender.
func TestCampaignDrainsPastOneBatch(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	flow := newBotFlow(ctx, t, harness)
	operations := newOperations(t, harness)
	actor := harness.operator(ctx, t, "drain@omniflow.test")

	const audience = 230
	for account := int64(707000); account < 707000+audience; account++ {
		consentingCustomer(ctx, t, flow, account, "ru")
	}
	russian := campaignFor(ctx, t, operations, actor, "ru")
	runner := campaigns.New(harness.pool, slog.New(slog.DiscardHandler), campaigns.Config{})
	runner.RunOnce(ctx)

	flow.notifier().RunOnce(ctx)
	var sent int
	if err := harness.pool.QueryRow(ctx, `SELECT count(*)::integer FROM campaign_recipients
		WHERE campaign_id = $1::uuid AND status = 'sent'`, russian).Scan(&sent); err != nil {
		t.Fatalf("count sent: %v", err)
	}
	if sent != audience {
		t.Fatalf("one delivery run sent %d of %d recipients", sent, audience)
	}
}

// TestCampaignBatchIsSharedBetweenCampaigns gives one campaign a backlog
// larger than a batch and starts a second, one-recipient campaign after it.
// The batch used to be a single queue ordered by queue time, so the second
// campaign waited for the whole backlog of the first.
func TestCampaignBatchIsSharedBetweenCampaigns(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	flow := newBotFlow(ctx, t, harness)
	operations := newOperations(t, harness)
	actor := harness.operator(ctx, t, "share@omniflow.test")

	for account := int64(703001); account <= 703005; account++ {
		consentingCustomer(ctx, t, flow, account, "ru")
	}
	russian := campaignFor(ctx, t, operations, actor, "ru")
	runner := campaigns.New(harness.pool, slog.New(slog.DiscardHandler), campaigns.Config{})
	runner.RunOnce(ctx)
	consentingCustomer(ctx, t, flow, 703100, "en")
	englishCampaign := campaignFor(ctx, t, operations, actor, "en")
	runner.RunOnce(ctx)

	batch, err := flow.store.PendingCampaignMessages(ctx, 2)
	if err != nil {
		t.Fatalf("read the batch: %v", err)
	}
	campaignsInBatch := map[string]bool{}
	for _, message := range batch {
		campaignsInBatch[message.CampaignID] = true
	}
	if len(batch) != 2 || !campaignsInBatch[russian] || !campaignsInBatch[englishCampaign] {
		t.Fatalf("a two-message batch holds %+v, want one recipient of each campaign", batch)
	}
}
