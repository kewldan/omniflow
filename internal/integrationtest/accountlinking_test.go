//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/omniflow/omniflow/internal/botapp"
	"github.com/omniflow/omniflow/internal/customer"
	"github.com/omniflow/omniflow/internal/customerauth"
	"github.com/omniflow/omniflow/internal/customerauthpg"
	"github.com/omniflow/omniflow/internal/customerpg"
	"github.com/omniflow/omniflow/internal/panelpg"
)

// How customer accounts are joined and linked, one situation per block.
//
// The operator merge is checked against everything a person owns that the
// merge must not lose, duplicate, or leave on the closed account: the wallet
// in each currency (expiring bonuses included), the trial, both sides of a
// referral, promo limits, Bedolaga history, acquisition records, loyalty,
// restrictions, Remnawave users, sessions, and the Telegram mapping. The
// linking tests are the other half: an identity that belongs to somebody else
// is refused, never moved, and an unlinked one is neither lost for good nor
// handed back to an account it was taken from.

func (harness *harness) exec(ctx context.Context, t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := harness.pool.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func (harness *harness) text(ctx context.Context, t *testing.T, sql string, args ...any) string {
	t.Helper()
	var value string
	if err := harness.pool.QueryRow(ctx, sql, args...).Scan(&value); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return value
}

func (harness *harness) count(ctx context.Context, t *testing.T, sql string, args ...any) int {
	t.Helper()
	var value int
	if err := harness.pool.QueryRow(ctx, sql, args...).Scan(&value); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return value
}

func seedPaidOrder(ctx context.Context, t *testing.T, harness *harness, customerID, key string) string {
	t.Helper()
	return harness.text(ctx, t, `INSERT INTO orders (
			user_id, state, operation, currency, subtotal_minor, external_minor, paid_minor, idempotency_key, paid_at
		) VALUES ($1::uuid, 'paid', 'purchase', 'RUB', 10000, 10000, 10000, $2, now()) RETURNING id::text`,
		customerID, key)
}

func mergeActor(ctx context.Context, t *testing.T, harness *harness, email string) panelpg.Actor {
	t.Helper()
	actor := harness.operator(ctx, t, email)
	actor.Reason = "the same person, signed in with a second provider"
	return actor
}

// The common case, carried end to end: a customer who bought in Telegram and
// later signed in on the web with Google, holding an empty-ish second account.
func TestAMergeCarriesEverythingThePersonOwns(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	operations := newOperations(t, harness)
	clock := time.Now().UTC()
	auth := newCustomerService(t, harness, func() time.Time { return clock })
	actor := mergeActor(ctx, t, harness, "merge-all@example.test")

	// The Telegram account: signed in on the web, so it holds a live session.
	signedIn, err := auth.SignInWithTelegram(ctx, signedWidget(810001, clock), customerauthpg.RequestContext{})
	if err != nil {
		t.Fatalf("telegram sign-in: %v", err)
	}
	source := signedIn.Customer.ID
	target := seedCustomer(ctx, t, harness, "web@example.test")
	harness.exec(ctx, t, `INSERT INTO identities (user_id, provider, provider_subject, verified_at, status, oidc_issuer)
		VALUES ($1::uuid, 'oidc:google', 'g-810001', now(), 'active', 'https://accounts.google.test')`, target)

	// Remnawave: the source was imported from the panel — a mapping with its
	// Telegram ID and a primary subscription that relies on the mapping. The
	// target bought on the web and has a subscription and mapping of its own.
	harness.exec(ctx, t, `INSERT INTO remnawave_users (user_id, remnawave_id, telegram_id) VALUES ($1::uuid, 5001, 810001)`, source)
	seedSubscription(ctx, t, harness, source, 1, "Phone")
	harness.exec(ctx, t, `INSERT INTO remnawave_users (user_id, remnawave_id) VALUES ($1::uuid, 9001)`, target)
	harness.exec(ctx, t, `INSERT INTO subscriptions (user_id, slot, label, remnawave_user_id) VALUES ($1::uuid, 1, 'Laptop', 9001)`, target)

	// Wallets in two currencies; part of the rouble balance is a bonus that
	// expires in a week.
	seedWallet(ctx, t, harness, source, "USD", 2500)
	seedWallet(ctx, t, harness, target, "USD", 1000)
	harness.exec(ctx, t, `WITH credit AS (
			INSERT INTO ledger_transactions (type, reference_type, reference_id, idempotency_key)
			VALUES ('credit', 'seed', 'rub', 'seed-rub-810001') RETURNING id
		) INSERT INTO ledger_entries (transaction_id, account_type, user_id, currency, amount_minor)
		SELECT id, 'customer_wallet', $1::uuid, 'RUB', 7000 FROM credit`, source)
	expiresAt := clock.Add(7 * 24 * time.Hour).Truncate(time.Second)
	harness.exec(ctx, t, `WITH bonus AS (
			INSERT INTO ledger_transactions (type, reference_type, reference_id, idempotency_key)
			VALUES ('credit', 'seed', 'bonus', 'seed-bonus-810001') RETURNING id
		) INSERT INTO ledger_entries (transaction_id, account_type, user_id, currency, amount_minor, expires_at)
		SELECT id, 'customer_wallet', $1::uuid, 'RUB', 3000, $2 FROM bonus`, source, expiresAt)

	// Referrals both ways: an inviter brought the source in and was paid on
	// its first purchase; the source invited a friend of its own.
	inviter := seedCustomer(ctx, t, harness, "inviter@example.test")
	friend := seedCustomer(ctx, t, harness, "friend@example.test")
	harness.exec(ctx, t, `INSERT INTO referral_codes (user_id, code) VALUES ($1::uuid, 'INVITER001'), ($2::uuid, 'SOURCE0001')`, inviter, source)
	sourceOrder := seedPaidOrder(ctx, t, harness, source, "merge-source-order")
	// The trial, taken on the Telegram account.
	planID := harness.planIDOf(ctx, t, harness.catalog(ctx, t, "merge-trial", 10000))
	harness.exec(ctx, t, `INSERT INTO trial_claims (user_id, plan_id, order_id) VALUES ($1::uuid, $2::uuid, $3::uuid)`, source, planID, sourceOrder)
	harness.exec(ctx, t, `INSERT INTO referral_attributions (code, referrer_user_id, referred_user_id, qualified_at, qualifying_order_id)
		VALUES ('INVITER001', $1::uuid, $2::uuid, now(), $3::uuid)`, inviter, source, sourceOrder)
	harness.exec(ctx, t, `INSERT INTO referral_rewards (referred_user_id, beneficiary_user_id, role, order_id, amount_minor, currency, days)
		VALUES ($1::uuid, $2::uuid, 'inviter', $3::uuid, 0, 'RUB', 7)`, source, inviter, sourceOrder)
	harness.exec(ctx, t, `INSERT INTO referral_attributions (code, referrer_user_id, referred_user_id)
		VALUES ('SOURCE0001', $1::uuid, $2::uuid)`, source, friend)

	// A once-per-customer promo code the source already used.
	harness.promotion(ctx, t, "ONCE", "", 1)
	harness.exec(ctx, t, `INSERT INTO promo_redemptions (promo_code_id, promotion_id, user_id, order_id, discount_minor)
		SELECT c.id, c.promotion_id, $1::uuid, $2::uuid, 1000 FROM promo_codes c WHERE c.normalized_code = 'ONCE'`, source, sourceOrder)

	// History carried from Bedolaga, acquisition, loyalty, a restriction,
	// auto-renewal, and the last Telegram username seen.
	harness.exec(ctx, t, `INSERT INTO legacy_goods_orders (user_id, source, source_id, kind, quantity, amount_minor, currency, delivered_at)
		VALUES ($1::uuid, 'bedolaga', 77, 'telegram_stars', 50, 9900, 'RUB', now())`, source)
	harness.exec(ctx, t, `INSERT INTO ad_campaigns (code, name, bonus_kind, bonus_value) VALUES ('spring', 'Spring', 'days', 3)`)
	harness.exec(ctx, t, `INSERT INTO customer_campaign_attributions (user_id, campaign_id, bonus_granted_at)
		SELECT $1::uuid, id, now() FROM ad_campaigns WHERE code = 'spring'`, source)
	programID := harness.text(ctx, t, `INSERT INTO loyalty_programs (version, enabled, metric, currency, window_days, grace_days, published_at)
		VALUES (1, true, 'spend', 'RUB', 365, 30, now()) RETURNING id::text`)
	tierID := harness.text(ctx, t, `INSERT INTO loyalty_tiers (program_id, code, name_en, name_ru, threshold, discount_bps)
		VALUES ($1::uuid, 'gold', 'gold', 'gold', 0, 500) RETURNING id::text`, programID)
	harness.exec(ctx, t, `INSERT INTO loyalty_standings (user_id, program_id, tier_id, evaluated_metric) VALUES ($1::uuid, $2::uuid, $3::uuid, 50000)`,
		source, programID, tierID)
	harness.exec(ctx, t, `INSERT INTO customer_restrictions (user_id, kind, reason) VALUES ($1::uuid, 'purchase', 'chargeback')`, source)
	harness.exec(ctx, t, `INSERT INTO auto_renew_settings (user_id, subscription_id) SELECT $1::uuid, id FROM subscriptions WHERE user_id = $1::uuid`, source)
	harness.exec(ctx, t, `INSERT INTO telegram_usernames (user_id, username) VALUES ($1::uuid, 'merge_person')`, source)

	preview, err := operations.MergePreview(ctx, source, target)
	if err != nil || len(preview.Blockers) != 0 {
		t.Fatalf("preview: %v %v", err, preview.Blockers)
	}
	if _, err := operations.MergeCustomers(ctx, source, target, actor); err != nil {
		t.Fatalf("merge: %v", err)
	}

	t.Run("wallet per currency, expiring part keeps its date", func(t *testing.T) {
		if got := walletBalance(ctx, t, harness, target, "USD"); got != 3500 {
			t.Fatalf("target USD = %d, want 3500", got)
		}
		if got := walletBalance(ctx, t, harness, target, "RUB"); got != 10000 {
			t.Fatalf("target RUB = %d, want 10000", got)
		}
		for _, currency := range []string{"USD", "RUB"} {
			if got := walletBalance(ctx, t, harness, source, currency); got != 0 {
				t.Fatalf("source %s = %d after the merge", currency, got)
			}
		}
		expiring := harness.count(ctx, t, `SELECT COALESCE(sum(amount_minor), 0) FROM ledger_entries
			WHERE user_id = $1::uuid AND currency = 'RUB' AND expires_at = $2`, target, expiresAt)
		if expiring != 3000 {
			t.Fatalf("the target holds %d of the bonus with its end date, want 3000", expiring)
		}
	})

	t.Run("one trial per person", func(t *testing.T) {
		if owner := harness.text(ctx, t, `SELECT user_id::text FROM trial_claims`); owner != target {
			t.Fatalf("the trial claim is on %s, want the survivor", owner)
		}
	})

	t.Run("referrals both ways, no self-referral, no second reward", func(t *testing.T) {
		if got := harness.text(ctx, t, `SELECT referred_user_id::text FROM referral_attributions WHERE referrer_user_id = $1::uuid`, inviter); got != target {
			t.Fatalf("the inviter's attribution names %s, want the survivor", got)
		}
		if got := harness.text(ctx, t, `SELECT referred_user_id::text FROM referral_rewards WHERE role = 'inviter'`); got != target {
			t.Fatalf("the paid reward names %s; a first purchase on the survivor would pay it again", got)
		}
		if got := harness.text(ctx, t, `SELECT referrer_user_id::text FROM referral_attributions WHERE referred_user_id = $1::uuid`, friend); got != target {
			t.Fatalf("the friend's commissions would go to %s, want the survivor", got)
		}
		if got := harness.text(ctx, t, `SELECT user_id::text FROM referral_codes WHERE code = 'SOURCE0001'
			UNION ALL SELECT user_id::text FROM referral_code_aliases WHERE code = 'SOURCE0001'`); got != target {
			t.Fatalf("the source's invite code resolves to %s", got)
		}
		if harness.count(ctx, t, `SELECT count(*) FROM referral_attributions WHERE referrer_user_id = referred_user_id`) != 0 {
			t.Fatal("a customer became their own referrer")
		}
	})

	t.Run("promo limits, history, attribution, loyalty, restrictions", func(t *testing.T) {
		for name, sql := range map[string]string{
			"promo redemption":     `SELECT user_id::text FROM promo_redemptions`,
			"legacy goods order":   `SELECT user_id::text FROM legacy_goods_orders`,
			"campaign attribution": `SELECT user_id::text FROM customer_campaign_attributions`,
			"loyalty standing":     `SELECT user_id::text FROM loyalty_standings`,
			"restriction":          `SELECT user_id::text FROM customer_restrictions`,
			"auto-renewal":         `SELECT user_id::text FROM auto_renew_settings`,
			"telegram username":    `SELECT user_id::text FROM telegram_usernames`,
		} {
			if got := harness.text(ctx, t, sql); got != target {
				t.Errorf("%s is on %s, want the survivor", name, got)
			}
		}
	})

	t.Run("two VPN users stay two subscriptions, neither re-created", func(t *testing.T) {
		got := harness.count(ctx, t, `SELECT count(*) FROM subscriptions WHERE user_id = $1::uuid`, target)
		if got != 2 {
			t.Fatalf("the survivor holds %d subscriptions, want 2", got)
		}
		moved := harness.count(ctx, t, `SELECT COALESCE(remnawave_user_id, 0) FROM subscriptions WHERE user_id = $1::uuid AND label = 'Phone'`, target)
		if moved != 5001 {
			t.Fatalf("the moved subscription carries Remnawave user %d, want 5001; fulfillment would mint a second user", moved)
		}
		if got := harness.count(ctx, t, `SELECT COALESCE(telegram_id, 0) FROM remnawave_users WHERE user_id = $1::uuid`, target); got != 810001 {
			t.Fatalf("the survivor's mapping has Telegram ID %d, want 810001", got)
		}
		if got := harness.count(ctx, t, `SELECT count(*) FROM remnawave_users WHERE user_id = $1::uuid AND telegram_id IS NOT NULL`, source); got != 0 {
			t.Fatal("the closed account's mapping still answers to the Telegram ID")
		}
	})

	t.Run("sessions of the absorbed account end", func(t *testing.T) {
		if reason := harness.text(ctx, t, `SELECT COALESCE(revoked_reason, '') FROM customer_sessions WHERE user_id = $1::uuid`, source); reason != "account_merged" {
			t.Fatalf("the source session's revocation reads %q", reason)
		}
		if _, err := auth.Resolve(ctx, signedIn.Token); err == nil {
			t.Fatal("the absorbed account's cookie still resolves")
		}
	})

	t.Run("the same Telegram account now reaches the survivor", func(t *testing.T) {
		again, err := auth.SignInWithTelegram(ctx, signedWidget(810001, clock), customerauthpg.RequestContext{})
		if err != nil || again.Customer.ID != target {
			t.Fatalf("web sign-in after the merge: %q %v", again.Customer.ID, err)
		}
		store, err := botapp.NewPostgresStore(ctx, harness.url)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		bot, err := store.EnsureCustomer(ctx, 810001, "en")
		if err != nil || bot.ID != target {
			t.Fatalf("bot after the merge: %q %v", bot.ID, err)
		}
	})

	t.Run("the audit trail and both histories record it", func(t *testing.T) {
		if harness.count(ctx, t, `SELECT count(*) FROM audit_events WHERE action = 'customer.merged'`) != 1 {
			t.Fatal("no audit event")
		}
	})
}

// A trial claim on the survivor behind an order that closed unpaid is a claim
// that never happened. It must give way to the one the source actually took,
// or the person could start a trial again.
func TestAMergeKeepsTheTrialThatStands(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	operations := newOperations(t, harness)
	actor := mergeActor(ctx, t, harness, "merge-trial@example.test")
	source := seedCustomer(ctx, t, harness, "trial-source@example.test")
	target := seedCustomer(ctx, t, harness, "trial-target@example.test")
	planID := harness.planIDOf(ctx, t, harness.catalog(ctx, t, "trial-stands", 0))
	taken := seedPaidOrder(ctx, t, harness, source, "taken-trial")
	harness.exec(ctx, t, `INSERT INTO trial_claims (user_id, plan_id, order_id) VALUES ($1::uuid, $2::uuid, $3::uuid)`, source, planID, taken)
	abandoned := harness.text(ctx, t, `INSERT INTO orders (user_id, state, operation, currency, subtotal_minor, external_minor, paid_minor, idempotency_key)
		VALUES ($1::uuid, 'cancelled', 'purchase', 'RUB', 0, 0, 0, 'abandoned-trial') RETURNING id::text`, target)
	harness.exec(ctx, t, `INSERT INTO trial_claims (user_id, plan_id, order_id) VALUES ($1::uuid, $2::uuid, $3::uuid)`, target, planID, abandoned)

	// Both hold invite codes: the source's keeps working as an alias.
	harness.exec(ctx, t, `INSERT INTO referral_codes (user_id, code) VALUES ($1::uuid, 'TRIALSRC01'), ($2::uuid, 'TRIALTGT01')`, source, target)

	if _, err := operations.MergeCustomers(ctx, source, target, actor); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if got := harness.text(ctx, t, `SELECT user_id::text FROM referral_code_aliases WHERE code = 'TRIALSRC01' AND source = 'merge'`); got != target {
		t.Fatalf("the source's code resolves to %s, want the survivor", got)
	}
	if order := harness.text(ctx, t, `SELECT order_id::text FROM trial_claims WHERE user_id = $1::uuid`, target); order != taken {
		t.Fatalf("the survivor's trial claim is behind order %s; the abandoned one would let the person claim again", order)
	}
}

// Every refusal, and the one direction that is allowed: a duplicate folded
// into a suspended account stays suspended with it.
func TestAMergeRefusesWhatWouldGoWrong(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	operations := newOperations(t, harness)
	actor := mergeActor(ctx, t, harness, "merge-refuse-more@example.test")

	blocked := func(t *testing.T, source, target string, want panelpg.MergeBlocker) {
		t.Helper()
		preview, err := operations.MergePreview(ctx, source, target)
		if err != nil {
			t.Fatalf("preview: %v", err)
		}
		found := false
		for _, blocker := range preview.Blockers {
			found = found || blocker == want
		}
		if !found {
			t.Fatalf("blockers %v, want %s", preview.Blockers, want)
		}
		if _, err := operations.MergeCustomers(ctx, source, target, actor); !errors.Is(err, panelpg.ErrValidaton) {
			t.Fatalf("the merge went through: %v", err)
		}
	}

	t.Run("both accounts sign in with different Telegram accounts", func(t *testing.T) {
		source := seedCustomer(ctx, t, harness, "")
		target := seedCustomer(ctx, t, harness, "")
		harness.exec(ctx, t, `INSERT INTO identities (user_id, provider, provider_subject, verified_at)
			VALUES ($1::uuid, 'telegram', '820001', now()), ($2::uuid, 'telegram', '820002', now())`, source, target)
		blocked(t, source, target, panelpg.BlockerIdentityConflict)
	})

	t.Run("a suspended account is not merged away", func(t *testing.T) {
		source := seedCustomer(ctx, t, harness, "")
		target := seedCustomer(ctx, t, harness, "")
		harness.exec(ctx, t, `UPDATE users SET status = 'suspended', suspended_at = now() WHERE id = $1::uuid`, source)
		blocked(t, source, target, panelpg.BlockerSourceSuspended)
	})

	t.Run("an open referral payout", func(t *testing.T) {
		source := seedCustomer(ctx, t, harness, "")
		target := seedCustomer(ctx, t, harness, "")
		hold := harness.text(ctx, t, `INSERT INTO ledger_transactions (type, reference_type, reference_id, idempotency_key)
			VALUES ('debit', 'seed', 'hold', 'hold-'||$1) RETURNING id::text`, source)
		harness.exec(ctx, t, `INSERT INTO referral_withdrawals (user_id, amount_minor, currency, payout_method,
			payout_details_ciphertext, risk_score, hold_ledger_transaction_id, idempotency_key)
			VALUES ($1::uuid, 1000, 'RUB', 'card', '\x00', 0, $2::uuid, 'w1')`, source, hold)
		blocked(t, source, target, panelpg.BlockerWithdrawalOpen)
	})

	t.Run("deleted, merged, and the same account", func(t *testing.T) {
		alive := seedCustomer(ctx, t, harness, "")
		deleted := seedCustomer(ctx, t, harness, "")
		harness.exec(ctx, t, `UPDATE users SET status = 'deleted', deleted_at = now() WHERE id = $1::uuid`, deleted)
		blocked(t, alive, deleted, panelpg.BlockerDeleted)
		blocked(t, alive, alive, panelpg.BlockerSameAccount)

		absorbed := seedCustomer(ctx, t, harness, "")
		survivor := seedCustomer(ctx, t, harness, "")
		if _, err := operations.MergeCustomers(ctx, absorbed, survivor, actor); err != nil {
			t.Fatalf("merge: %v", err)
		}
		blocked(t, alive, absorbed, panelpg.BlockerTargetMerged)
	})

	t.Run("into a suspended account is allowed and stays suspended", func(t *testing.T) {
		source := seedCustomer(ctx, t, harness, "")
		target := seedCustomer(ctx, t, harness, "")
		harness.exec(ctx, t, `UPDATE users SET status = 'suspended', suspended_at = now() WHERE id = $1::uuid`, target)
		if _, err := operations.MergeCustomers(ctx, source, target, actor); err != nil {
			t.Fatalf("merge into a suspended account: %v", err)
		}
		if status := harness.text(ctx, t, `SELECT status FROM users WHERE id = $1::uuid`, target); status != "suspended" {
			t.Fatalf("the survivor is %s", status)
		}
	})
}

// Two operators merging the same pair in opposite directions at once must end
// with one survivor, not two accounts each pointing at the other.
func TestOppositeMergesAtOnceLeaveOneSurvivor(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	operations := newOperations(t, harness)
	actor := mergeActor(ctx, t, harness, "merge-race@example.test")
	first := seedCustomer(ctx, t, harness, "")
	second := seedCustomer(ctx, t, harness, "")
	seedWallet(ctx, t, harness, first, "RUB", 100)
	seedWallet(ctx, t, harness, second, "RUB", 200)

	var wait sync.WaitGroup
	errs := make([]error, 2)
	for index, pair := range [][2]string{{first, second}, {second, first}} {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, errs[index] = operations.MergeCustomers(ctx, pair[0], pair[1], actor)
		}()
	}
	wait.Wait()
	if (errs[0] == nil) == (errs[1] == nil) {
		t.Fatalf("both or neither merge succeeded: %v / %v", errs[0], errs[1])
	}
	if merged := harness.count(ctx, t, `SELECT count(*) FROM users WHERE status = 'merged' AND id IN ($1::uuid, $2::uuid)`, first, second); merged != 1 {
		t.Fatalf("%d accounts are merged, want exactly one", merged)
	}
	total := walletBalance(ctx, t, harness, first, "RUB") + walletBalance(ctx, t, harness, second, "RUB")
	if total != 300 {
		t.Fatalf("the two wallets hold %d together, want 300", total)
	}
}

