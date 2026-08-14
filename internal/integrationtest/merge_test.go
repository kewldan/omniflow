//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/panelpg"
)

// Merging two customer accounts against a real database.
//
// Forty-six tables reference `users`, and the properties worth asserting are the
// ones a Go test cannot see: that the subscription slot is renumbered rather
// than colliding with the unique index, that the wallet moves by addition rather
// than by rewriting an append-only table, and that a second attempt moves
// nothing.

func TestAMergeMovesEverythingTransferable(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service := newOperations(t, harness)
	actor := harness.operator(ctx, t, "merge@example.test")
	actor.Reason = "duplicate account, customer signed in with a second provider"

	source := seedCustomer(ctx, t, harness, "source@example.test")
	target := seedCustomer(ctx, t, harness, "target@example.test")

	// The source holds a subscription in slot 1, and so does the target — which
	// is the collision the slot renumbering exists for.
	seedSubscription(ctx, t, harness, source, 1, "Phone")
	seedSubscription(ctx, t, harness, target, 1, "Laptop")
	seedWallet(ctx, t, harness, source, "USD", 2500)
	seedWallet(ctx, t, harness, target, "USD", 1000)

	preview, err := service.MergePreview(ctx, source, target)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if len(preview.Blockers) != 0 {
		t.Fatalf("the merge is blocked by %v", preview.Blockers)
	}
	if preview.Source.ActiveSubscriptions != 1 || len(preview.Source.Wallet) != 1 {
		t.Fatalf("the preview reports %+v", preview.Source)
	}
	if preview.Source.Wallet[0].BalanceMinor != 2500 {
		t.Fatalf("the source balance reads %d", preview.Source.Wallet[0].BalanceMinor)
	}

	if _, err := service.MergeCustomers(ctx, source, target, actor); err != nil {
		t.Fatalf("merge: %v", err)
	}

	// Both subscriptions are now on the target, in different slots.
	rows, err := harness.pool.Query(ctx,
		`SELECT slot, label FROM subscriptions WHERE user_id = $1::uuid ORDER BY slot`, target)
	if err != nil {
		t.Fatalf("read subscriptions: %v", err)
	}
	defer rows.Close()
	slots := map[int32]string{}
	for rows.Next() {
		var slot int32
		var label string
		if err := rows.Scan(&slot, &label); err != nil {
			t.Fatalf("scan: %v", err)
		}
		slots[slot] = label
	}
	if len(slots) != 2 {
		t.Fatalf("the target holds %d subscriptions, want 2: %v", len(slots), slots)
	}
	if slots[1] != "Laptop" || slots[2] != "Phone" {
		t.Fatalf("slots are %v; the moved subscription should have been renumbered", slots)
	}

	// The wallet moved by addition. The source's own entries are still there —
	// the ledger is append-only — and its balance is now zero because a
	// compensating entry was added, not because history was rewritten.
	if balance := walletBalance(ctx, t, harness, source, "USD"); balance != 0 {
		t.Fatalf("the source balance is %d after the merge", balance)
	}
	if balance := walletBalance(ctx, t, harness, target, "USD"); balance != 3500 {
		t.Fatalf("the target balance is %d, want 3500", balance)
	}
	var sourceEntries int
	if err := harness.pool.QueryRow(ctx,
		`SELECT count(*) FROM ledger_entries WHERE user_id = $1::uuid`, source,
	).Scan(&sourceEntries); err != nil {
		t.Fatalf("count entries: %v", err)
	}
	if sourceEntries < 2 {
		t.Fatalf("the source has %d ledger entries; its history was rewritten rather "+
			"than compensated", sourceEntries)
	}

	// The source is closed and points at the target.
	var status, mergedInto string
	if err := harness.pool.QueryRow(ctx,
		`SELECT status, coalesce(merged_into::text, '') FROM users WHERE id = $1::uuid`, source,
	).Scan(&status, &mergedInto); err != nil {
		t.Fatalf("read source: %v", err)
	}
	if status != "merged" || mergedInto != target {
		t.Fatalf("the source is %s pointing at %q", status, mergedInto)
	}

	// Both histories carry the event, so support reads it from either side.
	var events int
	if err := harness.pool.QueryRow(ctx,
		`SELECT count(*) FROM customer_lifecycle_events
		 WHERE action IN ('merged_away', 'merged_in')`,
	).Scan(&events); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if events != 2 {
		t.Fatalf("%d lifecycle events, want one on each account", events)
	}
}

