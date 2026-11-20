//go:build integration

package integrationtest

import (
	"context"
	"testing"

	"github.com/omniflow/omniflow/internal/panelpg"
)

// A maintenance transition queues one customer notice when the settings ask
// for it, none when they do not, and none for a repeat of the same state.
func TestMaintenanceTransitionQueuesCustomerNotice(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service := newOperations(t, harness)
	actor := harness.operator(ctx, t, "maintenance-broadcast@example.test")
	actor.Reason = "planned upgrade"

	count := func() int {
		t.Helper()
		var total int
		if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM maintenance_broadcasts`).Scan(&total); err != nil {
			t.Fatal(err)
		}
		return total
	}
	on := panelpg.MaintenanceInput{Active: true, NoticeRU: "Обновляем серверы", NoticeEN: "Upgrading servers"}

	// Off by default: nothing is queued.
	if _, err := service.SetMaintenance(ctx, on, actor); err != nil {
		t.Fatalf("activate: %v", err)
	}
	if _, err := service.SetMaintenance(ctx, panelpg.MaintenanceInput{}, actor); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got := count(); got != 0 {
		t.Fatalf("a notice was queued with broadcasting off: %d", got)
	}

	if _, err := harness.pool.Exec(ctx, `INSERT INTO installation_settings (section, document)
		VALUES ('maintenance', '{"broadcastCustomers": true, "broadcastEmail": true}')
		ON CONFLICT (section) DO UPDATE SET document = EXCLUDED.document`); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetMaintenance(ctx, on, actor); err != nil {
		t.Fatalf("activate: %v", err)
	}
	// Rewording the notice while maintenance stays on is not a transition.
	on.NoticeEN = "Upgrading servers, part two"
	if _, err := service.SetMaintenance(ctx, on, actor); err != nil {
		t.Fatalf("reword: %v", err)
	}
	if got := count(); got != 1 {
		t.Fatalf("expected one notice after activation, got %d", got)
	}
	var (
		transition, noticeEN string
		email                bool
	)
	if err := harness.pool.QueryRow(ctx, `SELECT transition, notice_en, email FROM maintenance_broadcasts
		ORDER BY created_at DESC LIMIT 1`).Scan(&transition, &noticeEN, &email); err != nil {
		t.Fatal(err)
	}
	if transition != "activated" || noticeEN != "Upgrading servers" || !email {
		t.Fatalf("unexpected notice: %s %q %v", transition, noticeEN, email)
	}

	// The bot delivers it once per customer through the ordinary pipeline.
	flow := newBotFlow(ctx, t, harness)
	customer := flow.customerOf(ctx, t, 700951)
	var broadcastID string
	if err := harness.pool.QueryRow(ctx, `SELECT id::text FROM maintenance_broadcasts
		ORDER BY created_at DESC LIMIT 1`).Scan(&broadcastID); err != nil {
		t.Fatal(err)
	}
	flow.notifier().RunOnce(ctx)
	flow.notifier().RunOnce(ctx)
	if status, _ := deliveryRow(ctx, t, harness, customer, "maintenance", broadcastID); status != "sent" {
		t.Fatalf("the maintenance notice is %q, want sent", status)
	}
	if sends := flow.telegram.sends(700951); sends != 1 {
		t.Fatalf("the customer was sent %d maintenance notices, want 1", sends)
	}

	if _, err := service.SetMaintenance(ctx, panelpg.MaintenanceInput{}, actor); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got := count(); got != 2 {
		t.Fatalf("expected a second notice after clearing, got %d", got)
	}
}