// A sign-in or a payment in flight on the absorbed account when the merge
// starts: the merge waits for it, and then carries what it wrote — the session
// ends, the money moves — instead of leaving it on the closed account.
func TestAMergeWaitsForWritesInFlightOnTheAbsorbedAccount(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	operations := newOperations(t, harness)
	actor := mergeActor(ctx, t, harness, "merge-inflight@example.test")
	source := seedCustomer(ctx, t, harness, "")
	target := seedCustomer(ctx, t, harness, "")

	tx, err := harness.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `INSERT INTO customer_sessions (user_id, token_hash, csrf_secret, auth_method,
			idle_expires_at, absolute_expires_at)
		VALUES ($1::uuid, decode(repeat('ab', 32), 'hex'), decode(repeat('cd', 32), 'hex'), 'telegram',
			now() + interval '1 hour', now() + interval '1 day')`, source); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `WITH credit AS (
			INSERT INTO ledger_transactions (type, reference_type, reference_id, idempotency_key)
			VALUES ('payment', 'seed', 'late', 'late-payment') RETURNING id
		) INSERT INTO ledger_entries (transaction_id, account_type, user_id, currency, amount_minor)
		SELECT id, 'customer_wallet', $1::uuid, 'RUB', 4200 FROM credit`, source); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := operations.MergeCustomers(ctx, source, target, actor)
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("the merge finished while a write on the account was open: %v", err)
	case <-time.After(500 * time.Millisecond):
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("merge: %v", err)
	}
	if got := walletBalance(ctx, t, harness, target, "RUB"); got != 4200 {
		t.Fatalf("the late payment reached the survivor as %d, want 4200", got)
	}
	if live := harness.count(ctx, t, `SELECT count(*) FROM customer_sessions WHERE user_id = $1::uuid AND revoked_at IS NULL`, source); live != 0 {
		t.Fatal("the session opened during the merge is still live")
	}
}

