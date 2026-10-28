//go:build integration

package integrationtest

import (
	"context"
	"log/slog"
	"testing"

	"github.com/go-telegram/bot/models"
	"github.com/omniflow/omniflow/internal/botapp"
)

func deliveryStatus(ctx context.Context, t *testing.T, harness *harness, customerID string) string {
	t.Helper()
	var status string
	if err := harness.pool.QueryRow(ctx, `SELECT COALESCE(
		(SELECT status FROM bot_delivery_state WHERE user_id = $1::uuid), 'active')`, customerID).
		Scan(&status); err != nil {
		t.Fatalf("read delivery state: %v", err)
	}
	return status
}

func chatMembership(telegramID int64, status models.ChatMemberType) *models.Update {
	return &models.Update{MyChatMember: &models.ChatMemberUpdated{
		Chat:          models.Chat{ID: telegramID, Type: models.ChatTypePrivate},
		From:          models.User{ID: telegramID},
		NewChatMember: models.ChatMember{Type: status},
	}}
}

// TestBotDeliveryStateClearsWhenTheCustomerIsHeardFrom walks one customer
// through every way the bot learns about its chat.
//
// A blocked state used to be cleared only by a successful send, and blocked
// customers are left out of every notification pass — so it was never
// cleared. A customer who unblocked the bot kept using it and never received a
// reminder, a receipt, or a support reply push again.
func TestBotDeliveryStateClearsWhenTheCustomerIsHeardFrom(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	flow := newBotFlow(ctx, t, harness)
	const account = int64(700800)
	customerID := flow.customerOf(ctx, t, account)

	// Telegram's answer for somebody who never pressed Start is recorded as
	// its own state, not as a block.
	flow.telegram.refuse["sendMessage"] = "Forbidden: bot can't initiate conversation with a user"
	sender := botapp.NewSender(flow.client, flow.store, slog.New(slog.DiscardHandler))
	if err := sender.Send(ctx, customerID, account, botapp.View{Text: "hello"}); err == nil {
		t.Fatal("a refused send reported success")
	}
	delete(flow.telegram.refuse, "sendMessage")
	if status := deliveryStatus(ctx, t, harness, customerID); status != "not_started" {
		t.Fatalf("after Telegram refused to initiate the chat the state is %q, want not_started", status)
	}

	flow.say(ctx, account, "/start")
	if status := deliveryStatus(ctx, t, harness, customerID); status != "active" {
		t.Fatalf("after the customer wrote to the bot the state is %q, want active", status)
	}

	flow.deliver(ctx, chatMembership(account, models.ChatMemberTypeBanned))
	if status := deliveryStatus(ctx, t, harness, customerID); status != "blocked" {
		t.Fatalf("after the customer blocked the bot the state is %q, want blocked", status)
	}
	flow.deliver(ctx, chatMembership(account, models.ChatMemberTypeMember))
	if status := deliveryStatus(ctx, t, harness, customerID); status != "active" {
		t.Fatalf("after the customer restarted the bot the state is %q, want active", status)
	}

	if _, err := harness.pool.Exec(ctx, `UPDATE bot_delivery_state SET status = 'blocked'
		WHERE user_id = $1::uuid`, customerID); err != nil {
		t.Fatalf("block the chat: %v", err)
	}
	flow.tap(ctx, account, 40, "nav:home")
	if status := deliveryStatus(ctx, t, harness, customerID); status != "active" {
		t.Fatalf("after the customer tapped a button the state is %q, want active", status)
	}
}
