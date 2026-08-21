//go:build integration

package integrationtest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/botapp"
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
	// The report used to carry the definitions of its own numbers as English
	// prose, and this asserted they were present. They live in the panel's
	// message catalogues now, beside every other operator-facing string, so the
	// payload carries figures and nothing a translator would need to touch.
	if report.Open < 0 || report.Unassigned < 0 || report.Resolved < 0 {
		t.Fatalf("report = %+v, want no negative counts", report)
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

// TestAnUnreachableCustomerDoesNotParkTheReplyQueue is the head-of-line
// defect: a reply to somebody the bot cannot push to used to stay pending
// forever, and once two hundred of them accumulated nobody behind them was ever
// delivered. A reply now leaves the queue in a terminal state either way, and
// the outcome is recorded where both the desk and the customer can read it.
func TestAnUnreachableCustomerDoesNotParkTheReplyQueue(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	operations := newOperations(t, harness)
	actor := harness.operator(ctx, t, "queue@example.test")
	store, err := botapp.NewPostgresStore(ctx, harness.url)
	if err != nil {
		t.Fatalf("build bot store: %v", err)
	}

	// The first customer signed in by magic link and has no Telegram identity;
	// the second is reachable in the chat.
	webOnly := harness.customer(ctx, t)
	reachable := harness.customer(ctx, t)
	crossSurfaceLinkTelegram(ctx, t, harness, reachable, "777001")
	webTicket := harness.ticket(ctx, t, webOnly, "Magic-link customer")
	chatTicket := harness.ticket(ctx, t, reachable, "Telegram customer")

	for index, ticketID := range []string{webTicket, chatTicket} {
		if _, err = operations.Reply(ctx, panelpg.ReplyInput{
			TicketID: ticketID, Body: "Here is the answer", DedupeKey: fmt.Sprintf("queue-%d", index),
		}, actor); err != nil {
			t.Fatalf("operator reply: %v", err)
		}
	}

	pending, err := store.PendingOperatorReplies(ctx, 10)
	if err != nil {
		t.Fatalf("claim pending replies: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("the queue holds %d replies, want both", len(pending))
	}
	// The web-only customer is in the claim with no chat to push to, so the
	// loop can record the outcome instead of never seeing the message.
	var webMessage, chatMessage int64
	for _, reply := range pending {
		switch reply.TicketID {
		case webTicket:
			webMessage = reply.MessageID
			if reply.TelegramID != 0 {
				t.Fatalf("a customer with no Telegram identity was claimed with chat %d", reply.TelegramID)
			}
		case chatTicket:
			chatMessage = reply.MessageID
			if reply.TelegramID != 777001 {
				t.Fatalf("the reachable customer was claimed with chat %d", reply.TelegramID)
			}
		}
	}

	if err = store.MarkOperatorReplyUndeliverable(ctx, webMessage, "no_telegram"); err != nil {
		t.Fatalf("record undeliverable: %v", err)
	}
	if err = store.MarkOperatorReplyFailed(ctx, chatMessage, "telegram_unavailable"); err != nil {
		t.Fatalf("record a transport failure: %v", err)
	}

	// The undeliverable message is out of the queue for good; the failed one is
	// retried because it has retries left.
	pending, err = store.PendingOperatorReplies(ctx, 10)
	if err != nil {
		t.Fatalf("claim pending replies again: %v", err)
	}
	if len(pending) != 1 || pending[0].MessageID != chatMessage {
		t.Fatalf("after one pass the queue holds %+v, want only the retryable reply", pending)
	}

	for range 2 {
		if err = store.MarkOperatorReplyFailed(ctx, chatMessage, "telegram_unavailable"); err != nil {
			t.Fatalf("record a transport failure: %v", err)
		}
	}
	pending, err = store.PendingOperatorReplies(ctx, 10)
	if err != nil {
		t.Fatalf("claim pending replies after the retry limit: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("a reply out of retries is still claimed: %+v", pending)
	}

	// The desk reads the outcome back beside the message rather than "queued".
	detail, err := operations.Ticket(ctx, webTicket)
	if err != nil {
		t.Fatalf("read the web-only ticket: %v", err)
	}
	var reply panelpg.SupportMessage
	for _, message := range detail.Messages {
		if message.Sender == "operator" {
			reply = message
		}
	}
	if reply.Delivery != "undeliverable" || reply.DeliveryReason != "no_telegram" {
		t.Fatalf("the desk shows %q (%q), want undeliverable because of no_telegram", reply.Delivery, reply.DeliveryReason)
	}

	// And a successful push writes the history row the customer's notification
	// screen reads, once.
	thirdTicket := harness.ticket(ctx, t, reachable, "Second question")
	if _, err = operations.Reply(ctx, panelpg.ReplyInput{
		TicketID: thirdTicket, Body: "Answered", DedupeKey: "queue-c",
	}, actor); err != nil {
		t.Fatalf("operator reply: %v", err)
	}
	pending, err = store.PendingOperatorReplies(ctx, 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("expected exactly the new reply to be pending, got %d (%v)", len(pending), err)
	}
	if err = store.MarkOperatorReplyDelivered(ctx, pending[0].MessageID); err != nil {
		t.Fatalf("record delivery: %v", err)
	}
	var sent int
	if err = harness.pool.QueryRow(ctx, `SELECT count(*)::integer FROM notification_deliveries
		WHERE user_id = $1::uuid AND kind = 'support' AND status = 'sent'`, reachable).Scan(&sent); err != nil {
		t.Fatalf("count support deliveries: %v", err)
	}
	if sent != 1 {
		t.Fatalf("a delivered reply left %d sent rows, want 1", sent)
	}
	if pending, err = store.PendingOperatorReplies(ctx, 10); err != nil || len(pending) != 0 {
		t.Fatalf("a delivered reply is still pending: %+v (%v)", pending, err)
	}
}