// Linking from the account panel and the admin panel: somebody else's
// identity is refused and stays where it is.
func TestLinkingAnotherCustomersIdentityIsRefused(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	clock := time.Now().UTC()
	auth := newCustomerService(t, harness, func() time.Time { return clock })
	admin, err := customerpg.New(harness.pool, adminTestKey, 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	owner, err := auth.SignInWithTelegram(ctx, signedWidget(830001, clock), customerauthpg.RequestContext{})
	if err != nil {
		t.Fatal(err)
	}
	other := seedCustomer(ctx, t, harness, "")

	if err := auth.LinkTelegramIdentity(ctx, other, signedWidget(830001, clock), customerauthpg.RequestContext{}); !errors.Is(err, customerauth.ErrIdentityTaken) {
		t.Fatalf("account-panel link of another customer's Telegram: %v", err)
	}
	if _, err := admin.LinkIdentity(ctx, other, "telegram", "830001", clock, nil); !errors.Is(err, customer.ErrIdentityConflict) {
		t.Fatalf("admin link of another customer's Telegram: %v", err)
	}
	if got := harness.text(ctx, t, `SELECT user_id::text FROM identities WHERE provider_subject = '830001'`); got != owner.Customer.ID {
		t.Fatalf("the identity moved to %s", got)
	}

	// The customer unlinked it themselves and did not replace it: still
	// theirs, so neither link takes it.
	harness.exec(ctx, t, `INSERT INTO identities (user_id, provider, provider_subject, verified_at)
		VALUES ($1::uuid, 'oidc:acme', 'acme-830001', now())`, owner.Customer.ID)
	telegramID := harness.text(ctx, t, `SELECT id::text FROM identities WHERE provider_subject = '830001'`)
	if err := auth.UnlinkIdentity(ctx, owner.Customer.ID, telegramID, customerauthpg.RequestContext{}); err != nil {
		t.Fatalf("unlink: %v", err)
	}
	if err := auth.LinkTelegramIdentity(ctx, other, signedWidget(830001, clock), customerauthpg.RequestContext{}); !errors.Is(err, customerauth.ErrIdentityTaken) {
		t.Fatalf("linking a Telegram account its customer only unlinked: %v", err)
	}
}

// The customer unlinked Telegram account A and linked B. A is no longer theirs:
// signing in with A used to try to bring it back beside B, fail on the
// one-per-provider index, and answer 500. It is now a new person's account.
func TestAReplacedTelegramAccountSignsInAsSomebodyNew(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	clock := time.Now().UTC()
	auth := newCustomerService(t, harness, func() time.Time { return clock })

	first, err := auth.SignInWithTelegram(ctx, signedWidget(840001, clock), customerauthpg.RequestContext{})
	if err != nil {
		t.Fatal(err)
	}
	customerID := first.Customer.ID
	harness.exec(ctx, t, `INSERT INTO remnawave_users (user_id, remnawave_id, telegram_id) VALUES ($1::uuid, 6001, 840001)`, customerID)
	harness.exec(ctx, t, `INSERT INTO identities (user_id, provider, provider_subject, verified_at)
		VALUES ($1::uuid, 'oidc:acme', 'acme-840001', now())`, customerID)
	identityA := harness.text(ctx, t, `SELECT id::text FROM identities WHERE provider_subject = '840001'`)
	if err := auth.UnlinkIdentity(ctx, customerID, identityA, customerauthpg.RequestContext{}); err != nil {
		t.Fatalf("unlink A: %v", err)
	}
	if got := harness.count(ctx, t, `SELECT count(*) FROM remnawave_users WHERE telegram_id = 840001`); got != 0 {
		t.Fatal("the mapping still answers to the unlinked Telegram account")
	}
	if err := auth.LinkTelegramIdentity(ctx, customerID, signedWidget(840002, clock), customerauthpg.RequestContext{}); err != nil {
		t.Fatalf("link B: %v", err)
	}
	if got := harness.count(ctx, t, `SELECT COALESCE(telegram_id, 0) FROM remnawave_users WHERE user_id = $1::uuid`, customerID); got != 840002 {
		t.Fatalf("the mapping names Telegram %d, want B", got)
	}

	again, err := auth.SignInWithTelegram(ctx, signedWidget(840001, clock), customerauthpg.RequestContext{})
	if err != nil {
		t.Fatalf("sign-in with A: %v", err)
	}
	if again.Customer.ID == customerID {
		t.Fatal("A signed back into the account that replaced it")
	}
	if got := harness.text(ctx, t, `SELECT user_id::text FROM identities WHERE provider_subject = '840001'`); got != again.Customer.ID {
		t.Fatalf("A's row is on %s, want the new customer", got)
	}
	if got := harness.text(ctx, t, `SELECT provider_subject FROM identities WHERE user_id = $1::uuid AND provider = 'telegram' AND status = 'active'`, customerID); got != "840002" {
		t.Fatalf("the original customer signs in with Telegram %s, want B", got)
	}
}

// An operator unlinks a Telegram account attached to the wrong customer. The
// next widget sign-in or bot tap used to put it straight back.
func TestAnOperatorUnlinkIsNotUndoneByTheNextSignIn(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	clock := time.Now().UTC()
	auth := newCustomerService(t, harness, func() time.Time { return clock })
	admin, err := customerpg.New(harness.pool, adminTestKey, 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	store, err := botapp.NewPostgresStore(ctx, harness.url)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	for _, path := range []string{"web", "bot"} {
		t.Run(path, func(t *testing.T) {
			telegramID := int64(850001)
			if path == "bot" {
				telegramID = 850002
			}
			wrong, err := auth.SignInWithTelegram(ctx, signedWidget(telegramID, clock), customerauthpg.RequestContext{})
			if err != nil {
				t.Fatal(err)
			}
			harness.exec(ctx, t, `INSERT INTO identities (user_id, provider, provider_subject, verified_at)
				VALUES ($1::uuid, 'oidc:acme', 'acme-'||$2::bigint::text, now())`, wrong.Customer.ID, telegramID)
			harness.exec(ctx, t, `INSERT INTO remnawave_users (user_id, remnawave_id, telegram_id) VALUES ($1::uuid, $2, $2)`, wrong.Customer.ID, telegramID)
			identityID := harness.text(ctx, t, `SELECT id::text FROM identities WHERE provider = 'telegram' AND provider_subject = $1::bigint::text`, telegramID)
			if _, err := admin.UnlinkIdentity(ctx, wrong.Customer.ID, identityID); err != nil {
				t.Fatalf("operator unlink: %v", err)
			}

			var landed string
			if path == "web" {
				again, err := auth.SignInWithTelegram(ctx, signedWidget(telegramID, clock), customerauthpg.RequestContext{})
				if err != nil {
					t.Fatalf("sign-in: %v", err)
				}
				landed = again.Customer.ID
			} else {
				bot, err := store.EnsureCustomer(ctx, telegramID, "en")
				if err != nil {
					t.Fatalf("bot: %v", err)
				}
				landed = bot.ID
			}
			if landed == wrong.Customer.ID {
				t.Fatal("the operator's unlink was undone by the next sign-in")
			}
			if got := harness.text(ctx, t, `SELECT user_id::text FROM identities WHERE provider = 'telegram' AND provider_subject = $1::bigint::text`, telegramID); got != landed {
				t.Fatalf("the identity is on %s, want %s", got, landed)
			}
		})
	}
}

// The customer unlinks Telegram on the web but keeps using the bot. Before,
// the bot read only active identity rows, missed, and created an empty new
// customer on every update; now it relinks, as the widget does.
func TestTheBotAfterAWebUnlinkStaysOnTheSameCustomer(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	clock := time.Now().UTC()
	auth := newCustomerService(t, harness, func() time.Time { return clock })
	store, err := botapp.NewPostgresStore(ctx, harness.url)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	first, err := store.EnsureCustomer(ctx, 860001, "en")
	if err != nil {
		t.Fatal(err)
	}
	harness.exec(ctx, t, `INSERT INTO identities (user_id, provider, provider_subject, verified_at)
		VALUES ($1::uuid, 'oidc:acme', 'acme-860001', now())`, first.ID)
	identityID := harness.text(ctx, t, `SELECT id::text FROM identities WHERE provider_subject = '860001'`)
	if err := auth.UnlinkIdentity(ctx, first.ID, identityID, customerauthpg.RequestContext{}); err != nil {
		t.Fatalf("unlink: %v", err)
	}
	before := harness.count(ctx, t, `SELECT count(*) FROM users`)
	for range 3 {
		again, err := store.EnsureCustomer(ctx, 860001, "en")
		if err != nil || again.ID != first.ID {
			t.Fatalf("bot tap after the unlink: %q %v, want %s", again.ID, err, first.ID)
		}
	}
	if after := harness.count(ctx, t, `SELECT count(*) FROM users`); after != before {
		t.Fatalf("three bot taps created %d customers", after-before)
	}
}

// E-mail sign-in trusts only an address its customer proved. An address
// somebody typed in and never confirmed — a typo, or somebody else's address
// on purpose — used to sign the mailbox owner into that customer's account.
func TestEmailSignInTrustsOnlyAProvenAddress(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	contacts, err := customerpg.New(harness.pool, adminTestKey, 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	mail := &capturingMailer{}
	service, err := customerauthpg.New(harness.pool, adminTestKey, customerauthpg.Options{
		PublicURL:   "https://vpn.test",
		EmailSignIn: &customerauthpg.EmailSignInOptions{Contacts: contacts, Mailer: mail},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := customerauthpg.RequestContext{AcceptLanguage: "en"}

	typo := harness.customer(ctx, t)
	if _, err := contacts.SetContact(ctx, typo, "email", "someone@example.com", nil, true, false); err != nil {
		t.Fatal(err)
	}
	if err := service.RequestEmailSignIn(ctx, "someone@example.com", "en", request); err != nil {
		t.Fatal(err)
	}
	owner, err := service.CompleteMagicLink(ctx, mail.lastToken(t), request)
	if err != nil {
		t.Fatalf("the mailbox owner's sign-in: %v", err)
	}
	if owner.Customer.ID == typo {
		t.Fatal("the mailbox owner signed in to the account that only typed the address")
	}
	again := func() string {
		if err := service.RequestEmailSignIn(ctx, "someone@example.com", "en", request); err != nil {
			t.Fatal(err)
		}
		next, err := service.CompleteMagicLink(ctx, mail.lastToken(t), request)
		if err != nil {
			t.Fatal(err)
		}
		return next.Customer.ID
	}
	if got := again(); got != owner.Customer.ID {
		t.Fatalf("the next sign-in landed on %s; each sign-in made another customer", got)
	}

	// A removed address: the next sign-in creates one customer, and the one
	// after that lands on it rather than on yet another.
	verified := time.Now()
	removed := harness.customer(ctx, t)
	if _, err := contacts.SetContact(ctx, removed, "email", "gone@example.com", &verified, true, false); err != nil {
		t.Fatal(err)
	}
	harness.exec(ctx, t, `UPDATE contact_channels SET revoked_at = now() WHERE user_id = $1::uuid`, removed)
	if err := service.RequestEmailSignIn(ctx, "gone@example.com", "en", request); err != nil {
		t.Fatal(err)
	}
	firstNew, err := service.CompleteMagicLink(ctx, mail.lastToken(t), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.RequestEmailSignIn(ctx, "gone@example.com", "en", request); err != nil {
		t.Fatal(err)
	}
	secondNew, err := service.CompleteMagicLink(ctx, mail.lastToken(t), request)
	if err != nil || secondNew.Customer.ID != firstNew.Customer.ID {
		t.Fatalf("second sign-in with a removed address: %q %v, want %q", secondNew.Customer.ID, err, firstNew.Customer.ID)
	}
}

// OIDC after an unlink: the customer's own unlinked account relinks on the
// next sign-in, as Telegram does, instead of failing for good; one that
// belongs to another customer is refused at link time, not moved; and an
// operator-unlinked one is free for whoever links it next.
func TestOIDCAfterAnUnlink(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	issuer := newFakeIssuer(t, "omniflow-account")
	service, err := customerauthpg.New(harness.pool, adminTestKey, customerauthpg.Options{
		PublicURL: "https://vpn.test", HTTPClient: trustingClient(issuer),
	})
	if err != nil {
		t.Fatal(err)
	}
	octx := oidc.ClientContext(ctx, trustingClient(issuer))
	if _, err := service.SaveOIDCProvider(octx, customerauthpg.SaveOIDCProviderInput{
		Slug: "corp", DisplayName: "Corp", Issuer: issuer.server.URL, DiscoveryURL: issuer.server.URL,
		ClientID: "omniflow-account", ClientSecret: "client-secret", Enabled: true,
		RequireVerifiedEmail: true, AllowAutoProvision: true,
	}); err != nil {
		t.Fatal(err)
	}
	admin, err := customerpg.New(harness.pool, adminTestKey, 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	const redirect = "https://vpn.test/v1/account/auth/oidc/corp/callback"
	signIn := func(subject, nonce string) (customerauthpg.SignInResult, error) {
		return service.CompleteOIDCSignIn(octx, "corp", subject+"|"+nonce, "verifier", nonce, redirect, customerauthpg.RequestContext{})
	}

	first, err := signIn("alice", "n1")
	if err != nil {
		t.Fatal(err)
	}
	alice := first.Customer.ID
	harness.exec(ctx, t, `INSERT INTO identities (user_id, provider, provider_subject, verified_at)
		VALUES ($1::uuid, 'telegram', '870001', now())`, alice)
	identityID := harness.text(ctx, t, `SELECT id::text FROM identities WHERE provider_subject = 'alice'`)
	if err := service.UnlinkIdentity(ctx, alice, identityID, customerauthpg.RequestContext{}); err != nil {
		t.Fatal(err)
	}
	again, err := signIn("alice", "n2")
	if err != nil || again.Customer.ID != alice {
		t.Fatalf("sign-in after the customer's own unlink: %q %v, want %s", again.Customer.ID, err, alice)
	}

	bob := seedCustomer(ctx, t, harness, "")
	if err := service.LinkOIDCIdentity(octx, bob, "corp", "alice|n3", "verifier", "n3", redirect, customerauthpg.RequestContext{}); !errors.Is(err, customerauth.ErrIdentityTaken) {
		t.Fatalf("linking another customer's account: %v", err)
	}

	if _, err := admin.UnlinkIdentity(ctx, alice, identityID); err != nil {
		t.Fatalf("operator unlink: %v", err)
	}
	if err := service.LinkOIDCIdentity(octx, bob, "corp", "alice|n4", "verifier", "n4", redirect, customerauthpg.RequestContext{}); err != nil {
		t.Fatalf("linking an operator-unlinked account: %v", err)
	}
	if got := harness.text(ctx, t, `SELECT user_id::text FROM identities WHERE provider_subject = 'alice'`); got != bob {
		t.Fatalf("the account is on %s, want the customer who linked it", got)
	}
}

// Two tabs removing the customer's two sign-in methods at once must not both
// pass the last-method guard.
func TestUnlinkingTwoMethodsAtOnceLeavesOne(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	clock := time.Now().UTC()
	auth := newCustomerService(t, harness, func() time.Time { return clock })

	for round := range 5 {
		telegramID := int64(880000 + round)
		first, err := auth.SignInWithTelegram(ctx, signedWidget(telegramID, clock), customerauthpg.RequestContext{})
		if err != nil {
			t.Fatal(err)
		}
		harness.exec(ctx, t, `INSERT INTO identities (user_id, provider, provider_subject, verified_at)
			VALUES ($1::uuid, 'oidc:acme', 'acme-'||$2::bigint::text, now())`, first.Customer.ID, telegramID)
		methods, err := auth.ListSignInMethods(ctx, first.Customer.ID)
		if err != nil || len(methods) != 2 {
			t.Fatalf("methods: %v %v", methods, err)
		}
		var wait sync.WaitGroup
		for _, method := range methods {
			wait.Add(1)
			go func() {
				defer wait.Done()
				_ = auth.UnlinkIdentity(ctx, first.Customer.ID, method.ID, customerauthpg.RequestContext{})
			}()
		}
		wait.Wait()
		if left := harness.count(ctx, t, `SELECT count(*) FROM identities WHERE user_id = $1::uuid AND status = 'active'`, first.Customer.ID); left != 1 {
			t.Fatalf("round %d: %d sign-in methods left, want 1", round, left)
		}
	}
}

// Telegram ID reuse. A deleted account is not revived by its Telegram account
// and no second account is created beside it; an anonymised one has let the
// account go, and the next sign-in is somebody new.
func TestTelegramReuseAfterDeletion(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	clock := time.Now().UTC()
	auth := newCustomerService(t, harness, func() time.Time { return clock })

	first, err := auth.SignInWithTelegram(ctx, signedWidget(890001, clock), customerauthpg.RequestContext{})
	if err != nil {
		t.Fatal(err)
	}
	harness.exec(ctx, t, `UPDATE users SET status = 'deleted', deleted_at = now() WHERE id = $1::uuid`, first.Customer.ID)
	before := harness.count(ctx, t, `SELECT count(*) FROM users`)
	if _, err := auth.SignInWithTelegram(ctx, signedWidget(890001, clock), customerauthpg.RequestContext{}); !errors.Is(err, customerauthpg.ErrAccountInactive) {
		t.Fatalf("sign-in to a deleted account: %v", err)
	}
	if after := harness.count(ctx, t, `SELECT count(*) FROM users`); after != before {
		t.Fatal("a sign-in to a deleted account created another one")
	}

	harness.exec(ctx, t, `UPDATE identities SET provider_subject = 'anonymized:'||id::text, status = 'revoked', revoked_at = now()
		WHERE user_id = $1::uuid`, first.Customer.ID)
	fresh, err := auth.SignInWithTelegram(ctx, signedWidget(890001, clock), customerauthpg.RequestContext{})
	if err != nil || fresh.Customer.ID == first.Customer.ID {
		t.Fatalf("sign-in after anonymisation: %q %v", fresh.Customer.ID, err)
	}
}
