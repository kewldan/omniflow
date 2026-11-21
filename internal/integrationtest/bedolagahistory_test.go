//go:build integration

package integrationtest

import (
	"context"
	"strconv"
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

	// A landing purchase paid through a provider, and one paid from the
	// balance, which is not a payment of its own.
	fixture.exec(ctx, t, `INSERT INTO transactions (user_id, type, amount_kopeks, payment_method, is_completed, created_at) VALUES
		(30, 'subscription_payment', 19900, 'platega', true, $1),
		(30, 'subscription_payment', 19900, 'balance', true, $1)`, paidEnd.AddDate(0, 0, -60))
	// Gifts: one Hana bought through a provider that its recipient is still
	// to claim, one she paid from the balance and was delivered.
	fixture.exec(ctx, t, `INSERT INTO guest_purchases (token, contact_type, contact_value, is_gift, buyer_user_id,
		period_days, amount_kopeks, payment_method, status, paid_at) VALUES
		('g1', 'telegram', '@friend', true, 30, 30, 30000, 'yookassa', 'pending_activation', $1),
		('g2', 'telegram', '@other', true, 30, 30, 30000, 'balance', 'delivered', $1),
		('g3', 'telegram', '@nobody', true, NULL, 30, 30000, 'yookassa', 'pending', NULL)`, paidEnd)
	// Her last discount: a discount code twenty days ago, after an older offer.
	fixture.exec(ctx, t, `INSERT INTO promocodes (code, type) VALUES ('SALE20', 'discount'), ('BONUS', 'balance')`)
	lastDiscount := now.AddDate(0, 0, -20)
	fixture.exec(ctx, t, `INSERT INTO promocode_uses (promocode_id, user_id, used_at)
		SELECT id, 30, CASE WHEN code = 'SALE20' THEN $1::timestamptz ELSE $2::timestamptz END FROM promocodes
		WHERE code IN ('SALE20', 'BONUS')`, lastDiscount, now.AddDate(0, 0, -1))
	fixture.exec(ctx, t, `INSERT INTO discount_offers (user_id, discount_percent, expires_at, claimed_at)
		VALUES (30, 15, $1, $1)`, now.AddDate(0, -3, 0))

	source := fixture.read(ctx, t)
	if len(source.Payments) != 7 || len(source.Gifts) != 1 || len(source.Discounts) != 1 ||
		!source.Discounts[0].At.Equal(lastDiscount) {
		t.Fatalf("read %d payments, %d unclaimed gifts, discounts %+v", len(source.Payments), len(source.Gifts), source.Discounts)
	}
	result, err := service.Apply(ctx, source, fixture.mapping, bedolagaimportpg.ApplyOptions{})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(result.Failures) != 0 || result.Remaining != 0 {
		t.Fatalf("apply: failures %+v, remaining %d", result.Failures, result.Remaining)
	}
	if result.Written["payment"] != 6 || result.Plan.Payments.Codes[bedolagaimport.CodePaymentMethodExcluded] != 1 ||
		result.Plan.Payments.ByKind[bedolagaimport.PaymentGift] != 1 || result.Plan.Payments.ByKind[bedolagaimport.PaymentDirect] != 1 {
		t.Fatalf("payments: written %d, plan %+v", result.Written["payment"], result.Plan.Payments)
	}
	if result.Written["discount"] != 1 || result.Plan.Gifts.Unclaimed != 1 || result.Plan.Gifts.SumMinor != 30000 {
		t.Fatalf("discounts written %d, gifts %+v", result.Written["discount"], result.Plan.Gifts)
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

	if fixture.count(ctx, t, `SELECT count(*) FROM legacy_payments WHERE user_id = $1::uuid`, hana) != 4 ||
		fixture.count(ctx, t, `SELECT count(*) FROM legacy_payments WHERE user_id = $1::uuid AND source = 'bedolaga_gift'`, hana) != 1 ||
		fixture.count(ctx, t, `SELECT count(*) FROM legacy_subscription_periods WHERE user_id = $1::uuid AND ends_at < now()`, hana) != 2 {
		t.Fatal("hana's top-ups, landing purchase, and gift are carried, and her two periods")
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
	if !segment(map[string]any{audience.FilterMinimumSpendMinor: float64(95700)})[hana] {
		t.Fatal("carried payments count as spend")
	}
	if !segment(map[string]any{audience.FilterInactiveForDays: float64(30)})[hana] ||
		segment(map[string]any{audience.FilterInactiveForDays: float64(60)})[hana] {
		t.Fatal("hana last paid 45 days ago, for the gift")
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
	if metric.SpendMinor != 95700 || metric.OrderCount != 4 || metric.TenureDays < 364 {
		t.Fatalf("loyalty reads the carried history: %+v", metric)
	}

	// "A discount at most once in sixty days": her Bedolaga code twenty days
	// ago keeps her out; thirty days lets her in.
	eligible := func(days int) bool {
		t.Helper()
		row, err := queries.CheckPromotionCustomerEligibility(ctx, dbgen.CheckPromotionCustomerEligibilityParams{
			Eligibility: []byte(`{"discountCooldownDays": ` + strconv.Itoa(days) + `}`), UserID: hanaID,
		})
		if err != nil {
			t.Fatalf("eligibility: %v", err)
		}
		return row.Valid && row.Bool
	}
	if eligible(60) || !eligible(14) {
		t.Fatal("the carried discount date decides the cooldown")
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

	// Alice's Bedolaga invite link still works: a new customer arriving with
	// `refalice` in any case is attributed to her, on the web and in the bot.
	alice := fixture.customerOf(ctx, t, 1)
	if fixture.count(ctx, t, `SELECT count(*) FROM referral_code_aliases WHERE code = 'REFALICE' AND user_id = $1::uuid`, alice) != 1 {
		t.Fatal("alice's Bedolaga referral code is an alias of hers")
	}
	newcomer := fixture.harness.customer(ctx, t)
	var newcomerID pgtype.UUID
	if err := newcomerID.Scan(newcomer); err != nil {
		t.Fatal(err)
	}
	state, err := queries.AttributeAccountReferral(ctx, dbgen.AttributeAccountReferralParams{UserID: newcomerID, Code: "REFALICE"})
	if err != nil || !state.OwnerKnown || !state.Inserted {
		t.Fatalf("web attribution by a carried code: %+v %v", state, err)
	}
	if fixture.count(ctx, t, `SELECT count(*) FROM referral_attributions WHERE referred_user_id = $1::uuid AND referrer_user_id = $2::uuid`,
		newcomer, alice) != 1 {
		t.Fatal("the newcomer is attributed to alice")
	}

	// A repeated run carries nothing twice, and verification holds.
	again, err := service.Apply(ctx, fixture.read(ctx, t), fixture.mapping, bedolagaimportpg.ApplyOptions{})
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if again.Written["payment"] != 0 || again.Written["trial"] != 0 || again.Written["period"] != 0 ||
		again.Written["discount"] != 0 || again.Written["referral_code"] != 0 {
		t.Fatalf("second apply wrote history again: %+v", again.Written)
	}
	report, err := service.Verify(ctx, fixture.read(ctx, t), fixture.mapping)
	if err != nil || !report.OK {
		t.Fatalf("verify: %v %+v", err, report)
	}
}
