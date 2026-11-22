//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/omniflow/omniflow/internal/accountsupport"
	"github.com/omniflow/omniflow/internal/analytics"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/remnawave"
)

// Business analytics against a real database: every query the reports run,
// with fixtures shaped like the periods the commerce core writes.

type analyticsFixture struct {
	harness     *harness
	planVersion string
}

func newAnalyticsFixture(ctx context.Context, t *testing.T) analyticsFixture {
	t.Helper()
	harness := newHarness(t)
	var planVersion string
	if err := harness.pool.QueryRow(ctx, `
		WITH plan AS (
			INSERT INTO plans (code, kind) VALUES ('monthly', 'one_time') RETURNING id
		), version AS (
			INSERT INTO plan_versions (plan_id, version, billing_period, duration_seconds)
			SELECT id, 1, 'month', 2592000 FROM plan RETURNING id
		)
		INSERT INTO plan_prices (plan_version_id, currency, amount_minor)
		SELECT id, 'RUB', 20000 FROM version
		RETURNING plan_version_id`,
	).Scan(&planVersion); err != nil {
		t.Fatalf("seed plan: %v", err)
	}
	return analyticsFixture{harness: harness, planVersion: planVersion}
}

// period seeds one order and its entitlement. key distinguishes orders;
// net is the price after discount, and a key starting with "bedolaga:" makes
// it an imported period.
func (fixture analyticsFixture) period(
	ctx context.Context, t *testing.T, customer, subscription, key string, net int64,
	paidAt, starts, ends time.Time, status string,
) (order, entitlement string) {
	t.Helper()
	pool := fixture.harness.pool
	if err := pool.QueryRow(ctx, `
		INSERT INTO orders (
			user_id, state, operation, currency, subtotal_minor, external_minor,
			paid_minor, idempotency_key, paid_at, subscription_id
		) VALUES ($1, 'fulfilled', 'purchase', 'RUB', $2, $2, $2, $3, $4, NULLIF($5, '')::uuid)
		RETURNING id`, customer, net, key, paidAt, subscription,
	).Scan(&order); err != nil {
		t.Fatalf("seed order %s: %v", key, err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO entitlements (
			user_id, order_id, plan_version_id, status, starts_at, ends_at, subscription_id
		) VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, '')::uuid)
		RETURNING id`, customer, order, fixture.planVersion, status, starts, ends, subscription,
	).Scan(&entitlement); err != nil {
		t.Fatalf("seed entitlement %s: %v", key, err)
	}
	return order, entitlement
}

func (fixture analyticsFixture) subscription(ctx context.Context, t *testing.T, customer string, slot int) string {
	t.Helper()
	var id string
	if err := fixture.harness.pool.QueryRow(ctx,
		`INSERT INTO subscriptions (user_id, slot, label) VALUES ($1, $2, 'main') RETURNING id`,
		customer, slot,
	).Scan(&id); err != nil {
		t.Fatalf("seed subscription: %v", err)
	}
	return id
}

func TestSnapshotsSeparatePaidImportedAndTrialAndBackfill(t *testing.T) {
	ctx := context.Background()
	fixture := newAnalyticsFixture(ctx, t)
	pool := fixture.harness.pool
	now := time.Now().UTC()

	paying := seedCustomer(ctx, t, fixture.harness, "paying@example.test")
	fixture.period(ctx, t, paying, "", "paid-1", 30000,
		now.Add(-10*24*time.Hour), now.Add(-10*24*time.Hour), now.Add(20*24*time.Hour), "active")

	imported := seedCustomer(ctx, t, fixture.harness, "imported@example.test")
	fixture.period(ctx, t, imported, "", "bedolaga:sub:1", 0,
		now.Add(-40*24*time.Hour), now.Add(-5*24*time.Hour), now.Add(25*24*time.Hour), "active")

	trial := seedCustomer(ctx, t, fixture.harness, "trial@example.test")
	trialOrder, _ := fixture.period(ctx, t, trial, "", "trial-1", 0,
		now.Add(-time.Hour), now.Add(-time.Hour), now.Add(2*24*time.Hour), "active")
	if _, err := pool.Exec(ctx, `INSERT INTO trial_claims (user_id, plan_id, order_id)
		SELECT $1, plan_id, $2 FROM plan_versions WHERE id = $3`, trial, trialOrder, fixture.planVersion); err != nil {
		t.Fatalf("seed trial claim: %v", err)
	}

	// A refunded order is not part of the base, whatever its entitlement says.
	refunded := seedCustomer(ctx, t, fixture.harness, "refunded@example.test")
	refundedOrder, _ := fixture.period(ctx, t, refunded, "", "refunded-1", 30000,
		now.Add(-3*24*time.Hour), now.Add(-3*24*time.Hour), now.Add(27*24*time.Hour), "active")
	if _, err := pool.Exec(ctx, `UPDATE orders SET state = 'refunded', refunded_minor = paid_minor WHERE id = $1`, refundedOrder); err != nil {
		t.Fatalf("refund: %v", err)
	}

	service := analytics.New(pool)
	taken, err := service.TakeSnapshots(ctx)
	if err != nil {
		t.Fatalf("snapshots: %v", err)
	}
	if taken < 2 {
		t.Fatalf("took %d snapshots, want today and a backfill", taken)
	}

	period, err := analytics.NewPeriod(now.Add(-30*24*time.Hour), now.Add(time.Minute), "UTC")
	if err != nil {
		t.Fatal(err)
	}
	report, err := service.SubscriptionReport(ctx, period)
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if len(report.Base) == 0 {
		t.Fatal("no base trend")
	}
	latest := report.Base[len(report.Base)-1]
	if latest.Paid != 1 || latest.Imported != 1 || latest.Trial != 1 {
		t.Fatalf("today's base = %+v, want one paid, one imported, one trial and no refunded", latest)
	}
	var mrr, estimated int64
	for _, line := range report.Mrr {
		if line.Day == latest.Day && line.Currency == "RUB" {
			mrr, estimated = line.MrrMinor, line.EstimatedMinor
		}
	}
	if mrr != 30000 || estimated != 20000 {
		t.Fatalf("MRR = %d, estimated = %d; want the price charged and the list price for the import", mrr, estimated)
	}
	// Eight days ago only the paying customer had access.
	eightDaysAgo := now.Add(-8 * 24 * time.Hour).Format(time.DateOnly)
	for _, day := range report.Base {
		if day.Day == eightDaysAgo && (day.Paid != 1 || day.Imported != 0) {
			t.Fatalf("the backfill for %s = %+v, want the one paid period", eightDaysAgo, day)
		}
	}

	// A second pass replaces today's reading and backfills nothing twice.
	again, err := service.TakeSnapshots(ctx)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if again > taken {
		t.Fatalf("second pass took %d, more than the first %d", again, taken)
	}
}

func TestRenewalsTellRenewedFromChurnedAndPending(t *testing.T) {
	ctx := context.Background()
	fixture := newAnalyticsFixture(ctx, t)
	now := time.Now().UTC()
	day := 24 * time.Hour

	renewer := seedCustomer(ctx, t, fixture.harness, "renewer@example.test")
	renewerSub := fixture.subscription(ctx, t, renewer, 1)
	fixture.period(ctx, t, renewer, renewerSub, "r-1", 20000,
		now.Add(-40*day), now.Add(-40*day), now.Add(-10*day), "superseded")
	fixture.period(ctx, t, renewer, renewerSub, "r-2", 20000,
		now.Add(-11*day), now.Add(-10*day), now.Add(20*day), "active")

	lapsed := seedCustomer(ctx, t, fixture.harness, "lapsed@example.test")
	fixture.period(ctx, t, lapsed, "", "l-1", 20000,
		now.Add(-50*day), now.Add(-50*day), now.Add(-20*day), "expired")

	recent := seedCustomer(ctx, t, fixture.harness, "recent@example.test")
	fixture.period(ctx, t, recent, "", "p-1", 20000,
		now.Add(-32*day), now.Add(-32*day), now.Add(-2*day), "expired")

	service := analytics.New(fixture.harness.pool)
	period, err := analytics.NewPeriod(now.Add(-30*day), now, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	report, err := service.SubscriptionReport(ctx, period)
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	renewals := report.Renewals
	if renewals.Ended != 3 || renewals.Renewed != 1 || renewals.Churned != 1 || renewals.Pending != 1 {
		t.Fatalf("renewals = %+v, want 3 ended: 1 renewed, 1 churned, 1 pending", renewals)
	}
	if renewals.RenewalRate == nil || *renewals.RenewalRate != 0.5 {
		t.Fatalf("renewal rate = %v, want 0.5 with the pending period left out", renewals.RenewalRate)
	}
	if len(report.Cohorts) == 0 {
		t.Fatal("no cohorts for customers who paid")
	}
}

func TestAcquisitionSpendCostAndFunnel(t *testing.T) {
	ctx := context.Background()
	fixture := newAnalyticsFixture(ctx, t)
	pool := fixture.harness.pool
	now := time.Now().UTC()

	var campaign string
	if err := pool.QueryRow(ctx,
		`INSERT INTO ad_campaigns (code, name) VALUES ('spring', 'Spring') RETURNING id`,
	).Scan(&campaign); err != nil {
		t.Fatalf("seed campaign: %v", err)
	}
	payer := seedCustomer(ctx, t, fixture.harness, "campaign-payer@example.test")
	browser := seedCustomer(ctx, t, fixture.harness, "campaign-browser@example.test")
	for _, customer := range []string{payer, browser} {
		if _, err := pool.Exec(ctx, `INSERT INTO customer_campaign_attributions (user_id, campaign_id, attributed_at)
			VALUES ($1, $2, now() - interval '2 days')`, customer, campaign); err != nil {
			t.Fatalf("attribute: %v", err)
		}
	}
	seedSettledOrder(ctx, t, fixture.harness, payer, "purchase", "RUB", 30000, now.Add(-24*time.Hour))

	service := analytics.New(pool)
	actor := analytics.Actor{AdminID: seedOperatorID(ctx, t, fixture.harness)}
	if _, err := service.CreateSpend(ctx, actor, analytics.SpendInput{
		CampaignID: campaign, SpentOn: now.Add(-2 * 24 * time.Hour).Format(time.DateOnly),
		Currency: "rub", AmountMinor: 10000,
	}); err != nil {
		t.Fatalf("spend: %v", err)
	}
	retried := actor
	retried.IdempotencyKey = "spend-yandex-1"
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := service.CreateSpend(ctx, retried, analytics.SpendInput{
			UtmSource: "Yandex", SpentOn: now.Format(time.DateOnly), Currency: "RUB", AmountMinor: 5000,
		}); err != nil {
			t.Fatalf("source spend attempt %d: %v", attempt, err)
		}
	}
	_, err := service.CreateSpend(ctx, actor, analytics.SpendInput{
		CampaignID: campaign, UtmSource: "yandex", SpentOn: now.Format(time.DateOnly), Currency: "RUB", AmountMinor: 1,
	})
	if !errors.Is(err, analytics.ErrInvalid) {
		t.Fatalf("a cost against both a campaign and a source = %v, want invalid", err)
	}

	if err := service.RecordFunnelStep(ctx, browser, analytics.StepPlansViewed, analytics.ChannelBot); err != nil {
		t.Fatalf("funnel: %v", err)
	}
	if err := service.RecordFunnelStep(ctx, browser, analytics.StepPlansViewed, analytics.ChannelWeb); err != nil {
		t.Fatalf("second funnel step must be a no-op, got %v", err)
	}
	if err := service.RecordFunnelStep(ctx, payer, analytics.StepCheckoutStarted, analytics.ChannelWeb); err != nil {
		t.Fatalf("funnel: %v", err)
	}

	period, err := analytics.NewPeriod(now.Add(-7*24*time.Hour), now.Add(time.Minute), "UTC")
	if err != nil {
		t.Fatal(err)
	}
	report, err := service.AcquisitionReport(ctx, period)
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if len(report.Campaigns) != 1 {
		t.Fatalf("campaigns = %+v", report.Campaigns)
	}
	line := report.Campaigns[0]
	if line.Signups != 2 || line.Payers != 1 || len(line.Economics) != 1 {
		t.Fatalf("campaign = %+v", line)
	}
	economics := line.Economics[0]
	if economics.CacMinor == nil || *economics.CacMinor != 10000 || economics.Roi == nil || *economics.Roi != 2 {
		t.Fatalf("economics = %+v, want CAC 10000 and ROI 2", economics)
	}
	foundSource := false
	for _, source := range report.Sources {
		if source.UtmSource == "yandex" && source.SpendMinor == 5000 && source.Orders == 0 {
			foundSource = true
		}
	}
	if !foundSource {
		t.Fatalf("spend on a source that brought nothing is missing: %+v", report.Sources)
	}
	if report.Funnel.PlansViewed != 1 || report.Funnel.CheckoutStarted != 1 || report.Funnel.Paid != 1 {
		t.Fatalf("funnel = %+v", report.Funnel)
	}

	spend, err := service.ListSpend(ctx, period)
	if err != nil || len(spend) != 2 {
		t.Fatalf("spend list = %+v, %v", spend, err)
	}
	if err := service.DeleteSpend(ctx, actor, spend[0].ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := service.DeleteSpend(ctx, actor, spend[0].ID); !errors.Is(err, analytics.ErrNotFound) {
		t.Fatalf("deleting twice = %v, want not found", err)
	}
	var audited int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE target_type = 'acquisition_spend'`).Scan(&audited); err != nil || audited != 3 {
		t.Fatalf("audit events = %d, %v; want two creations and a deletion", audited, err)
	}
}

