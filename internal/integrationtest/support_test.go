//go:build integration

package integrationtest

import (
	"context"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/panelpg"
)

// ticket inserts one open ticket in the default queue, the way the bot does.
func (harness *harness) ticket(ctx context.Context, t *testing.T, customerID, subject string) string {
	t.Helper()
	var id string
	if err := harness.pool.QueryRow(ctx,
		`INSERT INTO support_tickets (user_id, subject, queue_id)
		 VALUES ($1::uuid, $2,
		         (SELECT id FROM support_queues WHERE is_default AND archived_at IS NULL))
		 RETURNING id::text`, customerID, subject).Scan(&id); err != nil {
		t.Fatalf("create ticket: %v", err)
	}
	if _, err := harness.pool.Exec(ctx,
		`INSERT INTO support_messages (ticket_id, sender, body)
		 VALUES ($1::uuid, 'customer', 'I need help')`, id); err != nil {
		t.Fatalf("create message: %v", err)
	}
	return id
}

// TestACustomerCanHoldSeveralOpenTickets is the constraint v0.8 removes on
// purpose. A billing question and a connection problem are two conversations,
// and forcing them into one thread makes both harder to answer and impossible
// to route separately.
func TestACustomerCanHoldSeveralOpenTickets(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	operations := newOperations(t, harness)
	customerID := harness.customer(ctx, t)

	first := harness.ticket(ctx, t, customerID, "Billing")
	second := harness.ticket(ctx, t, customerID, "Cannot connect")
	if first == second {
		t.Fatal("the second ticket reused the first")
	}

	page, err := operations.SearchTickets(ctx, panelpg.TicketFilter{
		CustomerID: customerID, Status: "open", PageSize: 10,
	})
	if err != nil {
		t.Fatalf("search tickets: %v", err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("expected two open tickets, got %d", len(page.Items))
	}
	// Every ticket lands in the default queue, so a new ticket always has
	// somewhere to go.
	for _, ticket := range page.Items {
		if ticket.QueueCode != "general" {
			t.Fatalf("ticket %s landed in %q", ticket.ID, ticket.QueueCode)
		}
	}
}

// TestAnInternalNoteIsNeverAMessage is the separation that makes a private note
// undeliverable: the path that sends to a customer reads support_messages, and
// a note is not in it.
func TestAnInternalNoteIsNeverAMessage(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	operations := newOperations(t, harness)
	actor := harness.operator(ctx, t, "desk@example.test")
	customerID := harness.customer(ctx, t)
	ticketID := harness.ticket(ctx, t, customerID, "Refund please")

	if _, err := operations.AddNote(ctx, ticketID, "Chargeback risk — check first", actor); err != nil {
		t.Fatalf("add note: %v", err)
	}

	detail, err := operations.Ticket(ctx, ticketID)
	if err != nil {
		t.Fatalf("read ticket: %v", err)
	}
	if len(detail.Notes) != 1 {
		t.Fatalf("expected one note, got %d", len(detail.Notes))
	}
	for _, message := range detail.Messages {
		if message.Body == "Chargeback risk — check first" {
			t.Fatal("an internal note reached the message list")
		}
	}

	// The delivery path is the real test: a note must not appear among the
	// operator replies waiting to be sent to Telegram.
	var pending int
	if err := harness.pool.QueryRow(ctx,
		`SELECT count(*) FROM support_messages
		 WHERE ticket_id = $1::uuid AND sender = 'operator' AND delivered_at IS NULL`,
		ticketID).Scan(&pending); err != nil {
		t.Fatalf("count pending replies: %v", err)
	}
	if pending != 0 {
		t.Fatalf("a note produced %d deliverable replies", pending)
	}
}

// TestFirstResponseIsRecordedOnce covers the measure the workload report rests
// on: it must survive a conversation that goes back and forth.
func TestFirstResponseIsRecordedOnce(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	operations := newOperations(t, harness)
	actor := harness.operator(ctx, t, "reply@example.test")
	customerID := harness.customer(ctx, t)
	ticketID := harness.ticket(ctx, t, customerID, "Question")

	if _, err := operations.Reply(ctx, panelpg.ReplyInput{
		TicketID: ticketID, Body: "Looking into it", DedupeKey: "reply-1",
	}, actor); err != nil {
		t.Fatalf("first reply: %v", err)
	}
	first, err := operations.Ticket(ctx, ticketID)
	if err != nil {
		t.Fatalf("read ticket: %v", err)
	}
	if first.Ticket.FirstReply == nil {
		t.Fatal("the first reply did not record a first-response time")
	}

	// A resubmitted form reaches the message that already exists rather than
	// sending the customer the same answer twice.
	if _, err = operations.Reply(ctx, panelpg.ReplyInput{
		TicketID: ticketID, Body: "Looking into it", DedupeKey: "reply-1",
	}, actor); err != nil {
		t.Fatalf("replayed reply: %v", err)
	}
	if _, err = operations.Reply(ctx, panelpg.ReplyInput{
		TicketID: ticketID, Body: "Fixed now", DedupeKey: "reply-2",
	}, actor); err != nil {
		t.Fatalf("second reply: %v", err)
	}

	second, err := operations.Ticket(ctx, ticketID)
	if err != nil {
		t.Fatalf("read ticket: %v", err)
	}
	if len(second.Messages) != 3 {
		// One customer message and two operator replies; the replay adds none.
		t.Fatalf("expected three messages, got %d", len(second.Messages))
	}
	if !second.Ticket.FirstReply.Equal(*first.Ticket.FirstReply) {
		t.Fatal("a later reply moved the first-response time")
	}

	report, err := operations.SupportReport(ctx, 24*time.Hour)
	if err != nil {
		t.Fatalf("build report: %v", err)
	}
	if report.MedianFirstResponseSeconds < 0 {
		t.Fatal("median first response must not be negative")
	}
	if len(report.Definitions) == 0 {
		t.Fatal("the report must carry the definitions of its own numbers")
	}
}

// TestMergeKeepsBothConversations covers the merge rule: the absorbed ticket
// keeps its row, its messages move, and a merge across customers is refused.
func TestMergeKeepsBothConversations(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	operations := newOperations(t, harness)
	actor := harness.operator(ctx, t, "merge@example.test")

	customerID := harness.customer(ctx, t)
	survivor := harness.ticket(ctx, t, customerID, "Original")
	duplicate := harness.ticket(ctx, t, customerID, "Same thing again")

	stranger := harness.customer(ctx, t)
	strangerTicket := harness.ticket(ctx, t, stranger, "Somebody else")

	// Merging across customers would put one customer's words into another's
	// conversation, so it is refused rather than warned about.
	if _, err := operations.MergeTicket(ctx, strangerTicket, survivor, actor); err == nil {
		t.Fatal("expected a cross-customer merge to be refused")
	}

	merged, err := operations.MergeTicket(ctx, duplicate, survivor, actor)
	if err != nil {
		t.Fatalf("merge tickets: %v", err)
	}
	if merged.Status != "merged" || merged.MergedInto != survivor {
		t.Fatalf("merged ticket reads %s pointing at %q", merged.Status, merged.MergedInto)
	}

	detail, err := operations.Ticket(ctx, survivor)
	if err != nil {
		t.Fatalf("read survivor: %v", err)
	}
	if len(detail.Messages) != 2 {
		t.Fatalf("expected both conversations on the survivor, got %d messages", len(detail.Messages))
	}

	// The absorbed ticket still exists. Deleting it would lose the trail that
	// explains where the customer's words went.
	if _, err = operations.Ticket(ctx, duplicate); err != nil {
		t.Fatalf("the absorbed ticket should still be readable: %v", err)
	}
}
