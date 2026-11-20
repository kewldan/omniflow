//go:build integration

package integrationtest

import (
	"context"
	"log/slog"
	"testing"

	"github.com/omniflow/omniflow/internal/accountpg"
)

// A customer's support version moves when an operator replies to them, and
// never because of another customer's conversation.
func TestAccountLiveVersionsAreTheCustomersOwn(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	account, err := accountpg.New(harness.pool, accountpg.UnavailablePanel{}, accountpg.Options{Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	customer := harness.customer(ctx, t)
	other := harness.customer(ctx, t)
	ticket := harness.ticket(ctx, t, customer, "Не подключается")
	otherTicket := harness.ticket(ctx, t, other, "Другой вопрос")

	before, err := account.LiveVersions(ctx, customer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = harness.pool.Exec(ctx, `INSERT INTO support_messages (ticket_id, sender, body)
		VALUES ($1::uuid, 'operator', 'Ответ другому')`, otherTicket); err != nil {
		t.Fatal(err)
	}
	unrelated, err := account.LiveVersions(ctx, customer)
	if err != nil {
		t.Fatal(err)
	}
	if unrelated["support"] != before["support"] {
		t.Fatalf("another customer's reply moved this customer's version: %v -> %v", before, unrelated)
	}
	if _, err = harness.pool.Exec(ctx, `INSERT INTO support_messages (ticket_id, sender, body)
		VALUES ($1::uuid, 'operator', 'Попробуйте переподключиться')`, ticket); err != nil {
		t.Fatal(err)
	}
	after, err := account.LiveVersions(ctx, customer)
	if err != nil {
		t.Fatal(err)
	}
	if after["support"] == before["support"] {
		t.Fatalf("the operator's reply did not move the support version: %v", after)
	}
	if after["orders"] != before["orders"] || after["subscriptions"] != before["subscriptions"] {
		t.Fatalf("unrelated areas moved: %v -> %v", before, after)
	}
}
