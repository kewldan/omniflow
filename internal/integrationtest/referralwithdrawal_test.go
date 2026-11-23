//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"

	"github.com/omniflow/omniflow/internal/referralpayout"
)

// seedReferralEarnings gives an inviter a paying referral and a credited
// inviter reward, through the same ledger rows a real reward writes.
func (harness *harness) seedReferralEarnings(ctx context.Context, t *testing.T, inviter string, amount int64) string {
	t.Helper()
	return harness.seedReferralReward(ctx, t, inviter, "inviter", amount)
}

// seedReferralReward is seedReferralEarnings for any reward role the inviter
// can earn — `inviter`, or `commission` on the friend's payment.
func (harness *harness) seedReferralReward(ctx context.Context, t *testing.T, inviter, role string, amount int64) string {
	t.Helper()
	invitee := harness.customer(ctx, t)
	var orderID, transactionID string
	if err := harness.pool.QueryRow(ctx, `INSERT INTO orders (user_id, state, operation, currency, subtotal_minor,
			external_minor, paid_minor, idempotency_key, paid_at)
		VALUES ($1::uuid, 'paid', 'purchase', 'RUB', 30000, 30000, 30000, 'ref-' || $1, now()) RETURNING id::text`,
		invitee).Scan(&orderID); err != nil {
		t.Fatal(err)
	}
	if _, err := harness.pool.Exec(ctx, `INSERT INTO referral_attributions (referred_user_id, referrer_user_id, code, qualified_at)
		VALUES ($1::uuid, $2::uuid, 'CODE', now())`, invitee, inviter); err != nil {
		t.Fatal(err)
	}
	if err := harness.pool.QueryRow(ctx, `INSERT INTO ledger_transactions (type, reference_type, reference_id, idempotency_key)
		VALUES ('referral_reward', 'referral', $1, 'reward-' || $1) RETURNING id::text`, orderID).Scan(&transactionID); err != nil {
		t.Fatal(err)
	}
	if _, err := harness.pool.Exec(ctx, `INSERT INTO ledger_entries (transaction_id, account_type, user_id, currency, amount_minor)
		VALUES ($1::uuid, 'customer_wallet', $2::uuid, 'RUB', $3), ($1::uuid, 'platform_clearing', NULL, 'RUB', -$3::bigint)`,
		transactionID, inviter, amount); err != nil {
		t.Fatal(err)
	}
	if _, err := harness.pool.Exec(ctx, `INSERT INTO referral_rewards (referred_user_id, beneficiary_user_id, role, order_id,
			amount_minor, currency, ledger_transaction_id)
		VALUES ($1::uuid, $2::uuid, $6, $3::uuid, $4, 'RUB', $5::uuid)`,
		invitee, inviter, orderID, amount, transactionID, role); err != nil {
		t.Fatal(err)
	}
	return invitee
}