type fakeNodes struct{ nodes []remnawave.Node }

func (fake *fakeNodes) Nodes(context.Context) ([]remnawave.Node, error) { return fake.nodes, nil }

func TestTrafficHistoryHandlesACounterReset(t *testing.T) {
	ctx := context.Background()
	fixture := newAnalyticsFixture(ctx, t)
	start := time.Date(2026, 11, 10, 10, 30, 0, 0, time.UTC)
	clock := start
	service := analytics.New(fixture.harness.pool).WithClock(func() time.Time { return clock })
	online := 7
	panel := &fakeNodes{}
	// 2000 up, a wobble down that is not a reset, then a reset to 500.
	for index, used := range []int64{1000, 3000, 2990, 500} {
		clock = start.Add(time.Duration(index) * time.Hour)
		panel.nodes = []remnawave.Node{{UUID: "node-1", Name: "Amsterdam", CountryCode: "NL",
			IsConnected: true, TrafficUsedBytes: used, TrafficLimitBytes: 10000, UsersOnline: &online}}
		if _, err := service.SampleNodes(ctx, panel); err != nil {
			t.Fatalf("sample: %v", err)
		}
	}
	period, err := analytics.NewPeriod(start.Add(-time.Hour), start.Add(24*time.Hour), "UTC")
	if err != nil {
		t.Fatal(err)
	}
	history, err := service.TrafficHistory(ctx, period)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(history.Nodes) != 1 || history.Nodes[0].TotalBytes != 2500 || history.Nodes[0].PeakOnline != 7 {
		t.Fatalf("history = %+v, want 2000 before the reset, nothing for the wobble, and 500 after it", history.Nodes)
	}
}