// The whole operation is idempotent, because the form somebody resubmits is the
// one that would otherwise move a balance twice.
func TestMergingTwiceMovesNothingAgain(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service := newOperations(t, harness)
	actor := harness.operator(ctx, t, "merge-twice@example.test")
	actor.Reason = "duplicate"

	source := seedCustomer(ctx, t, harness, "twice-source@example.test")
	target := seedCustomer(ctx, t, harness, "twice-target@example.test")
	seedWallet(ctx, t, harness, source, "USD", 5000)

	if _, err := service.MergeCustomers(ctx, source, target, actor); err != nil {
		t.Fatalf("first merge: %v", err)
	}
	again, err := service.MergeCustomers(ctx, source, target, actor)
	if err != nil {
		t.Fatalf("second merge: %v", err)
	}
	if !again.AlreadyMerged {
		t.Fatal("the second merge did not report that it had already happened")
	}
	if balance := walletBalance(ctx, t, harness, target, "USD"); balance != 5000 {
		t.Fatalf("the target balance is %d after two merges, want 5000", balance)
	}
}

func TestAMergeIsRefusedWhenItWouldBeWrong(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service := newOperations(t, harness)
	actor := harness.operator(ctx, t, "merge-refuse@example.test")
	actor.Reason = "testing"

	first := seedCustomer(ctx, t, harness, "refuse-a@example.test")
	second := seedCustomer(ctx, t, harness, "refuse-b@example.test")

	// Into itself.
	preview, err := service.MergePreview(ctx, first, first)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if len(preview.Blockers) == 0 || preview.Blockers[0] != panelpg.BlockerSameAccount {
		t.Fatalf("merging an account with itself reported %v", preview.Blockers)
	}
	if _, err := service.MergeCustomers(ctx, first, first, actor); !errors.Is(
		err, panelpg.ErrValidaton,
	) {
		t.Fatalf("merging an account with itself was accepted: %v", err)
	}

	// One referred the other. Merging would make a customer their own referrer,
	// and somebody was paid a reward for that signup.
	seedReferral(ctx, t, harness, first, second)
	preview, err = service.MergePreview(ctx, second, first)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	found := false
	for _, blocker := range preview.Blockers {
		if blocker == panelpg.BlockerReferralBetween {
			found = true
		}
	}
	if !found {
		t.Fatalf("a referral between the accounts did not block the merge: %v", preview.Blockers)
	}

	// And a merge with no reason, which is the first question anybody asks
	// about one afterwards.
	blank := actor
	blank.Reason = ""
	third := seedCustomer(ctx, t, harness, "refuse-c@example.test")
	if _, err := service.MergeCustomers(ctx, third, first, blank); !errors.Is(
		err, panelpg.ErrValidaton,
	) {
		t.Fatalf("a reasonless merge was accepted: %v", err)
	}
}

func seedSubscription(
	ctx context.Context, t *testing.T, harness *harness, customerID string, slot int, label string,
) {
	t.Helper()
	if _, err := harness.pool.Exec(ctx,
		`INSERT INTO subscriptions (user_id, slot, label) VALUES ($1, $2, $3)`,
		customerID, slot, label,
	); err != nil {
		t.Fatalf("seed subscription: %v", err)
	}
}

func seedWallet(
	ctx context.Context, t *testing.T, harness *harness,
	customerID, currency string, amount int64,
) {
	t.Helper()
	key := "seed-" + customerID + "-" + currency + "-" + strings.ReplaceAll(
		time.Now().String(), " ", "_")
	if _, err := harness.pool.Exec(ctx, `
		WITH transaction AS (
			INSERT INTO ledger_transactions (type, reference_type, reference_id, idempotency_key)
			VALUES ('credit', 'seed', $1, $2) RETURNING id
		)
		INSERT INTO ledger_entries (transaction_id, account_type, user_id, currency, amount_minor)
		SELECT id, 'customer_wallet', $1::uuid, $3, $4 FROM transaction`,
		customerID, key, currency, amount,
	); err != nil {
		t.Fatalf("seed wallet: %v", err)
	}
}

func walletBalance(
	ctx context.Context, t *testing.T, harness *harness, customerID, currency string,
) int64 {
	t.Helper()
	var balance int64
	if err := harness.pool.QueryRow(ctx, `
		SELECT COALESCE(sum(amount_minor), 0) FROM ledger_entries
		WHERE account_type = 'customer_wallet' AND user_id = $1::uuid AND currency = $2`,
		customerID, currency,
	).Scan(&balance); err != nil {
		t.Fatalf("read balance: %v", err)
	}
	return balance
}

func seedReferral(
	ctx context.Context, t *testing.T, harness *harness, referrer, referred string,
) {
	t.Helper()
	if _, err := harness.pool.Exec(ctx, `
		WITH code AS (
			INSERT INTO referral_codes (user_id, code) VALUES ($1, 'SEEDCODE01') RETURNING code
		)
		INSERT INTO referral_attributions (code, referrer_user_id, referred_user_id)
		SELECT code, $1, $2 FROM code`,
		referrer, referred,
	); err != nil {
		t.Fatalf("seed referral: %v", err)
	}
}
