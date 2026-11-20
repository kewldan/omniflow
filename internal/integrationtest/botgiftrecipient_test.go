//go:build integration

package integrationtest

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"
)

// A sender in the bot names a @username after choosing the gift; the paid gift
// is claimed for that customer by the notifier and they are told in the bot,
// once however often the pass runs, and the sender hears that it arrived.
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
	flow.say(ctx, sender, "@sender")
	if body := flow.telegram.lastScreen(t).Fields["text"]; !strings.Contains(body, "cannot send a gift to yourself") {
		t.Fatalf("a gift to the sender's own username was not refused: %q", body)
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
	if notices := harness.giftSenderNotices(ctx, t, senderID); notices != 1 {
		t.Fatalf("the sender was told %d times that the gift arrived, want once", notices)
	}
}

func (harness *harness) giftSenderNotices(ctx context.Context, t *testing.T, senderID string) int {
	t.Helper()
	var notices int
	if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM notification_deliveries
		WHERE user_id = $1::uuid AND dedupe_key LIKE 'gift-sent:%'`, senderID).Scan(&notices); err != nil {
		t.Fatal(err)
	}
	return notices
}

// sayAs delivers a message from an account with its own Telegram username.
func (flow *botFlow) sayAs(ctx context.Context, telegramID int64, username, body string) {
	flow.deliver(ctx, &models.Update{Message: &models.Message{
		ID:   int(flow.update + 1),
		From: &models.User{ID: telegramID, LanguageCode: "en", Username: username},
		Chat: models.Chat{ID: telegramID, Type: models.ChatTypePrivate},
		Text: body,
	}})
}

// A gift bought for a @username nobody has used the bot with waits; the first
// message from that username claims it for the newcomer, who is told, and the
// sender hears that it arrived.
func TestBotGiftForAPhantomUsernameIsClaimedOnTheirFirstMessage(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	flow := newBotFlow(ctx, t, harness)
	planVersionID := harness.catalog(ctx, t, "bot-gift-phantom", 30000)
	const sender, newcomer = int64(700600), int64(700601)
	senderID := flow.customerOf(ctx, t, sender)
	harness.seedWallet(ctx, t, senderID, "bot-gift-phantom-wallet", 100000)

	flow.tap(ctx, sender, 30, "act:gift-message:plan:"+planVersionID)
	flow.say(ctx, sender, "@Phantom_Pal")
	if body := flow.telegram.lastScreen(t).Fields["text"]; !strings.Contains(body, "has not used our bot yet") {
		t.Fatalf("the sender was not told the gift will wait: %q", body)
	}
	flow.tap(ctx, sender, 31, "act:gift-nonote")
	if body := flow.telegram.lastScreen(t).Fields["text"]; !strings.Contains(body, "waits for their first message") {
		t.Fatalf("the code screen does not say the gift waits: %q", body)
	}
	notifier := flow.notifier()
	notifier.RunOnce(ctx)
	var status string
	if err := harness.pool.QueryRow(ctx, `SELECT r.status FROM gift_recipients r JOIN gifts g ON g.id = r.gift_id
		WHERE g.sender_user_id = $1::uuid`, senderID).Scan(&status); err != nil || status != "waiting" {
		t.Fatalf("before the newcomer writes the gift is %q (%v), want waiting", status, err)
	}

	flow.sayAs(ctx, newcomer, "phantom_pal", "/start")
	notifier.RunOnce(ctx)
	notifier.RunOnce(ctx)
	newcomerID := flow.customerOf(ctx, t, newcomer)
	var entitlements int
	if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM entitlements WHERE user_id = $1::uuid`, newcomerID).Scan(&entitlements); err != nil || entitlements != 1 {
		t.Fatalf("the newcomer holds %d entitlements (%v), want the gift", entitlements, err)
	}
	if sent := flow.telegram.sends(newcomer); sent < 2 {
		t.Fatalf("the newcomer was sent %d messages, want the start screen and the gift", sent)
	}
	if notices := harness.giftSenderNotices(ctx, t, senderID); notices != 1 {
		t.Fatalf("the sender was told %d times that the gift arrived, want once", notices)
	}
}