func TestSupportRatingIsAskedOnceAndCountedOnce(t *testing.T) {
	ctx := context.Background()
	fixture := newAnalyticsFixture(ctx, t)
	pool := fixture.harness.pool
	customer := seedCustomer(ctx, t, fixture.harness, "rated@example.test")
	operator := seedOperatorID(ctx, t, fixture.harness)
	var ticket string
	if err := pool.QueryRow(ctx, `INSERT INTO support_tickets (user_id, queue_id, status, resolved_at, assignee_id)
		SELECT $1, id, 'resolved', now() - interval '1 hour', $2 FROM support_queues WHERE is_default LIMIT 1
		RETURNING id`, customer, operator).Scan(&ticket); err != nil {
		t.Fatalf("seed ticket: %v", err)
	}
	// A ticket resolved a month ago is never asked about.
	if _, err := pool.Exec(ctx, `INSERT INTO support_tickets (user_id, queue_id, status, resolved_at)
		SELECT $1, id, 'resolved', now() - interval '30 days' FROM support_queues WHERE is_default LIMIT 1`, customer); err != nil {
		t.Fatalf("seed old ticket: %v", err)
	}

	service := analytics.New(pool)
	due, err := service.PendingRatingRequests(ctx, 10)
	if err != nil || len(due) != 1 || due[0].TicketID != ticket {
		t.Fatalf("due = %+v, %v; want only the recent ticket", due, err)
	}
	if err := service.MarkRatingRequested(ctx, ticket); err != nil {
		t.Fatal(err)
	}
	if due, _ := service.PendingRatingRequests(ctx, 10); len(due) != 0 {
		t.Fatalf("asked twice: %+v", due)
	}
	stranger := seedCustomer(ctx, t, fixture.harness, "stranger@example.test")
	if taken, _ := service.RecordRating(ctx, stranger, ticket, 1); taken {
		t.Fatal("a customer rated somebody else's ticket")
	}
	if taken, err := service.RecordRating(ctx, customer, ticket, 5); !taken || err != nil {
		t.Fatalf("rating = %v, %v", taken, err)
	}
	if taken, _ := service.RecordRating(ctx, customer, ticket, 1); taken {
		t.Fatal("a second tap changed the mark")
	}
	period, err := analytics.NewPeriod(time.Now().Add(-24*time.Hour), time.Now().Add(time.Minute), "UTC")
	if err != nil {
		t.Fatal(err)
	}
	satisfaction, err := service.SupportSatisfaction(ctx, period)
	if err != nil {
		t.Fatalf("satisfaction: %v", err)
	}
	if satisfaction.Ratings != 1 || satisfaction.Csat == nil || *satisfaction.Csat != 1 ||
		satisfaction.Distribution[4] != 1 || len(satisfaction.Operators) != 1 {
		t.Fatalf("satisfaction = %+v", satisfaction)
	}
}

