//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"testing"

	"github.com/omniflow/omniflow/internal/panelpg"
)

// An operator bans a mixed list of customer and Telegram IDs with a template
// message, sees which were not found, and suspends bot-blockers without access.
func TestModerationBansAndBlockers(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service, err := panelpg.New(harness.pool, operationsKey, panelpg.Options{})
	if err != nil {
		t.Fatal(err)
	}
	actor := harness.operator(ctx, t, "moderator@example.test")
	first := harness.customer(ctx, t)
	second := harness.customer(ctx, t)
	if _, err = harness.pool.Exec(ctx, `INSERT INTO remnawave_users (user_id, remnawave_id, telegram_id, observed_state) VALUES ($1::uuid, 9901, 777001, '{}')`, second); err != nil {
		t.Fatal(err)
	}
	if _, err = service.BanCustomers(ctx, panelpg.BanInput{IDs: []string{first}}, actor); !errors.Is(err, panelpg.ErrValidaton) {
		t.Fatalf("a ban without a reason: %v", err)
	}
	results, err := service.BanCustomers(ctx, panelpg.BanInput{
		IDs: []string{first, "777001", "123456"}, Reason: "torrent abuse", Template: "torrent",
	}, actor)
	if err != nil || len(results) != 3 || results[0].Outcome != "suspended" || results[1].CustomerID != second || results[2].Outcome != "not_found" {
		t.Fatalf("results = %+v, %v", results, err)
	}
	var messages int
	if err = harness.pool.QueryRow(ctx, `SELECT count(*) FROM customer_direct_messages WHERE user_id IN ($1::uuid, $2::uuid)`, first, second).Scan(&messages); err != nil || messages != 2 {
		t.Fatalf("ban messages = %d, %v", messages, err)
	}

	blocker := harness.customer(ctx, t)
	if _, err = harness.pool.Exec(ctx, `INSERT INTO bot_delivery_state (user_id, status) VALUES ($1::uuid, 'blocked')`, blocker); err != nil {
		t.Fatal(err)
	}
	if state, err := service.BlockerState(ctx); err != nil || state.Blocked != 1 {
		t.Fatalf("blocker state = %+v, %v", state, err)
	}
	if err = service.RequestBlockerScan(ctx, actor); err != nil {
		t.Fatal(err)
	}
	if err = service.RequestBlockerScan(ctx, actor); !errors.Is(err, panelpg.ErrConflict) {
		t.Fatalf("a second pending scan: %v", err)
	}
	if cleaned, err := service.CleanBlockers(ctx, actor); err != nil || cleaned != 1 {
		t.Fatalf("cleaned = %d, %v", cleaned, err)
	}
}
