//go:build integration

package integrationtest

import (
	"context"
	"strings"
	"testing"
)

// A support reply the bot could not deliver reaches a web-only customer by
// e-mail, once. A reply that reached Telegram, one the customer already read
// in the cabinet, and one written moments ago are not mailed.
func TestUndeliveredSupportReplyIsMailed(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	customer := harness.emailCustomer(ctx, t, "webonly@example.com", false)

	var ticketID string
	if err := harness.pool.QueryRow(ctx, `INSERT INTO support_tickets (user_id, queue_id, subject)
		SELECT $1::uuid, id, 'Cannot connect' FROM support_queues WHERE is_default AND archived_at IS NULL
		RETURNING id::text`, customer).Scan(&ticketID); err != nil {
		t.Fatalf("seed ticket: %v", err)
	}
	reply := func(body, age string, read bool) int64 {
		t.Helper()
		var id int64
		if err := harness.pool.QueryRow(ctx, `INSERT INTO support_messages (ticket_id, sender, body, created_at, read_at)
			VALUES ($1::uuid, 'operator', $2, now() - $3::interval, CASE WHEN $4 THEN now() END) RETURNING id`,
			ticketID, body, age, read).Scan(&id); err != nil {
			t.Fatalf("seed reply: %v", err)
		}
		return id
	}
	deliver := func(id int64, status string) {
		t.Helper()
		if _, err := harness.pool.Exec(ctx, `INSERT INTO notification_deliveries (user_id, kind, dedupe_key, class, status, error_code)
			VALUES ($1::uuid, 'support', 'support-message:' || $2::bigint::text, 'transactional', $3, 'no_telegram')`,
			customer, id, status); err != nil {
			t.Fatalf("seed delivery: %v", err)
		}
	}
	undelivered := reply("Reinstall the <b>app</b>, please.", "1 hour", false)
	deliver(undelivered, "suppressed")
	deliver(reply("Seen already", "1 hour", true), "suppressed")
	deliver(reply("Telegram got this one", "1 hour", false), "sent")
	deliver(reply("Just written", "1 minute", false), "suppressed")
	reply("The bot has not decided yet", "1 hour", false)

	sender := &recordingSender{}
	queue := newEmailQueue(t, harness, sender, false)
	for range 2 {
		if err := queue.Pass(ctx); err != nil {
			t.Fatalf("mail pass: %v", err)
		}
	}
	sent := sender.messages()
	if len(sent) != 1 {
		t.Fatalf("sent %d messages, want exactly the one undelivered reply: %+v", len(sent), sent)
	}
	message := sent[0]
	if message.To != "webonly@example.com" || !strings.Contains(message.Subject, "Cannot connect") ||
		!strings.Contains(message.Text, "Reinstall the app, please.") ||
		!strings.Contains(message.Text, "https://vpn.example/account/support/"+ticketID) {
		t.Fatalf("support reply e-mail: %+v", message)
	}
	var kind, class string
	if err := harness.pool.QueryRow(ctx, `SELECT kind, class FROM email_outbox WHERE user_id = $1::uuid`, customer).
		Scan(&kind, &class); err != nil || kind != "support_reply" || class != "transactional" {
		t.Fatalf("outbox row: %s %s %v", kind, class, err)
	}

	// A customer who switched transactional mail off gets nothing.
	if _, err := harness.pool.Exec(ctx, `UPDATE contact_channels SET transactional_enabled = false WHERE user_id = $1::uuid`, customer); err != nil {
		t.Fatal(err)
	}
	deliver(reply("Another answer", "1 hour", false), "suppressed")
	if err := queue.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	if len(sender.messages()) != 1 {
		t.Fatal("transactional mail switched off means no support e-mail")
	}
}
