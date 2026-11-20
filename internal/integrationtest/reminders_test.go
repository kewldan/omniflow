//go:build integration

package integrationtest

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/panelpg"
)

// A reminder for customers with no subscription reaches such a customer once
// a day up to its send limit, never one who has a subscription, and its
// cabinet card disappears — with the messages — once dismissed.
func TestRemindersFollowTheirSegmentAndLimits(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	flow := newBotFlow(ctx, t, harness)
	panel, err := panelpg.New(harness.pool, operationsKey, panelpg.Options{})
	if err != nil {
		t.Fatal(err)
	}
	const lonely, subscribed = 720001, 720002
	lonelyID := flow.customerOf(ctx, t, lonely)
	subscribedID := flow.customerOf(ctx, t, subscribed)
	subscription := harness.seedSubscription(ctx, t, subscribedID, 1)
	harness.seedSubscriptionEntitlement(ctx, t, subscribedID, subscription,
		harness.catalog(ctx, t, "reminder-plan", 10000), "active", "30 days")

	if _, err = panel.SaveReminder(ctx, panelpg.Reminder{Name: "x", Segment: "nonsense"}, panelpg.Actor{Type: "admin"}); err == nil {
		t.Fatal("an unknown segment was accepted")
	}
	reminder, err := panel.SaveReminder(ctx, panelpg.Reminder{
		Name: "Come and try", Active: true, Segment: "no_subscription", RepeatEveryDays: 1, MaxSends: 2,
		TitleRU: "Попробуйте", BodyRU: "Есть пробный период", TitleEN: "Try it", BodyEN: "There is a <trial>",
		ButtonRoute: "plans", ShowInCabinet: true,
	}, panelpg.Actor{Type: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	if count, err := panel.ReminderAudience(ctx, reminder.ID); err != nil || count != 1 {
		t.Fatalf("audience = %d (%v), want only the customer with no subscription", count, err)
	}

	notifier := flow.notifier()
	notifier.DeliverReminders(ctx)
	notifier.DeliverReminders(ctx)
	if sent := flow.telegram.sends(lonely); sent != 1 {
		t.Fatalf("the customer got %d reminders; one a day at most", sent)
	}
	if sent := flow.telegram.sends(subscribed); sent != 0 {
		t.Fatalf("a subscribed customer got %d reminders", sent)
	}

	var userID, reminderID pgtype.UUID
	_ = userID.Scan(lonelyID)
	_ = reminderID.Scan(reminder.ID)
	queries := dbgen.New(harness.pool)
	cards, err := queries.ListCabinetReminders(ctx, userID)
	if err != nil || len(cards) != 1 {
		t.Fatalf("cards = %d (%v)", len(cards), err)
	}
	if err = queries.DismissReminder(ctx, dbgen.DismissReminderParams{UserID: userID, ReminderID: reminderID}); err != nil {
		t.Fatal(err)
	}
	if cards, _ = queries.ListCabinetReminders(ctx, userID); len(cards) != 0 {
		t.Fatal("a dismissed card is still shown")
	}
	if count, _ := panel.ReminderAudience(ctx, reminder.ID); count != 1 {
		t.Fatal("the audience preview counts the segment, dismissed or not")
	}
}
