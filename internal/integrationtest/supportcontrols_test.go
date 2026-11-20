//go:build integration

package integrationtest

import (
	"context"
	"testing"

	"github.com/omniflow/omniflow/internal/accountsupport"
	"github.com/omniflow/omniflow/internal/panelpg"
)

// A customer's chosen topic and urgency route the ticket; an operator hides a
// message from the customer, closes every open ticket, and blocks a customer
// from support, which the database enforces for every surface.
func TestSupportControls(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service, err := panelpg.New(harness.pool, operationsKey, panelpg.Options{})
	if err != nil {
		t.Fatal(err)
	}
	support := newAccountSupport(t, harness)
	actor := harness.operator(ctx, t, "desk@example.test")
	customer := harness.customer(ctx, t)

	conversation, err := support.CreateTicket(ctx, accountsupport.NewTicket{
		CustomerID: customer, Subject: "Оплата не прошла", Body: "Списали деньги, подписки нет", Topic: "payment", Urgent: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var queue, priority string
	if err = harness.pool.QueryRow(ctx, `SELECT q.code, t.priority FROM support_tickets t JOIN support_queues q ON q.id = t.queue_id WHERE t.id = $1::uuid`, conversation.Ticket.ID).Scan(&queue, &priority); err != nil || queue != "payment" || priority != "high" {
		t.Fatalf("ticket queue = %q priority = %q, %v", queue, priority, err)
	}

	var messageID int64
	if err = harness.pool.QueryRow(ctx, `INSERT INTO support_messages (ticket_id, sender, body) VALUES ($1::uuid, 'operator', 'Ваш пароль: hunter2') RETURNING id`, conversation.Ticket.ID).Scan(&messageID); err != nil {
		t.Fatal(err)
	}
	if err = service.DeleteSupportMessage(ctx, conversation.Ticket.ID, messageID, actor); err != nil {
		t.Fatal(err)
	}
	seen, err := support.Conversation(ctx, customer, conversation.Ticket.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range seen.Messages {
		if message.ID == messageID {
			t.Fatal("a hidden message must not reach the customer")
		}
	}

	actor.Reason = "incident answered"
	if closed, err := service.CloseAllSupportTickets(ctx, "", actor); err != nil || closed != 1 {
		t.Fatalf("closed = %d, %v", closed, err)
	}

	if err = service.SetCustomerRestriction(ctx, customer, "support", "abusive language", true, actor); err != nil {
		t.Fatal(err)
	}
	if _, err = support.CreateTicket(ctx, accountsupport.NewTicket{CustomerID: customer, Subject: "Again", Body: "Again"}); !accountsupport.SupportBlocked(err) {
		t.Fatalf("a blocked customer opening a ticket: %v", err)
	}
}
