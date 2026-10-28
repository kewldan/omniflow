//go:build integration

package integrationtest

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// giftNotes reads the notes on a sender's gifts, oldest first. A gift with no
// note reads as an empty string.
func giftNotes(ctx context.Context, t *testing.T, harness *harness, senderID string) []string {
	t.Helper()
	rows, err := harness.pool.Query(ctx, `SELECT COALESCE(sender_message, '')
		FROM gifts WHERE sender_user_id = $1::uuid ORDER BY created_at, id`, senderID)
	if err != nil {
		t.Fatalf("read gifts: %v", err)
	}
	defer rows.Close()
	notes := make([]string, 0, 4)
	for rows.Next() {
		var note string
		if err := rows.Scan(&note); err != nil {
			t.Fatalf("scan gift: %v", err)
		}
		notes = append(notes, note)
	}
	return notes
}

// TestBotGiftWithANoteBecomesAnOrder drives the gift flow the way a customer
// does — the plan button, the note prompt, the typed note — and expects a gift
// carrying that note. The note prompt used to receive only "plan" from the
// callback, so the typed note could not be turned into a gift and every
// attempt ended on the gift list with nothing bought.
func TestBotGiftWithANoteBecomesAnOrder(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	flow := newBotFlow(ctx, t, harness)
	planVersionID := harness.catalog(ctx, t, "bot-gift-note", 30000)
	const sender = int64(700100)
	senderID := flow.customerOf(ctx, t, sender)

	flow.tap(ctx, sender, 10, "act:gift-plan")
	if !slices.Contains(flow.telegram.lastScreen(t).callbacks(t), "act:gift-message:plan:"+planVersionID) {
		t.Fatalf("the plan list offers no gift button for the plan: %v", flow.telegram.lastScreen(t).callbacks(t))
	}
	flow.tap(ctx, sender, 10, "act:gift-message:plan:"+planVersionID)
	prompt := flow.telegram.lastScreen(t)
	if !slices.Contains(prompt.callbacks(t), "act:gift-buy:plan:"+planVersionID) {
		t.Fatalf("the note prompt offers no way to give without a note: %v", prompt.callbacks(t))
	}
	flow.say(ctx, sender, "Happy birthday!")

	if notes := giftNotes(ctx, t, harness, senderID); !slices.Equal(notes, []string{"Happy birthday!"}) {
		t.Fatalf("gifts after the note = %q, want one gift carrying the note", notes)
	}
	if body := flow.telegram.lastScreen(t).Fields["text"]; !strings.Contains(body, "<code>") {
		t.Fatalf("the sender was not shown the claim code: %q", body)
	}
}

// TestBotGiftWithoutANoteBecomesAnOrder covers the two ways to skip the note:
// the button on the prompt, and the dash the prompt tells the customer to
// send. Neither delivers a note to the recipient.
func TestBotGiftWithoutANoteBecomesAnOrder(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	flow := newBotFlow(ctx, t, harness)
	planVersionID := harness.catalog(ctx, t, "bot-gift-plain", 30000)
	const sender = int64(700200)
	senderID := flow.customerOf(ctx, t, sender)

	flow.tap(ctx, sender, 11, "act:gift-message:plan:"+planVersionID)
	flow.tap(ctx, sender, 11, "act:gift-buy:plan:"+planVersionID)
	flow.tap(ctx, sender, 12, "act:gift-message:credit:50000")
	flow.say(ctx, sender, "-")

	if notes := giftNotes(ctx, t, harness, senderID); !slices.Equal(notes, []string{"", ""}) {
		t.Fatalf("gifts = %q, want two gifts and no note on either", notes)
	}
}

// TestBotGiftNoteOverTheLimitKeepsThePromptOpen sends a note one character
// longer than the gift record accepts. It used to reach the database, fail its
// check, and read "the order could not be created"; now the bot says how long
// a note may be and the shortened note completes the same gift.
func TestBotGiftNoteOverTheLimitKeepsThePromptOpen(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	flow := newBotFlow(ctx, t, harness)
	planVersionID := harness.catalog(ctx, t, "bot-gift-long", 30000)
	const sender = int64(700300)
	senderID := flow.customerOf(ctx, t, sender)

	flow.tap(ctx, sender, 13, "act:gift-message:plan:"+planVersionID)
	flow.say(ctx, sender, strings.Repeat("я", 201))
	if notes := giftNotes(ctx, t, harness, senderID); len(notes) != 0 {
		t.Fatalf("a note over the limit created gifts: %q", notes)
	}
	if body := flow.telegram.lastScreen(t).Fields["text"]; !strings.Contains(body, "200") {
		t.Fatalf("the refusal does not say how long a note may be: %q", body)
	}

	flow.say(ctx, sender, strings.Repeat("я", 200))
	if notes := giftNotes(ctx, t, harness, senderID); len(notes) != 1 || len([]rune(notes[0])) != 200 {
		t.Fatalf("the shortened note did not complete the gift: %q", notes)
	}
}
