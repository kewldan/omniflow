//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/accountcheckout"
	"github.com/omniflow/omniflow/internal/audience"
	"github.com/omniflow/omniflow/internal/bedolagaimport"
	"github.com/omniflow/omniflow/internal/bedolagaimportpg"
	"github.com/omniflow/omniflow/internal/botapp"
	"github.com/omniflow/omniflow/internal/campaigns"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/panelpg"
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
	// Her VPN purchases: a month from the balance, written as a debit; a
	// landing-page purchase through a provider; an older month from the
	// balance. Not VPN spend: the top-up that funded the balance, Telegram
	// Stars bought from it, an operator's free tariff switch, and a purchase
	// that never completed.
	fixture.exec(ctx, t, `INSERT INTO transactions (user_id, type, amount_kopeks, payment_method, is_completed, created_at, completed_at) VALUES
		(30, 'subscription_payment', -22900, 'balance', true, $1, $1),
		(30, 'subscription_payment', 22900, 'yookassa', true, $2, NULL),
		(30, 'subscription_payment', 19900, 'balance', true, $3, NULL),
		(30, 'deposit', 120000, 'yookassa', true, $3, NULL),
		(30, 'stars_payment', 50000, 'balance', true, $2, NULL),
		(30, 'subscription_payment', 0, NULL, true, $2, NULL),
		(30, 'subscription_payment', 99900, 'yookassa', false, $2, NULL)`,
		paidEnd.AddDate(0, -1, 0), paidEnd.AddDate(0, 0, -40), paidEnd.AddDate(0, 0, -60))
	// Gifts: one Hana bought through a provider that its recipient is still
	// to claim, one she paid from the balance (a gift_payment) that was
	// delivered.
	fixture.exec(ctx, t, `INSERT INTO guest_purchases (token, contact_type, contact_value, is_gift, buyer_user_id,
		period_days, amount_kopeks, payment_method, status, paid_at) VALUES
		('g1', 'telegram', '@friend', true, 30, 30, 30000, 'yookassa', 'pending_activation', $1),
		('g2', 'telegram', '@other', true, 30, 30, 30000, 'balance', 'delivered', $1),
		('g3', 'telegram', '@nobody', true, NULL, 30, 30000, 'yookassa', 'pending', NULL)`, paidEnd)
	fixture.exec(ctx, t, `INSERT INTO transactions (user_id, type, amount_kopeks, payment_method, is_completed, created_at)
		VALUES (30, 'gift_payment', 30000, 'balance', true, $1)`, paidEnd)
	// Her last discount: a discount code twenty days ago, after an older offer.
	fixture.exec(ctx, t, `INSERT INTO promocodes (code, type) VALUES ('SALE20', 'discount'), ('BONUS', 'balance')`)
	lastDiscount := now.AddDate(0, 0, -20)
	fixture.exec(ctx, t, `INSERT INTO promocode_uses (promocode_id, user_id, used_at)
		SELECT id, 30, CASE WHEN code = 'SALE20' THEN $1::timestamptz ELSE $2::timestamptz END FROM promocodes
		WHERE code IN ('SALE20', 'BONUS')`, lastDiscount, now.AddDate(0, 0, -1))
	fixture.exec(ctx, t, `INSERT INTO discount_offers (user_id, discount_percent, expires_at, claimed_at)
		VALUES (30, 15, $1, $1)`, now.AddDate(0, -3, 0))

	// The spec's two cases: Ivan topped up 360 ₽ and spent it on Stars; Kate
	// topped up 499 ₽ and bought 90 days with it.
	fixture.exec(ctx, t, `INSERT INTO users (id, telegram_id, username, status, created_at) VALUES
		(31, 5031, 'ivan', 'active', $1), (32, 5032, 'kate', 'active', $1)`, now.AddDate(0, -2, 0))
	fixture.exec(ctx, t, `INSERT INTO transactions (user_id, type, amount_kopeks, payment_method, is_completed, created_at) VALUES
		(31, 'deposit', 36000, 'yookassa', true, $1), (31, 'stars_payment', -36000, 'balance', true, $1),
		(32, 'deposit', 49900, 'yookassa', true, $1), (32, 'subscription_payment', -49900, 'balance', true, $1)`, now.AddDate(0, 0, -10))

	// Advertising campaigns: stars_elite brought Hana and then Ivan; Ivan
	// also registered through a later one, which does not move him. A
	// campaign whose code Omniflow cannot hold is reported and left. An
	// Omniflow campaign already has the code "summer".
	fixture.exec(ctx, t, `INSERT INTO advertising_campaigns (id, name, start_parameter, bonus_type, balance_bonus_kopeks,
		subscription_duration_days, tariff_duration_days, is_active) VALUES
		(1, 'Stars Elite', 'Stars_Elite', 'balance', 5000, NULL, NULL, true),
		(2, 'Summer', 'summer', 'subscription', 0, 7, NULL, false),
		(3, 'Bad code', 'x-y', 'none', 0, NULL, NULL, true),
		(4, 'Tariff', 'tariff_promo', 'tariff', 0, NULL, 14, true)`)
	fixture.exec(ctx, t, `INSERT INTO advertising_campaign_registrations (campaign_id, user_id, bonus_type, created_at) VALUES
		(1, 30, 'balance', $1), (1, 31, 'balance', $2), (2, 31, 'subscription', $3), (3, 32, 'none', $3)`,
		now.AddDate(-1, 0, -5), now.AddDate(0, -2, 0), now.AddDate(0, -1, 0))
	if _, err := fixture.harness.pool.Exec(ctx, `INSERT INTO ad_campaigns (code, name, bonus_kind, bonus_value) VALUES ('summer', 'Summer here', 'days', 3)`); err != nil {
		t.Fatal(err)
	}
	// Stars: Ivan's 360 ₽ became 200 delivered stars; a refunded order is
	// not history.
	fixture.exec(ctx, t, `INSERT INTO stars_orders (user_id, recipient_username, quantity, amount_kopeks, status, idempotency_key, created_at)
		VALUES (31, 'ivan_tg', 200, 36000, 'completed', 'ivan-1', $1), (31, 'ivan_tg', 50, 9000, 'refunded', 'ivan-2', $1)`,
		now.AddDate(0, 0, -10))

	source := fixture.read(ctx, t)
	// Alice's -5000 month, Hana's five purchases, and Kate's 90 days.
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
	if result.Written["payment"] != 7 || result.Plan.Payments.ByKind[bedolagaimport.PaymentGift] != 1 ||
		result.Plan.Payments.ByKind[bedolagaimport.PaymentGiftBalance] != 1 || result.Plan.Payments.ByKind[bedolagaimport.PaymentSubscription] != 5 ||
		result.Plan.Payments.StarsMinor != 86000 {
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

	if fixture.count(ctx, t, `SELECT count(*) FROM legacy_payments WHERE user_id = $1::uuid`, hana) != 5 ||
		fixture.count(ctx, t, `SELECT count(*) FROM legacy_payments WHERE user_id = $1::uuid AND source = 'bedolaga_gift'`, hana) != 1 ||
		fixture.count(ctx, t, `SELECT count(*) FROM legacy_subscription_periods WHERE user_id = $1::uuid AND ends_at < now()`, hana) != 2 {
		t.Fatal("hana's five VPN purchases are carried, and her two periods")
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
	ivan, kate := fixture.customerOf(ctx, t, 31), fixture.customerOf(ctx, t, 32)
	spenders := segment(map[string]any{audience.FilterMinimumSpendMinor: float64(1)})
	if spenders[ivan] || !spenders[kate] {
		t.Fatalf("Stars are not VPN spend: ivan %t, kate %t", spenders[ivan], spenders[kate])
	}
	if fixture.count(ctx, t, `SELECT COALESCE(sum(amount_minor), 0)::int FROM legacy_payments WHERE user_id = $1::uuid`, kate) != 49900 ||
		fixture.count(ctx, t, `SELECT count(*) FROM legacy_payments WHERE user_id = $1::uuid`, ivan) != 0 {
		t.Fatal("kate spent 499 ₽ on VPN, ivan nothing")
	}
	if !segment(map[string]any{audience.FilterMinimumSpendMinor: float64(125700)})[hana] {
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
	if metric.SpendMinor != 125700 || metric.OrderCount != 5 || metric.TenureDays < 364 {
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
	// Alice bought a month today in Bedolaga's fixture; Hana paid today in
	// Omniflow after paying before. Only she was a payer before the window,
	// and Frank's top-up is not a VPN purchase at all.
	if payers.Payers != 2 || payers.Returning != 1 {
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

	// Campaigns by code: stars_elite and tariff_promo are created with their
	// bonuses mapped, summer is matched to Omniflow's own and left as the
	// operator set it, x-y is skipped. Hana and Ivan are attributed to
	// stars_elite on their original dates, with the bonus recorded as given.
	if result.Written["campaign"] != 2 || result.Plan.Campaigns.Codes[bedolagaimport.CodeInvalidCampaignCode] != 1 ||
		result.Plan.Campaigns.Warnings[bedolagaimport.CodeTariffBonusAsDays] != 1 {
		t.Fatalf("campaigns: written %d, report %+v", result.Written["campaign"], result.Plan.Campaigns)
	}
	if fixture.count(ctx, t, `SELECT count(*) FROM ad_campaigns WHERE code = 'stars_elite' AND bonus_kind = 'wallet' AND bonus_value = 5000 AND active`) != 1 ||
		fixture.count(ctx, t, `SELECT count(*) FROM ad_campaigns WHERE code = 'tariff_promo' AND bonus_kind = 'days' AND bonus_value = 14`) != 1 ||
		fixture.count(ctx, t, `SELECT count(*) FROM ad_campaigns WHERE code = 'summer' AND name = 'Summer here' AND bonus_value = 3`) != 1 {
		t.Fatal("campaigns are carried by code, and an existing one is left alone")
	}
	if result.Written["campaign_registration"] != 2 ||
		fixture.count(ctx, t, `SELECT count(*) FROM customer_campaign_attributions ca JOIN ad_campaigns c ON c.id = ca.campaign_id
			WHERE c.code = 'stars_elite' AND ca.user_id = ANY($1::uuid[]) AND ca.bonus_granted_at = ca.attributed_at
			AND ca.attributed_at < now() - interval '50 days'`, []string{hana, ivan}) != 2 {
		t.Fatalf("attributions: %+v", result.Written)
	}
	starsSegment, err := audience.Compile(map[string]any{"adCampaign": "stars_elite", "goods": map[string]any{"kind": "stars", "withinDays": float64(30)}}, now)
	if err != nil {
		t.Fatal(err)
	}
	var buyers int
	if err = fixture.harness.pool.QueryRow(ctx, "SELECT count(*) FROM users u WHERE "+starsSegment.Where, starsSegment.Args...).Scan(&buyers); err != nil || buyers != 1 {
		t.Fatalf("stars buyers from stars_elite: %d %v", buyers, err)
	}
	// Ivan's order, and Alice's from the base fixture; Ivan's refund is not.
	if result.Written["stars_order"] != 2 || result.Plan.Goods.CarriedStars != 300 ||
		fixture.count(ctx, t, `SELECT count(*) FROM legacy_goods_orders WHERE user_id = $1::uuid AND quantity = 200 AND amount_minor = 36000`, ivan) != 1 {
		t.Fatalf("stars orders: %+v %+v", result.Written, result.Plan.Goods)
	}
	sales, err := newOperations(t, fixture.harness).SalesReport(ctx, now.AddDate(0, 0, -30), now.Add(time.Hour), "UTC", "")
	inWindow := int64(fixture.count(ctx, t, `SELECT COALESCE(sum(quantity), 0)::int FROM legacy_goods_orders
		WHERE delivered_at >= $1 AND delivered_at < $2`, now.AddDate(0, 0, -30), now.Add(time.Hour)))
	if err != nil || len(sales.LegacyGoods) != 1 || inWindow < 200 || sales.LegacyGoods[0].Quantity != inWindow {
		t.Fatalf("the sales report shows the old shop apart: %+v %v", sales.LegacyGoods, err)
	}

	// A repeated run carries nothing twice, and verification holds.
	again, err := service.Apply(ctx, fixture.read(ctx, t), fixture.mapping, bedolagaimportpg.ApplyOptions{})
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if again.Written["payment"] != 0 || again.Written["trial"] != 0 || again.Written["period"] != 0 ||
		again.Written["discount"] != 0 || again.Written["referral_code"] != 0 || again.Written["campaign"] != 0 ||
		again.Written["campaign_registration"] != 0 || again.Written["stars_order"] != 0 {
		t.Fatalf("second apply wrote history again: %+v", again.Written)
	}
	report, err := service.Verify(ctx, fixture.read(ctx, t), fixture.mapping)
	if err != nil || !report.OK {
		t.Fatalf("verify: %v %+v", err, report)
	}
}

// Under the bot_blocked policy a Bedolaga `blocked` user arrives active with
// the bot marked unreachable for them, so no delivery pass writes into the
// closed chat and the first broadcast's blocked share is not inflated.
func TestBedolagaBotBlockedPolicy(t *testing.T) {
	fixture := newBedolagaFixture(t)
	ctx := context.Background()
	fixture.mapping.BlockedUsers = bedolagaimport.BlockedBotBlocked
	result, err := fixture.service(t, nil).Apply(ctx, fixture.read(ctx, t), fixture.mapping, bedolagaimportpg.ApplyOptions{})
	if err != nil || len(result.Failures) != 0 {
		t.Fatalf("apply: %v %+v", err, result.Failures)
	}
	dave := fixture.customerOf(ctx, t, 4)
	if fixture.count(ctx, t, `SELECT count(*) FROM users WHERE id = $1::uuid AND status = 'active'`, dave) != 1 ||
		fixture.count(ctx, t, `SELECT count(*) FROM bot_delivery_state WHERE user_id = $1::uuid
			AND status = 'blocked' AND last_error_code = 'imported_from_bedolaga'`, dave) != 1 {
		t.Fatal("dave arrives active, with the bot marked blocked for him")
	}
	if fixture.count(ctx, t, `SELECT count(*) FROM bot_delivery_state WHERE status = 'blocked'`) != 1 {
		t.Fatal("only the blocked user is marked")
	}
	if fixture.count(ctx, t, `SELECT count(*) FROM customer_lifecycle_events WHERE user_id = $1::uuid`, dave) != 0 {
		t.Fatal("nobody was suspended")
	}

	// A Telegram campaign to everybody is estimated without him, and his
	// recipient row says why he was not tried.
	operations := newOperations(t, fixture.harness)
	actor := fixture.harness.operator(ctx, t, "blocked@omniflow.test")
	segment, err := operations.SaveSegment(ctx, panelpg.AudienceSegment{
		Code: "everyone", NameEN: "Everyone", NameRU: "Все", Filters: map[string]any{},
	}, actor)
	if err != nil {
		t.Fatal(err)
	}
	if segment.ChatClosed != 1 {
		t.Fatalf("segment chatClosed = %d, want 1", segment.ChatClosed)
	}
	template, err := operations.SaveTemplate(ctx, panelpg.MessageTemplate{
		Code: "hello", Class: "marketing", SubjectEN: "Hi", SubjectRU: "Привет", BodyEN: "Hi", BodyRU: "Привет", Variables: []string{},
	}, actor)
	if err != nil {
		t.Fatal(err)
	}
	campaign, err := operations.CreateCampaignOn(ctx, "telegram", "Hello", template.ID, segment.ID, actor)
	if err != nil || int64(campaign.Estimated) != segment.Size-1 {
		t.Fatalf("campaign estimate %d of %d: %v", campaign.Estimated, segment.Size, err)
	}
	// After an import the first Telegram send waits for a blocked-bot scan:
	// a scan from before the import does not count, one after it does.
	if _, err = fixture.harness.pool.Exec(ctx, `INSERT INTO blocker_scans (status, checked, blocked, requested_at, finished_at)
		VALUES ('completed', 10, 1, now() - interval '400 days', now() - interval '399 days')`); err != nil {
		t.Fatal(err)
	}
	if _, err = operations.SetCampaignState(ctx, campaign.ID, "running", nil, actor); !errors.Is(err, panelpg.ErrBlockerScanRequired) {
		t.Fatalf("a send before the scan: %v", err)
	}
	if _, err = fixture.harness.pool.Exec(ctx, `INSERT INTO blocker_scans (status, checked, blocked, finished_at)
		VALUES ('completed', 10, 1, now())`); err != nil {
		t.Fatal(err)
	}
	if _, err = operations.SetCampaignState(ctx, campaign.ID, "running", nil, actor); err != nil {
		t.Fatal(err)
	}
	campaigns.New(fixture.harness.pool, slog.New(slog.DiscardHandler), campaigns.Config{}).RunOnce(ctx)
	if fixture.count(ctx, t, `SELECT count(*) FROM campaign_recipients WHERE user_id = $1::uuid
		AND status = 'suppressed' AND suppression_reason = 'delivery_blocked'`, dave) != 1 {
		t.Fatal("the closed chat is suppressed as delivery_blocked, not tried")
	}

	// He writes to the bot again: delivery comes back.
	store, err := botapp.NewPostgresStoreWithPool(fixture.harness.pool)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.RecordReachable(ctx, 5004); err != nil {
		t.Fatal(err)
	}
	if fixture.count(ctx, t, `SELECT count(*) FROM bot_delivery_state WHERE user_id = $1::uuid AND status = 'active'`, dave) != 1 {
		t.Fatal("a /start clears the carried block")
	}
}
