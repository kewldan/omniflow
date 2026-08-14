//go:build integration

package integrationtest

import (
	"context"
	"testing"
	"time"
)

// Notification history and the test send, against a real database.
//
// The properties worth asserting here are the ones no Go test can see. The
// deduplication that makes a double-click one message is a conflict clause with
// a WHERE on it. The reason a suppressed message carries is a column the
// notifier writes and this reads. And the claim the bot performs is a join to
// the Telegram-identity view, which decides whether an unreachable customer
// produces a delivery attempt at all.

func TestTheHistoryAnswersWhyAMessageNeverArrived(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service := newOperations(t, harness)

	customer := seedCustomer(ctx, t, harness, "history@example.test")

	// One that went out, and two that deliberately did not — which is the whole
	// point. "Suppressed with a reason" is an answer; "no record" is not.
	seedDelivery(ctx, t, harness, customer, "expiry", "sent", "")
	seedDelivery(ctx, t, harness, customer, "marketing", "suppressed", "frequency_cap")
	seedDelivery(ctx, t, harness, customer, "news", "suppressed", "no_consent")

	page, err := service.Deliveries(ctx, customer, "", "", 0, 50)
	if err != nil {
		t.Fatalf("read history: %v", err)
	}
	if page.Total != 3 || len(page.Deliveries) != 3 {
		t.Fatalf("the history holds %d of %d", len(page.Deliveries), page.Total)
	}

	reasons := map[string]string{}
	for _, delivery := range page.Deliveries {
		reasons[delivery.Kind] = delivery.Reason
	}
	if reasons["marketing"] != "frequency_cap" || reasons["news"] != "no_consent" {
		t.Fatalf("the suppression reasons read %v", reasons)
	}
	if reasons["expiry"] != "" {
		t.Fatalf("a sent message carries the reason %q", reasons["expiry"])
	}

	// Filtering narrows both the page and the total, so the count under the
	// table describes what is in it rather than the whole history.
	filtered, err := service.Deliveries(ctx, customer, "", "suppressed", 0, 50)
	if err != nil {
		t.Fatalf("filter: %v", err)
	}
	if filtered.Total != 2 {
		t.Fatalf("filtering to suppressed reports %d", filtered.Total)
	}

	summaries, err := service.DeliverySummaries(ctx, customer)
	if err != nil {
		t.Fatalf("summarise: %v", err)
	}
	byKind := map[string]int64{}
	for _, summary := range summaries {
		byKind[summary.Kind] = summary.Suppressed
	}
	if byKind["marketing"] != 1 || byKind["expiry"] != 0 {
		t.Fatalf("the summary reads %v", byKind)
	}
}

// A double-click is one message. The dedupe is a conflict clause with a WHERE
// on it, which is not a thing a Go test can exercise.
func TestATestSendIsQueuedOnceNoMatterHowManyClicks(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service := newOperations(t, harness)
	actor := harness.operator(ctx, t, "tester@example.test")

	customer := seedCustomer(ctx, t, harness, "tested@example.test")

	first, err := service.SendTestNotification(ctx, customer, actor)
	if err != nil {
		t.Fatalf("first test: %v", err)
	}
	if !first.Queued || first.Status != "pending" {
		t.Fatalf("the first test reports %+v", first)
	}

	second, err := service.SendTestNotification(ctx, customer, actor)
	if err != nil {
		t.Fatalf("second test: %v", err)
	}
	if second.Queued {
		t.Fatal("an impatient second click queued a second message")
	}

	var queued int
	if err := harness.pool.QueryRow(ctx,
		`SELECT count(*) FROM notification_deliveries WHERE user_id = $1::uuid AND kind = 'test'`,
		customer,
	).Scan(&queued); err != nil {
		t.Fatalf("count: %v", err)
	}
	if queued != 1 {
		t.Fatalf("%d test deliveries were queued", queued)
	}

	// And it is a kind of its own, so it can never be read back as a real notice
	// or counted against a marketing frequency budget.
	page, err := service.Deliveries(ctx, customer, "test", "", 0, 50)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if page.Total != 1 || page.Deliveries[0].Class != "transactional" {
		t.Fatalf("the queued test reads %+v", page.Deliveries)
	}
}

// A test send is an operator action on somebody else's account, so it is in the
// audit trail like every other one.
func TestATestSendIsAudited(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service := newOperations(t, harness)
	actor := harness.operator(ctx, t, "audited@example.test")

	customer := seedCustomer(ctx, t, harness, "audit-target@example.test")
	if _, err := service.SendTestNotification(ctx, customer, actor); err != nil {
		t.Fatalf("test send: %v", err)
	}

	var events int
	if err := harness.pool.QueryRow(ctx,
		`SELECT count(*) FROM audit_events
		 WHERE action = 'customer.notification.tested' AND target_id = $1`, customer,
	).Scan(&events); err != nil {
		t.Fatalf("count audit events: %v", err)
	}
	if events != 1 {
		t.Fatalf("%d audit events for a test send", events)
	}
}

func seedDelivery(
	ctx context.Context, t *testing.T, harness *harness,
	customerID, kind, status, reason string,
) {
	t.Helper()
	class := "transactional"
	if kind == "marketing" {
		class = "marketing"
	}
	var sentAt any
	if status == "sent" {
		sentAt = time.Now().UTC()
	}
	if _, err := harness.pool.Exec(ctx, `
		INSERT INTO notification_deliveries
			(user_id, kind, dedupe_key, status, class, error_code, sent_at)
		VALUES ($1::uuid, $2, $3, $4, $5, NULLIF($6, ''), $7)`,
		customerID, kind, "seed-"+kind, status, class, reason, sentAt,
	); err != nil {
		t.Fatalf("seed delivery %s: %v", kind, err)
	}
}
