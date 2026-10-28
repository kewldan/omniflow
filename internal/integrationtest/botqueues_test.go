//go:build integration

package integrationtest

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/botapp"
)

// notifier builds the bot's notification pipeline over the flow's stand-ins.
func (flow *botFlow) notifier() *botapp.Notifier {
	logger := slog.New(slog.DiscardHandler)
	return botapp.NewNotifier(logger, botapp.NewSender(flow.client, flow.store, logger),
		flow.store, flow.remnawave, flow.commerce, botapp.CommerceSettings{Currency: "RUB"})
}

// sends counts the messages the bot tried to send to one chat.
func (stub *botTelegram) sends(chatID int64) int {
	stub.mutex.Lock()
	defer stub.mutex.Unlock()
	count := 0
	for _, request := range stub.requests {
		if request.Method == "sendMessage" && request.Fields["chat_id"] == itoa64(chatID) {
			count++
		}
	}
	return count
}

func setDeliveryStatus(ctx context.Context, t *testing.T, harness *harness, customerID, status string) {
	t.Helper()
	if _, err := harness.pool.Exec(ctx, `INSERT INTO bot_delivery_state (user_id, status)
		VALUES ($1::uuid, $2) ON CONFLICT (user_id) DO UPDATE SET status = EXCLUDED.status`,
		customerID, status); err != nil {
		t.Fatalf("set delivery state: %v", err)
	}
}

func deliveryRow(ctx context.Context, t *testing.T, harness *harness, customerID, kind, dedupe string) (string, string) {
	t.Helper()
	var status, code string
	err := harness.pool.QueryRow(ctx, `SELECT status, COALESCE(error_code, '') FROM notification_deliveries
		WHERE user_id = $1::uuid AND kind = $2 AND dedupe_key = $3`, customerID, kind, dedupe).Scan(&status, &code)
	if err != nil {
		return "", ""
	}
	return status, code
}

// TestNewsQueueIsNotHeldByUnreachableCustomers puts an unreachable customer at
// the head of the announcement queue. The batch is ordered by post and
// customer and bounded, and such a customer was read, skipped without a
// record, and read again on every pass for the whole announcement window — so
// enough of them stopped announcements for everybody.
func TestNewsQueueIsNotHeldByUnreachableCustomers(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	flow := newBotFlow(ctx, t, harness)
	postID := harness.newsPost(ctx, t, "queue-head", "published")

	accounts := map[int64]string{}
	for _, account := range []int64{700901, 700902, 700903} {
		accounts[account] = flow.customerOf(ctx, t, account)
	}
	setDeliveryStatus(ctx, t, harness, accounts[700901], "blocked")
	setDeliveryStatus(ctx, t, harness, accounts[700902], "not_started")

	pending, err := flow.store.PendingNewsAnnouncements(ctx, 7*24*time.Hour, 10)
	if err != nil {
		t.Fatalf("read the queue: %v", err)
	}
	if len(pending) != 1 || pending[0].CustomerID != accounts[700903] {
		t.Fatalf("the queue holds %+v, want only the one reachable customer", pending)
	}

	flow.notifier().RunOnce(ctx)
	if status, _ := deliveryRow(ctx, t, harness, accounts[700903], "announcement", postID); status != "sent" {
		t.Fatalf("the reachable customer's announcement is %q, want sent", status)
	}
	if flow.telegram.sends(700901)+flow.telegram.sends(700902) != 0 {
		t.Fatal("an unreachable customer was sent the announcement")
	}

	// A quiet-hours deferral is a delay, not a drop: once its instant passes
	// the announcement is back in the queue.
	if _, err := harness.pool.Exec(ctx, `UPDATE notification_deliveries SET status = 'deferred',
		deferred_until = now() - interval '1 minute' WHERE user_id = $1::uuid AND dedupe_key = $2`,
		accounts[700903], postID); err != nil {
		t.Fatalf("defer the announcement: %v", err)
	}
	if pending, err = flow.store.PendingNewsAnnouncements(ctx, 7*24*time.Hour, 10); err != nil || len(pending) != 1 {
		t.Fatalf("a deferral whose time has come is not pending again: %+v, %v", pending, err)
	}
}

// dunningAttempt records one failed renewal charge that wants a message.
func dunningAttempt(ctx context.Context, t *testing.T, harness *harness, customerID, cycle string, age time.Duration) string {
	t.Helper()
	var id string
	if err := harness.pool.QueryRow(ctx, `INSERT INTO dunning_attempts
		(user_id, cycle_key, attempt, funding, outcome, failure_code, scheduled_for, occurred_at, notify_required)
		VALUES ($1::uuid, $2, 2, 'wallet', 'failed', 'insufficient_funds', now() - $3::interval,
		        now() - $3::interval, true) RETURNING id::text`, customerID, cycle, age).Scan(&id); err != nil {
		t.Fatalf("record a dunning attempt: %v", err)
	}
	return id
}

// TestDunningNoticeStopsAtAFinalRefusal sends a failed-charge notice to a
// customer whose chat refuses it. It used to be retried every fifteen minutes
// for as long as the attempt existed; a refusal Telegram calls final now ends
// it on the first pass, a customer already known to be unreachable is not
// tried at all, and a week-old notice is not sent.
func TestDunningNoticeStopsAtAFinalRefusal(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	flow := newBotFlow(ctx, t, harness)
	refusing, blocked, stale, fine := int64(701001), int64(701002), int64(701003), int64(701004)
	refusingID := flow.customerOf(ctx, t, refusing)
	blockedID := flow.customerOf(ctx, t, blocked)
	staleID := flow.customerOf(ctx, t, stale)
	fineID := flow.customerOf(ctx, t, fine)
	refusedAttempt := dunningAttempt(ctx, t, harness, refusingID, "cycle-refusing", time.Minute)
	dunningAttempt(ctx, t, harness, blockedID, "cycle-blocked", time.Minute)
	dunningAttempt(ctx, t, harness, staleID, "cycle-stale", 8*24*time.Hour)
	fineAttempt := dunningAttempt(ctx, t, harness, fineID, "cycle-fine", time.Minute)
	setDeliveryStatus(ctx, t, harness, blockedID, "blocked")

	// The stand-in refuses only the refusing customer's chat.
	flow.telegram.refuseChat = map[int64]string{refusing: "Forbidden: bot was blocked by the user"}
	notifier := flow.notifier()
	notifier.RunOnce(ctx)
	notifier.RunOnce(ctx)

	if status, code := deliveryRow(ctx, t, harness, refusingID, "payment", "dunning:"+refusedAttempt); status != "suppressed" || code != "bot_blocked" {
		t.Fatalf("the refused notice is %q (%q), want suppressed for bot_blocked", status, code)
	}
	if attempts := flow.telegram.sends(refusing); attempts != 1 {
		t.Fatalf("the refusing chat was tried %d times over two passes, want once", attempts)
	}
	if flow.telegram.sends(blocked) != 0 || flow.telegram.sends(stale) != 0 {
		t.Fatal("a notice went to a blocked chat or was sent a week late")
	}
	if status, _ := deliveryRow(ctx, t, harness, fineID, "payment", "dunning:"+fineAttempt); status != "sent" || flow.telegram.sends(fine) != 1 {
		t.Fatalf("the reachable customer's notice is %q after %d sends, want sent once", status, flow.telegram.sends(fine))
	}
}
