//go:build integration

package integrationtest

import (
	"context"
	"strings"
	"testing"

	"github.com/omniflow/omniflow/internal/commercepg"
)

// lastTo is the text of the last message the bot sent to one chat.
func (stub *botTelegram) lastTo(t *testing.T, chatID int64) string {
	t.Helper()
	stub.mutex.Lock()
	defer stub.mutex.Unlock()
	for index := len(stub.requests) - 1; index >= 0; index-- {
		request := stub.requests[index]
		if request.Method == "sendMessage" && request.Fields["chat_id"] == itoa64(chatID) {
			return request.Fields["text"]
		}
	}
	t.Fatalf("nothing was sent to %d", chatID)
	return ""
}

func eventNoticeIDs(ctx context.Context, t *testing.T, harness *harness, customerID, kind string) []string {
	t.Helper()
	rows, err := harness.pool.Query(ctx, `SELECT id::text FROM customer_event_notices
		WHERE user_id = $1::uuid AND kind = $2 ORDER BY created_at`, customerID, kind)
	if err != nil {
		t.Fatalf("read notices: %v", err)
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

// Both sides of a referral hear about it: the inviter when somebody joins and
// when their reward is credited, the invitee with a welcome naming what they
// will get and again when their bonus arrives. Each notice goes out once, and
// a pair imported already qualified is not announced at all.
func TestReferralEventsNotifyBothSidesOnce(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	flow := newBotFlow(ctx, t, harness)
	store := harness.commerceStore(t, testOptions())
	if _, err := harness.pool.Exec(ctx, `INSERT INTO referral_programs
		(singleton, enabled, currency, inviter_reward_minor, invitee_reward_minor)
		VALUES (true, true, 'RUB', 20000, 10000)`); err != nil {
		t.Fatalf("configure the referral programme: %v", err)
	}
	inviter := flow.customerOf(ctx, t, 710001)
	invitee := flow.customerOf(ctx, t, 710002)
	if _, err := harness.pool.Exec(ctx, `INSERT INTO referral_codes (user_id, code) VALUES ($1::uuid, 'NOTICECODE')`, inviter); err != nil {
		t.Fatal(err)
	}
	if err := flow.store.AttributeReferralForCustomer(ctx, invitee, "noticecode"); err != nil {
		t.Fatalf("attribute: %v", err)
	}

	notifier := flow.notifier()
	notifier.RunOnce(ctx)
	joined := eventNoticeIDs(ctx, t, harness, inviter, "referral_joined")
	welcome := eventNoticeIDs(ctx, t, harness, invitee, "referral_welcome")
	if len(joined) != 1 || len(welcome) != 1 {
		t.Fatalf("notices: joined=%v welcome=%v, want one each", joined, welcome)
	}
	if status, _ := deliveryRow(ctx, t, harness, inviter, "referral", "event:"+joined[0]); status != "sent" {
		t.Fatalf("inviter's notice is %q, want sent", status)
	}
	if status, _ := deliveryRow(ctx, t, harness, invitee, "referral", "event:"+welcome[0]); status != "sent" {
		t.Fatalf("invitee's welcome is %q, want sent", status)
	}
	if body := flow.telegram.lastTo(t, 710002); !strings.Contains(body, "100") {
		t.Fatalf("welcome %q does not name the invitee's reward", body)
	}

	planVersionID := harness.catalog(ctx, t, "notice-plan", 25000)
	order, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: invitee, PlanVersionID: planVersionID, Currency: "RUB",
		Operation: "purchase", IdempotencyKey: "notice-purchase",
	})
	if err != nil {
		t.Fatalf("create order: %v", err)
	}
	harness.settle(ctx, t, store, uuidText(order.ID), 25000, "notice-event")
	// A replayed webhook grants nothing twice and so announces nothing twice.
	harness.settle(ctx, t, store, uuidText(order.ID), 25000, "notice-event")
	notifier.RunOnce(ctx)
	inviterRewards := eventNoticeIDs(ctx, t, harness, inviter, "referral_reward")
	inviteeRewards := eventNoticeIDs(ctx, t, harness, invitee, "referral_reward")
	if len(inviterRewards) != 1 || len(inviteeRewards) != 1 {
		t.Fatalf("reward notices: inviter=%v invitee=%v, want one each", inviterRewards, inviteeRewards)
	}
	if status, _ := deliveryRow(ctx, t, harness, inviter, "referral", "event:"+inviterRewards[0]); status != "sent" {
		t.Fatalf("inviter's reward notice is %q, want sent", status)
	}

	before := flow.telegram.sends(710001)
	notifier.RunOnce(ctx)
	if after := flow.telegram.sends(710001); after != before {
		t.Fatalf("a second pass sent %d more messages to the inviter", after-before)
	}

	// An imported pair that already qualified in Bedolaga is not news.
	imported := harness.customer(ctx, t)
	if _, err := harness.pool.Exec(ctx, `INSERT INTO referral_attributions
		(referred_user_id, referrer_user_id, code, qualified_at) VALUES ($1::uuid, $2::uuid, 'NOTICECODE', now())`,
		imported, inviter); err != nil {
		t.Fatal(err)
	}
	if ids := eventNoticeIDs(ctx, t, harness, inviter, "referral_joined"); len(ids) != 1 {
		t.Fatalf("an imported pair queued a notice: %v", ids)
	}
}

// A loyalty tier change tells the customer and the operators, the operators
// with identifiers and tier codes only.
func TestLoyaltyChangeNotifiesCustomerAndOperators(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	flow := newBotFlow(ctx, t, harness)
	goldID := harness.loyaltyProgram(ctx, t, 1, 1000)
	customer := flow.customerOf(ctx, t, 710101)
	harness.standIn(ctx, t, customer, goldID)
	if _, err := harness.pool.Exec(ctx, `INSERT INTO loyalty_standing_history (user_id, to_tier_id, evaluated_metric, reason)
		VALUES ($1::uuid, $2::uuid, 100000, 'evaluation')`, customer, goldID); err != nil {
		t.Fatal(err)
	}
	flow.notifier().RunOnce(ctx)
	notices := eventNoticeIDs(ctx, t, harness, customer, "loyalty_tier")
	if len(notices) != 1 {
		t.Fatalf("loyalty notices = %v, want one", notices)
	}
	if status, _ := deliveryRow(ctx, t, harness, customer, "loyalty", "event:"+notices[0]); status != "sent" {
		t.Fatalf("loyalty notice is %q, want sent", status)
	}
	if body := flow.telegram.lastTo(t, 710101); !strings.Contains(body, "Gold") || !strings.Contains(body, "10%") {
		t.Fatalf("loyalty notice %q does not name the tier and its discount", body)
	}
	var payload string
	if err := harness.pool.QueryRow(ctx, `SELECT payload::text FROM operator_notifications WHERE kind = 'marketing'`).Scan(&payload); err != nil {
		t.Fatalf("operator notice: %v", err)
	}
	if !strings.Contains(payload, `"toTier": "gold"`) || !strings.Contains(payload, customer) {
		t.Fatalf("operator payload %s", payload)
	}
}