// A withdrawal holds its amount at once, refuses what the policy forbids,
// is idempotent, and gives the money back when rejected or cancelled; only
// one of two racing requests can win.
func TestReferralWithdrawalLifecycle(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service, err := referralpayout.New(harness.pool, operationsKey, nil, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	actor := harness.operator(ctx, t, "finance@omniflow.test")
	inviter := harness.customer(ctx, t)
	harness.seedReferralEarnings(ctx, t, inviter, 80000)
	// The inviter's own top-up is not withdrawable while only the referral
	// share may leave.
	harness.seedWallet(ctx, t, inviter, "own-topup", 50000)

	if _, err = service.RequestWithdrawal(ctx, inviter, "k0", referralpayout.Input{AmountMinor: 60000, Method: "card", Details: "4111"}); !errors.Is(err, referralpayout.ErrDisabled) {
		t.Fatalf("disabled programme: %v", err)
	}
	if _, err = service.SaveSettings(ctx, referralpayout.Settings{Enabled: true, Currency: "RUB", MinimumMinor: 50000,
		CooldownDays: 30, OnlyReferral: true}, actor); err != nil {
		t.Fatal(err)
	}
	summary, err := service.Summary(ctx, inviter)
	if err != nil || summary.AvailableMinor != 80000 || summary.EarnedMinor != 80000 {
		t.Fatalf("summary = %+v (%v)", summary, err)
	}
	for name, input := range map[string]referralpayout.Input{
		"below minimum": {AmountMinor: 40000, Method: "card", Details: "4111"},
		"too much":      {AmountMinor: 90000, Method: "card", Details: "4111"},
		"bad method":    {AmountMinor: 60000, Method: "cash", Details: "4111"},
	} {
		if _, err := service.RequestWithdrawal(ctx, inviter, "bad-"+name, input); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}

	// Two requests race for the same balance; one wins.
	var wins, losses int
	var mu sync.Mutex
	var wait sync.WaitGroup
	for index := range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := service.RequestWithdrawal(ctx, inviter, "race-"+string(rune('a'+index)),
				referralpayout.Input{AmountMinor: 70000, Method: "sbp", Details: "+79990000000"})
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				wins++
			} else {
				losses++
			}
		}()
	}
	wait.Wait()
	if wins != 1 || losses != 1 {
		t.Fatalf("race: %d wins, %d losses", wins, losses)
	}
	if balance := harness.walletBalance(ctx, t, inviter); balance != 130000-70000 {
		t.Fatalf("balance after hold = %d", balance)
	}
	requests, err := service.List(ctx, "pending", 10)
	if err != nil || len(requests) != 1 {
		t.Fatalf("pending = %+v (%v)", requests, err)
	}
	request := requests[0]
	if request.Risk.Score >= 50 {
		t.Fatalf("an ordinary partner scored %+v", request.Risk)
	}
	details, err := service.RevealDetails(ctx, request.ID, actor)
	if err != nil || details != "+79990000000" {
		t.Fatalf("details = %q (%v)", details, err)
	}
	var plaintext bool
	if err = harness.pool.QueryRow(ctx, `SELECT position('7999' in encode(payout_details_ciphertext, 'escape')) > 0
		FROM referral_withdrawals WHERE id = $1::uuid`, request.ID).Scan(&plaintext); err != nil || plaintext {
		t.Fatalf("payout details are stored in clear (%v)", err)
	}

	if err = service.Reject(ctx, request.ID, "", actor); !errors.Is(err, referralpayout.ErrInvalid) {
		t.Fatalf("a rejection without a reason: %v", err)
	}
	if err = service.Reject(ctx, request.ID, "details do not match", actor); err != nil {
		t.Fatal(err)
	}
	if err = service.Reject(ctx, request.ID, "again", actor); !errors.Is(err, referralpayout.ErrState) {
		t.Fatalf("a second rejection: %v", err)
	}
	if balance := harness.walletBalance(ctx, t, inviter); balance != 130000 {
		t.Fatalf("balance after rejection = %d, want the hold returned", balance)
	}

	// A rejected request does not start the cooldown; the next one is approved and paid.
	approved, err := service.RequestWithdrawal(ctx, inviter, "second", referralpayout.Input{AmountMinor: 50000, Method: "card", Details: "4111 1111"})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := service.RequestWithdrawal(ctx, inviter, "second", referralpayout.Input{AmountMinor: 50000, Method: "card", Details: "4111 1111"})
	if err != nil || replay.ID != approved.ID {
		t.Fatalf("replay = %+v (%v)", replay, err)
	}
	if err = service.MarkPaid(ctx, approved.ID, "", actor); !errors.Is(err, referralpayout.ErrState) {
		t.Fatalf("paid before approval: %v", err)
	}
	if err = service.Approve(ctx, approved.ID, "ok", actor); err != nil {
		t.Fatal(err)
	}
	if err = service.MarkPaid(ctx, approved.ID, "sent", actor); err != nil {
		t.Fatal(err)
	}
	if _, err = service.RequestWithdrawal(ctx, inviter, "third", referralpayout.Input{AmountMinor: 50000, Method: "card", Details: "1"}); !errors.Is(err, referralpayout.ErrCooldown) {
		t.Fatalf("within the cooldown: %v", err)
	}
	if balance := harness.walletBalance(ctx, t, inviter); balance != 80000 {
		t.Fatalf("balance after payout = %d", balance)
	}
	var audits int
	if err = harness.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action LIKE 'panel.referral_withdrawal.%'`).Scan(&audits); err != nil || audits != 4 {
		t.Fatalf("audit rows = %d (%v)", audits, err)
	}

	// A customer cancels their own pending request; nobody else can.
	if _, err = service.SaveSettings(ctx, referralpayout.Settings{Enabled: true, Currency: "RUB", MinimumMinor: 10000,
		CooldownDays: 0, OnlyReferral: false}, actor); err != nil {
		t.Fatal(err)
	}
	pending, err := service.RequestWithdrawal(ctx, inviter, "fourth", referralpayout.Input{AmountMinor: 20000, Method: "crypto", Details: "TON..."})
	if err != nil {
		t.Fatal(err)
	}
	stranger := harness.customer(ctx, t)
	if err = service.Cancel(ctx, stranger, pending.ID); !errors.Is(err, referralpayout.ErrNotFound) {
		t.Fatalf("a stranger's cancel: %v", err)
	}
	if err = service.Cancel(ctx, inviter, pending.ID); err != nil {
		t.Fatal(err)
	}
	if balance := harness.walletBalance(ctx, t, inviter); balance != 80000 {
		t.Fatalf("balance after cancel = %d", balance)
	}
}

// Commission on a friend's payments — a partner's personal rate included — is
// referral income like the invite reward: with only the referral share
// allowed out, both count towards what may be withdrawn, and the customer's
// own top-up still does not.
func TestReferralWithdrawalCountsCommission(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service, err := referralpayout.New(harness.pool, operationsKey, nil, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	actor := harness.operator(ctx, t, "finance@omniflow.test")
	partner := harness.customer(ctx, t)
	harness.seedReferralReward(ctx, t, partner, "inviter", 30000)
	harness.seedReferralReward(ctx, t, partner, "commission", 40000)
	harness.seedWallet(ctx, t, partner, "own-topup", 50000)
	if _, err = service.SaveSettings(ctx, referralpayout.Settings{Enabled: true, Currency: "RUB", MinimumMinor: 50000,
		CooldownDays: 30, OnlyReferral: true}, actor); err != nil {
		t.Fatal(err)
	}

	summary, err := service.Summary(ctx, partner)
	if err != nil || summary.EarnedMinor != 70000 || summary.AvailableMinor != 70000 {
		t.Fatalf("summary = %+v (%v)", summary, err)
	}
	if _, err = service.RequestWithdrawal(ctx, partner, "over", referralpayout.Input{AmountMinor: 70001, Method: "card", Details: "4111"}); !errors.Is(err, referralpayout.ErrInsufficient) {
		t.Fatalf("over the referral share: %v", err)
	}
	request, err := service.RequestWithdrawal(ctx, partner, "all", referralpayout.Input{AmountMinor: 70000, Method: "card", Details: "4111"})
	if err != nil {
		t.Fatal(err)
	}
	// A commission comes from a paying referral, so neither the earned total
	// nor its single source reads as income with nobody paying.
	for _, flag := range request.Risk.Flags {
		if flag == "no_paying_referrals" {
			t.Fatalf("flags = %v", request.Risk.Flags)
		}
	}
}
