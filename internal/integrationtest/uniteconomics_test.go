//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/analytics"
	"github.com/omniflow/omniflow/internal/remnawave"
)

// Unit economics, activation, revenue movement, the renewal forecast, churn
// reasons, and channel value against a real database.

func TestEconomicsSpreadsCostsAndChargesProviderRates(t *testing.T) {
	ctx := context.Background()
	fixture := newAnalyticsFixture(ctx, t)
	pool := fixture.harness.pool
	now := time.Now().UTC()
	service := analytics.New(pool)
	actor := analytics.Actor{AdminID: seedOperatorID(ctx, t, fixture.harness), IdempotencyKey: "cost-1"}

	payer := seedCustomer(ctx, t, fixture.harness, "economics-payer@example.test")
	order, _ := fixture.period(ctx, t, payer, "", "economics-1", 100000,
		now.Add(-2*24*time.Hour), now.Add(-2*24*time.Hour), now.Add(28*24*time.Hour), "active")
	if _, err := pool.Exec(ctx, `INSERT INTO payment_intents (order_id, provider, status, amount_minor, currency, idempotency_key)
		VALUES ($1, 'yookassa', 'succeeded', 100000, 'RUB', 'economics-intent-1')`, order); err != nil {
		t.Fatalf("seed intent: %v", err)
	}
	// A second payment through a provider with no rate entered: its fee is
	// unknown, and the report must say so rather than count it as zero.
	other := seedCustomer(ctx, t, fixture.harness, "economics-other@example.test")
	otherOrder, _ := fixture.period(ctx, t, other, "", "economics-2", 50000,
		now.Add(-24*time.Hour), now.Add(-24*time.Hour), now.Add(29*24*time.Hour), "active")
	if _, err := pool.Exec(ctx, `INSERT INTO payment_intents (order_id, provider, status, amount_minor, currency, idempotency_key)
		VALUES ($1, 'cryptobot', 'succeeded', 50000, 'RUB', 'economics-intent-2')`, otherOrder); err != nil {
		t.Fatalf("seed intent: %v", err)
	}

	// A rate that starts after the payment never applies to it; the one in
	// force on the day does.
	if _, err := service.SetProviderFee(ctx, actor, analytics.ProviderFeeInput{
		Provider: "yookassa", EffectiveFrom: now.AddDate(0, 0, -30).Format(time.DateOnly), PercentBp: 350,
	}); err != nil {
		t.Fatalf("set fee: %v", err)
	}
	if _, err := service.SetProviderFee(ctx, actor, analytics.ProviderFeeInput{
		Provider: "yookassa", EffectiveFrom: now.AddDate(0, 0, 1).Format(time.DateOnly), PercentBp: 9000,
	}); err != nil {
		t.Fatalf("set future fee: %v", err)
	}
	// A rate from today replaces nothing older: the payments of the last two
	// days are still charged the rate in force when they were made.
	if _, err := service.SetProviderFee(ctx, actor, analytics.ProviderFeeInput{
		Provider: "yookassa", EffectiveFrom: now.Format(time.DateOnly), PercentBp: 101,
	}); err != nil {
		t.Fatalf("set today's fee: %v", err)
	}
	if _, err := service.SetProviderFee(ctx, actor, analytics.ProviderFeeInput{
		Provider: "yookassa", EffectiveFrom: "2026-01-01", PercentBp: 10001,
	}); !errors.Is(err, analytics.ErrInvalid) {
		t.Fatalf("a share over 100%% = %v, want ErrInvalid", err)
	}

	// A thirty-day server bill: a ten-day window reads a third of it.
	from := now.AddDate(0, 0, -14)
	cost, err := service.CreateOperatingCost(ctx, actor, analytics.OperatingCostInput{
		Category: "servers", CoversFrom: from.Format(time.DateOnly),
		CoversTo: from.AddDate(0, 0, 29).Format(time.DateOnly), Currency: "rub", AmountMinor: 30000,
	})
	if err != nil {
		t.Fatalf("create cost: %v", err)
	}
	// The same idempotency key returns the first cost instead of a second.
	again, err := service.CreateOperatingCost(ctx, actor, analytics.OperatingCostInput{
		Category: "servers", CoversFrom: from.Format(time.DateOnly),
		CoversTo: from.AddDate(0, 0, 29).Format(time.DateOnly), Currency: "RUB", AmountMinor: 30000,
	})
	if err != nil || again.ID != cost.ID {
		t.Fatalf("retried cost = %s, %v; want the first one, %s", again.ID, err, cost.ID)
	}
	if _, err := service.CreateOperatingCost(ctx, analytics.Actor{AdminID: actor.AdminID}, analytics.OperatingCostInput{
		Category: "servers", CoversFrom: "2026-02-10", CoversTo: "2026-02-01", Currency: "RUB", AmountMinor: 1,
	}); !errors.Is(err, analytics.ErrInvalid) {
		t.Fatalf("a span that ends before it starts = %v, want ErrInvalid", err)
	}

	windowStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -9)
	period, err := analytics.NewPeriod(windowStart, windowStart.AddDate(0, 0, 10), "UTC")
	if err != nil {
		t.Fatal(err)
	}
	report, err := service.EconomicsReport(ctx, period)
	if err != nil {
		t.Fatalf("economics: %v", err)
	}
	if len(report.Currencies) != 1 {
		t.Fatalf("currencies = %+v, want RUB only", report.Currencies)
	}
	rub := report.Currencies[0]
	if rub.ProviderMinor != 150000 {
		t.Fatalf("provider money = %d, want 150000", rub.ProviderMinor)
	}
	if rub.FeesMinor != 3500 || !rub.FeesEstimated {
		t.Fatalf("fees = %d estimated=%v, want 3500 and estimated", rub.FeesMinor, rub.FeesEstimated)
	}
	if rub.OperatingMinor != 10000 {
		t.Fatalf("running costs = %d, want a third of 30000", rub.OperatingMinor)
	}
	if rub.ContributionMinor != 150000-3500-10000 {
		t.Fatalf("contribution = %d", rub.ContributionMinor)
	}
	if rub.Margin == nil {
		t.Fatal("no margin")
	}
	var withoutRate int64
	for _, line := range report.Providers {
		withoutRate += line.WithoutRate
	}
	if withoutRate != 1 {
		t.Fatalf("payments without a rate = %d, want 1", withoutRate)
	}

	if err := service.DeleteOperatingCost(ctx, actor, cost.ID); err != nil {
		t.Fatalf("delete cost: %v", err)
	}
	if err := service.DeleteOperatingCost(ctx, actor, cost.ID); !errors.Is(err, analytics.ErrNotFound) {
		t.Fatalf("deleting twice = %v, want ErrNotFound", err)
	}
	var audits int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_events
		WHERE action IN ('operating_cost.created', 'operating_cost.deleted', 'payment_provider_fee.set')`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	// Three rates set, one cost created once despite the retry, one deleted.
	if audits != 5 {
		t.Fatalf("audit events = %d, want 5", audits)
	}
}

func TestMovementTellsNewLostAndGrownRevenue(t *testing.T) {
	ctx := context.Background()
	fixture := newAnalyticsFixture(ctx, t)
	now := time.Now().UTC()
	start := now.AddDate(0, 0, -30)

	// Paying at the start and lost by the end.
	lost := seedCustomer(ctx, t, fixture.harness, "movement-lost@example.test")
	fixture.period(ctx, t, lost, "", "movement-lost", 20000,
		start.AddDate(0, 0, -10), start.AddDate(0, 0, -10), start.AddDate(0, 0, 10), "expired")
	// Paying at both ends, more at the end.
	grower := seedCustomer(ctx, t, fixture.harness, "movement-grower@example.test")
	subscription := fixture.subscription(ctx, t, grower, 1)
	fixture.period(ctx, t, grower, subscription, "movement-grow-1", 20000,
		start.AddDate(0, 0, -5), start.AddDate(0, 0, -5), start.AddDate(0, 0, 25), "superseded")
	fixture.period(ctx, t, grower, subscription, "movement-grow-2", 40000,
		start.AddDate(0, 0, 25), start.AddDate(0, 0, 25), start.AddDate(0, 0, 55), "active")
	// New in the window.
	newcomer := seedCustomer(ctx, t, fixture.harness, "movement-new@example.test")
	fixture.period(ctx, t, newcomer, "", "movement-new", 20000,
		now.AddDate(0, 0, -3), now.AddDate(0, 0, -3), now.AddDate(0, 0, 27), "active")

	period, err := analytics.NewPeriod(start, now.Add(time.Minute), "UTC")
	if err != nil {
		t.Fatal(err)
	}
	report, err := analytics.New(fixture.harness.pool).SubscriptionReport(ctx, period)
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if len(report.Movement) != 1 {
		t.Fatalf("movement = %+v, want one currency", report.Movement)
	}
	line := report.Movement[0]
	if line.StartMrrMinor != 40000 || line.EndMrrMinor != 60000 {
		t.Fatalf("start/end = %d/%d, want 40000/60000", line.StartMrrMinor, line.EndMrrMinor)
	}
	if line.NewMrrMinor != 20000 || line.NewCustomers != 1 {
		t.Fatalf("new = %d (%d), want 20000 (1)", line.NewMrrMinor, line.NewCustomers)
	}
	if line.ExpansionMrrMinor != 20000 || line.ChurnedMrrMinor != 20000 || line.ChurnedCustomers != 1 {
		t.Fatalf("expansion/churned = %d/%d", line.ExpansionMrrMinor, line.ChurnedMrrMinor)
	}
	// (40000 + 20000 − 0 − 20000) ÷ 40000.
	if line.NetRevenueRetention == nil || *line.NetRevenueRetention != 1 {
		t.Fatalf("net revenue retention = %v, want 1", line.NetRevenueRetention)
	}
	if line.GrossRevenueRetention == nil || *line.GrossRevenueRetention != 0.5 {
		t.Fatalf("gross revenue retention = %v, want 0.5", line.GrossRevenueRetention)
	}

	// The newcomer's period ends in 27 days and the grower's in 25, neither
	// with a successor: both are due, each worth the plan's list price
	// whatever was paid for the period itself.
	if len(report.Forecast.Lines) != 1 || report.Forecast.Lines[0].Due != 2 || report.Forecast.Lines[0].ValueMinor != 40000 {
		t.Fatalf("forecast = %+v, want two periods due worth the list price", report.Forecast.Lines)
	}
}

func TestChurnReasonsAreTheCustomersOwnAndCountedOnceADay(t *testing.T) {
	ctx := context.Background()
	fixture := newAnalyticsFixture(ctx, t)
	service := analytics.New(fixture.harness.pool)
	customer := seedCustomer(ctx, t, fixture.harness, "churn@example.test")
	subscription := fixture.subscription(ctx, t, customer, 1)
	stranger := seedCustomer(ctx, t, fixture.harness, "churn-stranger@example.test")

	if err := service.RecordChurnReason(ctx, customer, subscription, analytics.OccasionAutoRenewOff, "price", analytics.SurfaceBot); err != nil {
		t.Fatalf("record: %v", err)
	}
	// A second answer the same day replaces the first.
	if err := service.RecordChurnReason(ctx, customer, subscription, analytics.OccasionAutoRenewOff, "blocked", analytics.SurfaceWeb); err != nil {
		t.Fatalf("re-record: %v", err)
	}
	if err := service.RecordChurnReason(ctx, stranger, subscription, analytics.OccasionAutoRenewOff, "price", analytics.SurfaceWeb); !errors.Is(err, analytics.ErrNotFound) {
		t.Fatalf("another customer's subscription = %v, want ErrNotFound", err)
	}
	if err := service.RecordChurnReason(ctx, customer, "", analytics.OccasionLapsed, "bored", analytics.SurfaceBot); !errors.Is(err, analytics.ErrInvalid) {
		t.Fatalf("an unknown reason = %v, want ErrInvalid", err)
	}
	if err := service.RecordChurnReason(ctx, customer, "", analytics.OccasionLapsed, "speed", analytics.SurfaceBot); err != nil {
		t.Fatalf("record without a subscription: %v", err)
	}

	now := time.Now().UTC()
	period, _ := analytics.NewPeriod(now.Add(-time.Hour), now.Add(time.Hour), "UTC")
	report, err := service.SubscriptionReport(ctx, period)
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	got := map[string]int64{}
	for _, line := range report.ChurnReasons {
		got[line.Occasion+"/"+line.Reason] = line.Answers
	}
	if len(got) != 2 || got["auto_renew_off/blocked"] != 1 || got["lapsed/speed"] != 1 {
		t.Fatalf("reasons = %v, want the replaced answer and the lapsed one", got)
	}
}

type fakeUsers struct {
	users   []remnawave.User
	devices []remnawave.PanelDevice
}

func (fake *fakeUsers) ListUsers(_ context.Context, start, size int) ([]remnawave.User, int, error) {
	if start >= len(fake.users) {
		return nil, len(fake.users), nil
	}
	return fake.users[start:min(start+size, len(fake.users))], len(fake.users), nil
}

func (fake *fakeUsers) AllDevices(_ context.Context, start, size int) ([]remnawave.PanelDevice, int, error) {
	if start >= len(fake.devices) {
		return nil, len(fake.devices), nil
	}
	return fake.devices[start:min(start+size, len(fake.devices))], len(fake.devices), nil
}

func TestUsageCensusMeasuresActivationAndNodes(t *testing.T) {
	ctx := context.Background()
	fixture := newAnalyticsFixture(ctx, t)
	pool := fixture.harness.pool
	now := time.Now().UTC()
	service := analytics.New(pool)

	// Two trials: one connected an hour after claiming and then paid, one
	// never connected.
	connected := seedCustomer(ctx, t, fixture.harness, "usage-connected@example.test")
	silent := seedCustomer(ctx, t, fixture.harness, "usage-silent@example.test")
	for index, customer := range []string{connected, silent} {
		subscription := fixture.subscription(ctx, t, customer, 1)
		if _, err := pool.Exec(ctx, `UPDATE subscriptions SET remnawave_user_id = $2 WHERE id = $1`,
			subscription, 100+index); err != nil {
			t.Fatalf("link subscription: %v", err)
		}
		trialOrder, _ := fixture.period(ctx, t, customer, "", "usage-trial-"+customer, 0,
			now.Add(-48*time.Hour), now.Add(-48*time.Hour), now.Add(-24*time.Hour), "expired")
		if _, err := pool.Exec(ctx, `INSERT INTO trial_claims (user_id, plan_id, order_id, claimed_at)
			SELECT $1, plan_id, $3, $4 FROM plan_versions WHERE id = $2`,
			customer, fixture.planVersion, trialOrder, now.Add(-48*time.Hour)); err != nil {
			t.Fatalf("seed trial: %v", err)
		}
	}
	fixture.period(ctx, t, connected, "", "usage-paid", 20000,
		now.Add(-24*time.Hour), now.Add(-24*time.Hour), now.Add(29*24*time.Hour), "active")

	first := now.Add(-47 * time.Hour)
	online := now.Add(-time.Hour)
	limit := 2
	panel := &fakeUsers{
		users: []remnawave.User{
			{ID: 100, HWIDDeviceLimit: &limit, Traffic: remnawave.UserTraffic{
				FirstConnectedAt: &first, OnlineAt: &online,
				LastConnectedNodeUUID: "11111111-1111-1111-1111-111111111111",
			}},
			{ID: 101},
		},
		devices: []remnawave.PanelDevice{
			{UserID: 100, UpdatedAt: now.Add(-time.Hour)},
			{UserID: 100, UpdatedAt: now.Add(-2 * time.Hour)},
			// A device not seen for two months no longer counts.
			{UserID: 100, UpdatedAt: now.AddDate(0, -2, 0)},
		},
	}
	recorded, err := service.CensusUsage(ctx, panel)
	if err != nil || recorded != 2 {
		t.Fatalf("census = %d, %v; want 2", recorded, err)
	}
	// A second pass with a later first connection never moves it forward.
	later := now
	panel.users[0].Traffic.FirstConnectedAt = &later
	if _, err := service.CensusUsage(ctx, panel); err != nil {
		t.Fatalf("second census: %v", err)
	}

	period, _ := analytics.NewPeriod(now.Add(-72*time.Hour), now.Add(time.Minute), "UTC")
	report, err := service.UsageReport(ctx, period)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if report.ObservedAt == nil {
		t.Fatal("no census moment")
	}
	trials := report.Trials
	if trials.Trials != 2 || trials.Connected != 1 || trials.ConnectedWithinDay != 1 {
		t.Fatalf("trials = %+v", trials)
	}
	if trials.ConvertedConnected != 1 || trials.ConvertedUnconnected != 0 {
		t.Fatalf("conversions = %+v", trials)
	}
	if trials.MedianHoursToConnect == nil || *trials.MedianHoursToConnect < 0.9 || *trials.MedianHoursToConnect > 1.1 {
		t.Fatalf("median hours = %v, want about one", trials.MedianHoursToConnect)
	}
	paying := report.Paying
	if paying.Customers != 1 || paying.OnlineWeek != 1 || paying.AtDeviceLimit != 1 || paying.OverDeviceLimit != 0 {
		t.Fatalf("paying = %+v", paying)
	}
	if len(report.Nodes) != 1 || report.Nodes[0].PayingCustomers != 1 {
		t.Fatalf("nodes = %+v, want the one node with its paying customer", report.Nodes)
	}
}

func TestChannelValueAndTheReferralProgramme(t *testing.T) {
	ctx := context.Background()
	fixture := newAnalyticsFixture(ctx, t)
	pool := fixture.harness.pool
	now := time.Now().UTC()
	service := analytics.New(pool)

	inviter := seedCustomer(ctx, t, fixture.harness, "inviter@example.test")
	invited := seedCustomer(ctx, t, fixture.harness, "invited@example.test")
	organic := seedCustomer(ctx, t, fixture.harness, "organic@example.test")
	if _, err := pool.Exec(ctx, `INSERT INTO referral_attributions (referred_user_id, referrer_user_id, code, created_at)
		VALUES ($1, $2, 'FRIEND', now() - interval '5 days')`, invited, inviter); err != nil {
		t.Fatalf("seed attribution: %v", err)
	}
	order, _ := fixture.period(ctx, t, invited, "", "channel-invited", 30000,
		now.Add(-4*24*time.Hour), now.Add(-4*24*time.Hour), now.Add(26*24*time.Hour), "active")
	fixture.period(ctx, t, organic, "", "channel-organic", 20000,
		now.Add(-3*24*time.Hour), now.Add(-3*24*time.Hour), now.Add(27*24*time.Hour), "active")
	var transaction string
	if err := pool.QueryRow(ctx, `INSERT INTO ledger_transactions (type, reference_type, reference_id, idempotency_key)
		VALUES ('referral_reward', 'referral_reward', 'test', 'channel-reward') RETURNING id`).Scan(&transaction); err != nil {
		t.Fatalf("seed ledger: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO referral_rewards (referred_user_id, beneficiary_user_id, role, order_id, amount_minor, currency, ledger_transaction_id)
		VALUES ($1, $2, 'inviter', $3, 6000, 'RUB', $4)`, invited, inviter, order, transaction); err != nil {
		t.Fatalf("seed reward: %v", err)
	}

	period, _ := analytics.NewPeriod(now.AddDate(0, 0, -7), now.Add(time.Minute), "UTC")
	report, err := service.AcquisitionReport(ctx, period)
	if err != nil {
		t.Fatalf("acquisition: %v", err)
	}
	channels := map[string]analytics.ChannelValueLine{}
	for _, line := range report.ChannelValue {
		channels[line.Channel] = line
	}
	referral, ok := channels["referral"]
	if !ok || referral.Payers != 1 || referral.RevenueMinor != 30000 || referral.SpendMinor != 6000 {
		t.Fatalf("referral channel = %+v", referral)
	}
	if referral.CacMinor == nil || *referral.CacMinor != 6000 || referral.LtvToCac == nil || *referral.LtvToCac != 5 {
		t.Fatalf("referral economics = %+v", referral)
	}
	if referral.PaybackMonths == nil {
		t.Fatal("no payback for a channel with cost and revenue")
	}
	if line := channels["organic"]; line.Payers != 1 || line.CacMinor != nil {
		t.Fatalf("organic channel = %+v, want one payer and no cost", line)
	}
	if report.Referrals.Invited != 1 || report.Referrals.Payers != 1 || len(report.Referrals.Rewards) != 1 {
		t.Fatalf("referrals = %+v", report.Referrals)
	}
	if len(report.ByLocale) != 1 || report.ByLocale[0].Locale != "en" || report.ByLocale[0].Signups < 3 {
		t.Fatalf("by locale = %+v", report.ByLocale)
	}
}
