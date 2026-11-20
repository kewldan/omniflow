//go:build integration

package integrationtest

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/omniflow/omniflow/internal/dormancy"
	"github.com/omniflow/omniflow/internal/installsettings"
)

// Quiet customers are marked dormant, but never one with something to lose: a
// live subscription, money on the wallet, or an open ticket. Any activity
// revives a dormant customer, and only an owner who asked for deletion gets
// dormant customers deleted, through the ordinary deletion flow.
func TestDormantCustomersAreMarkedRevivedAndOptionallyDeleted(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	pool := harness.pool

	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	customer := func(lastActive string) string {
		t.Helper()
		var id string
		if err := pool.QueryRow(ctx,
			`INSERT INTO users (locale, created_at, last_activity_at) VALUES ('ru', now() - interval '2 years', now() - $1::interval) RETURNING id::text`,
			lastActive,
		).Scan(&id); err != nil {
			t.Fatalf("create customer: %v", err)
		}
		return id
	}

	quiet := customer("400 days")
	recent := customer("10 days")
	withTicket := customer("400 days")
	exec(`INSERT INTO support_tickets (user_id, queue_id)
		SELECT $1::uuid, id FROM support_queues WHERE is_default AND archived_at IS NULL`, withTicket)

	pass := dormancy.New(pool, installsettings.New(pool, slog.New(slog.NewTextHandler(io.Discard, nil))), slog.New(slog.NewTextHandler(io.Discard, nil)))
	result, err := pass.RunOnce(ctx, dormancy.Settings{Enabled: true, InactiveDays: 365})
	if err != nil {
		t.Fatalf("pass: %v", err)
	}
	if result.MarkedDormant != 1 {
		t.Fatalf("marked %d customers dormant, want only the quiet one", result.MarkedDormant)
	}
	dormantAt := func(id string) bool {
		var dormant bool
		if err := pool.QueryRow(ctx, `SELECT dormant_at IS NOT NULL FROM users WHERE id = $1`, id).Scan(&dormant); err != nil {
			t.Fatalf("read dormancy: %v", err)
		}
		return dormant
	}
	if !dormantAt(quiet) || dormantAt(recent) || dormantAt(withTicket) {
		t.Fatalf("dormancy: quiet=%v recent=%v withTicket=%v", dormantAt(quiet), dormantAt(recent), dormantAt(withTicket))
	}

	// Activity — the same write the bot and the web make — revives at once.
	exec(`UPDATE users SET last_activity_at = now() WHERE id = $1`, quiet)
	if dormantAt(quiet) {
		t.Fatal("activity did not revive the dormant customer")
	}
	var revived int
	if err = pool.QueryRow(ctx,
		`SELECT count(*) FROM customer_lifecycle_events WHERE user_id = $1 AND action = 'revived'`, quiet,
	).Scan(&revived); err != nil || revived != 1 {
		t.Fatalf("revived events = %d (err %v), want 1", revived, err)
	}

	// With deletion on, a customer dormant for long enough is deleted through
	// the ordinary flow; one dormant for less is left alone.
	longGone := customer("900 days")
	exec(`UPDATE users SET dormant_at = now() - interval '200 days' WHERE id = $1`, longGone)
	exec(`UPDATE users SET last_activity_at = now() - interval '900 days' WHERE id = $1`, longGone)
	exec(`UPDATE users SET dormant_at = now() - interval '200 days' WHERE id = $1`, longGone)
	justDormant := customer("900 days")
	if _, err = pass.RunOnce(ctx, dormancy.Settings{Enabled: true, InactiveDays: 365, DeleteAfterDays: 90}); err != nil {
		t.Fatalf("pass with deletion: %v", err)
	}
	status := func(id string) string {
		var value string
		if err := pool.QueryRow(ctx, `SELECT status FROM users WHERE id = $1`, id).Scan(&value); err != nil {
			t.Fatalf("read status: %v", err)
		}
		return value
	}
	if status(longGone) != "deleted" {
		t.Fatalf("long-dormant customer is %s, want deleted", status(longGone))
	}
	if status(justDormant) != "active" || !dormantAt(justDormant) {
		t.Fatalf("a customer dormant only since this pass was deleted or not marked: %s", status(justDormant))
	}
	if status(quiet) != "active" {
		t.Fatal("the revived customer was deleted")
	}
	var requested int
	if err = pool.QueryRow(ctx,
		`SELECT count(*) FROM customer_lifecycle_events WHERE user_id = $1 AND action = 'deletion_requested' AND actor_type = 'system'`, longGone,
	).Scan(&requested); err != nil || requested != 1 {
		t.Fatalf("deletion_requested events = %d (err %v), want 1", requested, err)
	}
}
