//go:build integration

package integrationtest

import (
	"context"
	"strings"
	"testing"

	"github.com/omniflow/omniflow/internal/operator"
)

// TestEveryOperatorKindIsAcceptedByTheSchema ties the Go list of operator
// topics to the CHECK constraints that guard them. The two drifted once: the
// notice-preview topic was a Go kind the table refused, so it never bound.
// Every kind must be insertable as a topic, and every kind that travels
// through operator_notifications must be insertable as a notice.
func TestEveryOperatorKindIsAcceptedByTheSchema(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	// Previews have queues of their own and never pass through
	// operator_notifications; every other kind does.
	ownQueue := map[string]bool{operator.KindCampaignTest: true, "notice_test": true}
	for index, kind := range operator.Kinds {
		if _, err := harness.pool.Exec(ctx, `INSERT INTO operator_topics (chat_id, kind, topic_id, status)
			VALUES ($1, $2, $3, 'bound')`, int64(-100), kind, int64(index+1)); err != nil {
			t.Errorf("topic kind %q refused: %v", kind, err)
		}
		if ownQueue[kind] {
			continue
		}
		if _, err := harness.pool.Exec(ctx, `INSERT INTO operator_notifications (kind, dedupe_key)
			VALUES ($1, 'schema-check')`, kind); err != nil {
			t.Errorf("notification kind %q refused: %v", kind, err)
		}
	}
}

// TestANewTicketQueuesOneOperatorNotice covers the trigger that tells operators
// about a ticket whichever surface opened it, and that the notice carries no
// customer text.
func TestANewTicketQueuesOneOperatorNotice(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	customerID := harness.customer(ctx, t)
	var ticketID string
	if err := harness.pool.QueryRow(ctx, `INSERT INTO support_tickets (user_id, subject, queue_id)
		SELECT $1::uuid, 'my card was charged twice', q.id FROM support_queues q WHERE q.is_default
		RETURNING id::text`, customerID).Scan(&ticketID); err != nil {
		t.Fatalf("open ticket: %v", err)
	}
	var payload string
	if err := harness.pool.QueryRow(ctx, `SELECT payload::text FROM operator_notifications
		WHERE kind = 'support' AND dedupe_key = 'ticket:' || $1`, ticketID).Scan(&payload); err != nil {
		t.Fatalf("read support notice: %v", err)
	}
	if want := `"ticketId": "` + ticketID + `"`; !strings.Contains(payload, want) {
		t.Fatalf("notice payload %s lacks %s", payload, want)
	}
	if strings.Contains(payload, "charged") {
		t.Fatalf("notice payload carries customer text: %s", payload)
	}
}

// A customer answering in an open ticket notifies operators too, once per
// message, without the text; the ticket's opening message does not, because
// the new-ticket notice already did, and operator messages never do.
func TestACustomerReplyQueuesAnOperatorNotice(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	customerID := harness.customer(ctx, t)
	var ticketID string
	if err := harness.pool.QueryRow(ctx, `INSERT INTO support_tickets (user_id, subject, queue_id)
		SELECT $1::uuid, 'vpn is slow', q.id FROM support_queues q WHERE q.is_default
		RETURNING id::text`, customerID).Scan(&ticketID); err != nil {
		t.Fatalf("open ticket: %v", err)
	}
	for _, message := range []struct{ sender, body string }{
		{"customer", "opening message"}, {"operator", "try the Netherlands"}, {"customer", "still slow, secret details"},
	} {
		if _, err := harness.pool.Exec(ctx, `INSERT INTO support_messages (ticket_id, sender, body) VALUES ($1::uuid, $2, $3)`,
			ticketID, message.sender, message.body); err != nil {
			t.Fatalf("insert %s message: %v", message.sender, err)
		}
	}
	var replies int
	var payload string
	if err := harness.pool.QueryRow(ctx, `SELECT count(*), max(payload::text) FROM operator_notifications
		WHERE kind = 'support' AND dedupe_key LIKE 'reply:%'`).Scan(&replies, &payload); err != nil {
		t.Fatal(err)
	}
	if replies != 1 || !strings.Contains(payload, ticketID) || !strings.Contains(payload, "customer_reply") {
		t.Fatalf("reply notices = %d (%s), want one for the customer's answer", replies, payload)
	}
	if strings.Contains(payload, "secret") {
		t.Fatalf("notice carries customer text: %s", payload)
	}
}
