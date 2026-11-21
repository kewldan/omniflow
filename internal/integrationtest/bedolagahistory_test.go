//go:build integration

package integrationtest

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/accountcheckout"
	"github.com/omniflow/omniflow/internal/audience"
	"github.com/omniflow/omniflow/internal/bedolagaimport"
	"github.com/omniflow/omniflow/internal/bedolagaimportpg"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// The history that keeps a migrated base workable: everybody who ever
// subscribed in Bedolaga has spent their trial, the top-ups they paid are
// spend that segments, loyalty, and reports read, and the subscriptions that
// ran out before the migration still put their customers in the "expired"
// segments.
func TestBedolagaImportCarriesHistory(t *testing.T) {
	fixture := newBedolagaFixture(t)
	ctx := context.Background()
	service := fixture.service(t, nil)
	now := fixture.now

	// A former customer: a trial a year ago, then a paid month, both long
	// over, their Remnawave user gone. Bedolaga would never give them another
	// trial; neither may Omniflow.
	fixture.exec(ctx, t, `INSERT INTO users (id, telegram_id, username, status, has_had_paid_subscription, created_at)
		VALUES (30, 5030, 'hana', 'active', true, $1)`, now.AddDate(-1, 0, -5))
	trialStart := now.AddDate(-1, 0, 0)
	fixture.exec(ctx, t, `INSERT INTO subscriptions (id, user_id, status, is_trial, start_date, end_date, created_at)
		VALUES (30, 30, 'expired', true, $1, $2, $1)`, trialStart, trialStart.AddDate(0, 0, 3))
	paidEnd := now.AddDate(0, 0, -45)
	fixture.exec(ctx, t, `INSERT INTO subscriptions (id, user_id, status, is_trial, tariff_id, start_date, end_date, created_at)
		VALUES (31, 30, 'expired', false, $1, $2, $3, $2)`, fixture.tariffs["Безлимит"], paidEnd.AddDate(0, -1, 0), paidEnd)
	// Her two payments, an operator's manual credit that is not one, and a
	// top-up that never completed.
	fixture.exec(ctx, t, `INSERT INTO transactions (user_id, type, amount_kopeks, payment_method, is_completed, created_at, completed_at) VALUES
		(30, 'deposit', 22900, 'yookassa', true, $1, $1),
		(30, 'deposit', 22900, 'cryptobot', true, $2, NULL),
		(30, 'deposit', 50000, 'manual', true, $2, NULL),
		(30, 'deposit', 99900, 'yookassa', false, $2, NULL)`, paidEnd.AddDate(0, -1, 0), paidEnd.AddDate(0, 0, -40))

	source := fixture.read(ctx, t)
	if len(source.Payments) != 5 {
		t.Fatalf("read %d completed top-ups, want 5", len(source.Payments))
	}
	result, err := service.Apply(ctx, source, fixture.mapping, bedolagaimportpg.ApplyOptions{})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(result.Failures) != 0 || result.Remaining != 0 {
		t.Fatalf("apply: failures %+v, remaining %d", result.Failures, result.Remaining)
	}
	if result.Written["payment"] != 4 || result.Plan.Payments.Codes[bedolagaimport.CodePaymentMethodExcluded] != 1 {
		t.Fatalf("payments: written %d, plan %+v", result.Written["payment"], result.Plan.Payments)
	}

	hana := fixture.customerOf(ctx, t, 30)
	bob := fixture.customerOf(ctx, t, 2)
	grace := fixture.customerOf(ctx, t, 12)
	var origin string
	var claimedAt time.Time
	var hasOrder bool
	if err := fixture.harness.pool.QueryRow(ctx, `SELECT origin, claimed_at, order_id IS NOT NULL
		FROM trial_claims WHERE user_id = $1::uuid`, hana).Scan(&origin, &claimedAt, &hasOrder); err != nil {
		t.Fatalf("read hana's trial claim: %v", err)
	}
	if origin != bedolagaimport.TrialOriginTrial || !claimedAt.Equal(trialStart) || hasOrder {
		t.Fatalf("an expired trial is carried, dated, without an order: %s %s %t", origin, claimedAt, hasOrder)
	}
	if err := fixture.harness.pool.QueryRow(ctx, `SELECT origin, order_id IS NOT NULL FROM trial_claims
		WHERE user_id = $1::uuid`, bob).Scan(&origin, &hasOrder); err != nil {
		t.Fatalf("read bob's trial claim: %v", err)
	}
	if origin != bedolagaimport.TrialOriginTrial || !hasOrder {
		t.Fatalf("a live trial claims through its entitlement's order: %s %t", origin, hasOrder)
	}
	if err := fixture.harness.pool.QueryRow(ctx, `SELECT origin FROM trial_claims WHERE user_id = $1::uuid`, grace).Scan(&origin); err != nil ||
		origin != bedolagaimport.TrialOriginHistory {
		t.Fatalf("a payer with no subscription left has spent the trial too: %s %v", origin, err)
	}
	if fixture.count(ctx, t, `SELECT count(*) FROM operator_notifications WHERE kind = 'trial'`) != 0 {
		t.Fatal("carried-over claims are not announced as trials taken today")
	}

	trial, err := accountcheckout.NewStore(fixture.harness.pool).TrialContext(ctx, hana)
	if err != nil {
		t.Fatalf("trial context: %v", err)
	}
	if !trial.AlreadyClaimed {
		t.Fatal("a customer whose Bedolaga trial expired cannot take another")
	}

	if fixture.count(ctx, t, `SELECT count(*) FROM legacy_payments WHERE user_id = $1::uuid`, hana) != 2 ||
		fixture.count(ctx, t, `SELECT count(*) FROM legacy_subscription_periods WHERE user_id = $1::uuid AND ends_at < now()`, hana) != 2 {
		t.Fatal("hana's two payments and two periods are carried")
	}

	segment := func(filters map[string]any) map[string]bool {
		t.Helper()
		query, err := audience.Compile(filters, now)
		if err != nil {
			t.Fatalf("compile %v: %v", filters, err)
		}
		rows, err := fixture.harness.pool.Query(ctx, `SELECT u.id::text FROM users u WHERE `+query.Where, query.Args...)
		if err != nil {
			t.Fatalf("segment %v: %v", filters, err)
		}
		defer rows.Close()
		members := map[string]bool{}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			members[id] = true
		}
		return members
	}
	if !segment(map[string]any{audience.FilterSubscription: "expired"})[hana] {
		t.Fatal("a customer whose subscription ran out before the migration is in the expired segment")
	}
	if segment(map[string]any{audience.FilterSubscription: "none"})[hana] {
		t.Fatal("a former subscriber has had a subscription")
	}
	ladder := segment(map[string]any{audience.FilterExpiredDaysAgo: map[string]any{"min": float64(30), "max": float64(60)}})
	if !ladder[hana] || ladder[bob] {
		t.Fatalf("the 30–60 day step finds hana (45 days) and not bob (live trial): %v", ladder)
	}
	if segment(map[string]any{audience.FilterExpiredDaysAgo: map[string]any{"max": float64(14)}})[hana] {
		t.Fatal("hana ended 45 days ago, outside the last fortnight")
	}
	if !segment(map[string]any{audience.FilterMinimumSpendMinor: float64(45800)})[hana] {
		t.Fatal("carried payments count as spend")
	}
	if !segment(map[string]any{audience.FilterInactiveForDays: float64(60)})[hana] ||
		segment(map[string]any{audience.FilterInactiveForDays: float64(120)})[hana] {
		t.Fatal("hana last paid 75 days ago")
	}
	if !segment(map[string]any{audience.FilterTrial: "used"})[hana] {
		t.Fatal("a carried trial is a trial had")
	}

	queries := dbgen.New(fixture.harness.pool)
	hanaID := pgtype.UUID{}
	if err := hanaID.Scan(hana); err != nil {
		t.Fatal(err)
	}
	metric, err := queries.CustomerLoyaltyMetric(ctx, dbgen.CustomerLoyaltyMetricParams{UserID: hanaID, Currency: "RUB", WindowDays: 365})
	if err != nil {
		t.Fatalf("loyalty metric: %v", err)
	}
	if metric.SpendMinor != 45800 || metric.OrderCount != 2 || metric.TenureDays < 364 {
		t.Fatalf("loyalty reads the carried history: %+v", metric)
	}

	// Hana comes back and pays in Omniflow: a returning payer, not a new one.
	if _, err := fixture.harness.pool.Exec(ctx, `INSERT INTO orders (user_id, state, operation, currency, subtotal_minor,
		discount_minor, wallet_minor, external_minor, paid_minor, idempotency_key, created_at, updated_at, paid_at)
		VALUES ($1::uuid, 'paid', 'renewal', 'RUB', 22900, 0, 0, 22900, 22900, 'comeback', now(), now(), now())`, hana); err != nil {
		t.Fatalf("seed the comeback order: %v", err)
	}
	payers, err := queries.ReturningPayers(ctx, dbgen.ReturningPayersParams{
		Since: pgtype.Timestamptz{Time: now.Add(-time.Hour), Valid: true}, Until: pgtype.Timestamptz{Time: now.Add(time.Hour), Valid: true},
	})
	if err != nil {
		t.Fatalf("returning payers: %v", err)
	}
	// Alice and Frank topped up today in Bedolaga's fixture; Hana paid today
	// in Omniflow after paying before. Only she was a payer before the window.
	if payers.Payers != 3 || payers.Returning != 1 {
		t.Fatalf("returning payers: %+v", payers)
	}

	// A repeated run carries nothing twice, and verification holds.
	again, err := service.Apply(ctx, fixture.read(ctx, t), fixture.mapping, bedolagaimportpg.ApplyOptions{})
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if again.Written["payment"] != 0 || again.Written["trial"] != 0 || again.Written["period"] != 0 {
		t.Fatalf("second apply wrote history again: %+v", again.Written)
	}
	report, err := service.Verify(ctx, fixture.read(ctx, t), fixture.mapping)
	if err != nil || !report.OK {
		t.Fatalf("verify: %v %+v", err, report)
	}
}
