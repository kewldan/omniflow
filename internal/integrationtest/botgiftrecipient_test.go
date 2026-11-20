//go:build integration

package integrationtest

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// A sender in the bot names a @username after choosing the gift; the paid gift
// is claimed for that customer by the notifier and they are told in the bot,
// once however often the pass runs.
func TestBotGiftAddressedToAUsernameReachesItsRecipient(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	flow := newBotFlow(ctx, t, harness)
	planVersionID := harness.catalog(ctx, t, "bot-gift-addressed", 30000)
	const sender, friend = int64(700500), int64(700501)
	senderID := flow.customerOf(ctx, t, sender)
	friendID := flow.customerOf(ctx, t, friend)
	orders := harness.commerceStore(t, testOptions())
	if err := orders.RecordTelegramUsername(ctx, friendID, "Friend_User"); err != nil {
		t.Fatal(err)
	}
	harness.seedWallet(ctx, t, senderID, "bot-gift-addressed-wallet", 100000)

	flow.tap(ctx, sender, 20, "act:gift-message:plan:"+planVersionID)
	flow.say(ctx, sender, "@nobody_here")
	if body := flow.telegram.lastScreen(t).Fields["text"]; !strings.Contains(body, "Nobody with that @username") {
		t.Fatalf("an unknown username was not refused: %q", body)
	}
	flow.say(ctx, sender, "@friend_user")
	prompt := flow.telegram.lastScreen(t)
	if !strings.Contains(prompt.Fields["text"], "@friend_user") || !slices.Contains(prompt.callbacks(t), "act:gift-nonote") {
		t.Fatalf("the note prompt = %q %v", prompt.Fields["text"], prompt.callbacks(t))
	}
	flow.tap(ctx, sender, 21, "act:gift-nonote")
	if body := flow.telegram.lastScreen(t).Fields["text"]; !strings.Contains(body, "@friend_user") {
		t.Fatalf("the code screen does not say who gets the gift: %q", body)
	}
	var username, status string
	if err := harness.pool.QueryRow(ctx, `SELECT r.username, r.status FROM gift_recipients r
		JOIN gifts g ON g.id = r.gift_id WHERE g.sender_user_id = $1::uuid`, senderID).Scan(&username, &status); err != nil ||
		username != "friend_user" || status != "waiting" {
		t.Fatalf("recipient row = %q %q (%v)", username, status, err)
	}

	notifier := flow.notifier()
	notifier.RunOnce(ctx)
	notifier.RunOnce(ctx)
	if sent := flow.telegram.sends(friend); sent != 1 {
		t.Fatalf("the recipient was told %d times, want once", sent)
	}
	var entitlements int
	if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM entitlements WHERE user_id = $1::uuid`, friendID).Scan(&entitlements); err != nil || entitlements != 1 {
		t.Fatalf("the recipient holds %d entitlements (%v)", entitlements, err)
	}
}