func seedOperatorID(ctx context.Context, t *testing.T, harness *harness) string {
	t.Helper()
	return harness.operator(ctx, t, "analyst@example.test").AdminID
}

func TestAMergeKeepsTheEarliestFunnelStep(t *testing.T) {
	ctx := context.Background()
	fixture := newAnalyticsFixture(ctx, t)
	pool := fixture.harness.pool
	source := seedCustomer(ctx, t, fixture.harness, "funnel-source@example.test")
	target := seedCustomer(ctx, t, fixture.harness, "funnel-target@example.test")
	for _, row := range []struct {
		user, step, channel, at string
	}{
		{source, "plans_viewed", "bot", "2026-01-01T00:00:00Z"},
		{target, "plans_viewed", "web", "2026-02-01T00:00:00Z"},
		{source, "checkout_started", "bot", "2026-01-02T00:00:00Z"},
	} {
		if _, err := pool.Exec(ctx, `INSERT INTO funnel_events (user_id, step, channel, occurred_at) VALUES ($1, $2, $3, $4)`,
			row.user, row.step, row.channel, row.at); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	var sourceID, targetID pgtype.UUID
	_ = sourceID.Scan(source)
	_ = targetID.Scan(target)
	if err := dbgen.New(pool).MoveCustomerFunnelEvents(ctx, dbgen.MoveCustomerFunnelEventsParams{Source: sourceID, Target: targetID}); err != nil {
		t.Fatalf("move: %v", err)
	}
	var left int
	var channel string
	var steps int
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM funnel_events WHERE user_id = $1),
		(SELECT channel FROM funnel_events WHERE user_id = $2 AND step = 'plans_viewed'),
		(SELECT count(*) FROM funnel_events WHERE user_id = $2)`, source, target).Scan(&left, &channel, &steps); err != nil {
		t.Fatal(err)
	}
	if left != 0 || channel != "bot" || steps != 2 {
		t.Fatalf("after the move: %d left on the source, plans_viewed on %q, %d steps on the target", left, channel, steps)
	}
}

func TestTheCabinetTakesOneMarkForAFinishedTicket(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	support := newAccountSupport(t, harness)
	customer := seedCustomer(ctx, t, harness, "cabinet-rating@example.test")
	var open, resolved string
	for _, row := range []struct {
		status string
		target *string
	}{{"open", &open}, {"resolved", &resolved}} {
		if err := harness.pool.QueryRow(ctx, `INSERT INTO support_tickets (user_id, queue_id, status, resolved_at)
			SELECT $1, id, $2, CASE WHEN $2 = 'resolved' THEN now() - interval '1 hour' END FROM support_queues WHERE is_default LIMIT 1
			RETURNING id`, customer, row.status).Scan(row.target); err != nil {
			t.Fatalf("seed %s ticket: %v", row.status, err)
		}
	}

	if _, err := support.Rate(ctx, customer, open, 5); !errors.Is(err, accountsupport.ErrRatingClosed) {
		t.Fatalf("rating an open ticket = %v, want refused", err)
	}
	if _, err := support.Rate(ctx, customer, resolved, 6); err == nil {
		t.Fatal("a mark of 6 was taken")
	}
	stranger := seedCustomer(ctx, t, harness, "cabinet-stranger@example.test")
	if _, err := support.Rate(ctx, stranger, resolved, 1); !errors.Is(err, accountsupport.ErrNotFound) {
		t.Fatalf("rating somebody else's ticket = %v, want not found", err)
	}
	ticket, err := support.Rate(ctx, customer, resolved, 4)
	if err != nil || ticket.Rating == nil || *ticket.Rating != 4 || ticket.CanRate {
		t.Fatalf("rate = %+v, %v", ticket, err)
	}
	if _, err := support.Rate(ctx, customer, resolved, 1); !errors.Is(err, accountsupport.ErrRatingClosed) {
		t.Fatalf("a second mark = %v, want refused", err)
	}
	// Answered in the browser, so the bot does not ask again.
	due, err := analytics.New(harness.pool).PendingRatingRequests(ctx, 10)
	if err != nil || len(due) != 0 {
		t.Fatalf("the bot would still ask: %+v, %v", due, err)
	}
}
