//go:build integration

package integrationtest

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/errorlog"
	"github.com/omniflow/omniflow/internal/installsettings"
	"github.com/omniflow/omniflow/internal/jobs"
	"github.com/omniflow/omniflow/internal/operator"
	"github.com/omniflow/omniflow/internal/panelpg"
)

// The installation settings an operator saves are read by the processes, the
// cadence gate runs a due pass once, the operator streams queue their notices
// from triggers (and stay quiet during a Bedolaga import), SLA breaches and
// scheduled reports are queued once, and logged errors land in the error log.
func TestOperationsRuntime(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	pool := harness.pool

	// Settings: a saved document is read, and a save is seen after Invalidate.
	if _, err := pool.Exec(ctx, `UPDATE installation_settings SET document = '{"reconcileIntervalMinutes": 30}' WHERE section = 'remnawave'`); err != nil {
		t.Fatal(err)
	}
	reader := installsettings.New(pool, slog.New(slog.DiscardHandler))
	if got := reader.Section(ctx, "remnawave").Positive("reconcileIntervalMinutes", 15); got != 30 {
		t.Fatalf("reconcile interval = %d", got)
	}
	if _, err := pool.Exec(ctx, `UPDATE installation_settings SET document = '{"reconcileIntervalMinutes": 45}' WHERE section = 'remnawave'`); err != nil {
		t.Fatal(err)
	}
	if got := reader.Section(ctx, "remnawave").Positive("reconcileIntervalMinutes", 15); got != 30 {
		t.Fatalf("a cached read must not hit the database, got %d", got)
	}
	reader.Invalidate("remnawave")
	if got := reader.Section(ctx, "remnawave").Positive("reconcileIntervalMinutes", 15); got != 45 {
		t.Fatalf("after invalidation = %d", got)
	}
	for _, section := range []string{"bot", "support", "reports", "access", "moderation"} {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM installation_settings WHERE section = $1)`, section).Scan(&exists); err != nil || !exists {
			t.Fatalf("section %s is not seeded: %v", section, err)
		}
	}

	// The gate runs a due pass once per interval and once per daily slot.
	gate := jobs.NewGate(pool)
	for attempt, want := range []bool{true, false} {
		due, err := gate.Claim(ctx, "test_pass", time.Hour)
		if err != nil || due != want {
			t.Fatalf("claim %d = %v, %v", attempt, due, err)
		}
	}
	morning := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	if due, _ := gate.ClaimAt(ctx, "test_slot", morning, 9*60); due {
		t.Fatal("a slot is not due before its time")
	}
	if due, _ := gate.ClaimAt(ctx, "test_slot", morning.Add(2*time.Hour), 9*60); !due {
		t.Fatal("a slot is due once its time passed")
	}
	if due, _ := gate.ClaimAt(ctx, "test_slot", morning.Add(3*time.Hour), 9*60); due {
		t.Fatal("a slot runs once a day")
	}

	// A trial claim queues a trial notice; the same row written by an import
	// does not.
	customer := harness.customer(ctx, t)
	store := harness.commerceStore(t, testOptions())
	orderID, _, _ := harness.paidOrder(ctx, t, store, customer, "ops-runtime", 1000)
	planID := ""
	if err := pool.QueryRow(ctx, `SELECT id::text FROM plans WHERE code = 'ops-runtime'`).Scan(&planID); err != nil {
		t.Fatalf("plan: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO trial_claims (user_id, plan_id, order_id) VALUES ($1::uuid, $2::uuid, $3::uuid)`, customer, planID, orderID); err != nil {
		t.Fatal(err)
	}
	if count := countNotices(ctx, t, harness, "trial"); count != 1 {
		t.Fatalf("trial notices = %d", count)
	}
	imported := harness.customer(ctx, t)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `SELECT set_config('omniflow.import', 'on', true)`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO trial_claims (user_id, plan_id, order_id) VALUES ($1::uuid, $2::uuid, $3::uuid)`, imported, planID, orderID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if count := countNotices(ctx, t, harness, "trial"); count != 1 {
		t.Fatalf("an imported trial must not be announced, notices = %d", count)
	}

	// An overdue first response is announced once, without the subject.
	ticketID := harness.ticket(ctx, t, customer, "Где мой ключ?")
	if _, err = pool.Exec(ctx, `UPDATE support_queues SET first_response_target_seconds = 60`); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE support_tickets SET created_at = now() - interval '2 hours' WHERE id = $1::uuid`, ticketID); err != nil {
		t.Fatal(err)
	}
	reports := operator.NewReports(pool, installsettings.Static(nil), slog.New(slog.DiscardHandler))
	for range 2 {
		if err = reports.RunSupportSLA(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var breaches int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM operator_notifications WHERE kind = 'support' AND payload->>'event' = 'sla_breach' AND payload->>'ticketId' = $1 AND NOT payload ? 'subject'`, ticketID).Scan(&breaches); err != nil || breaches != 1 {
		t.Fatalf("SLA breach notices = %d, %v", breaches, err)
	}

	// A report carries aggregates and is queued once per window.
	now := time.Now().UTC().Add(24 * time.Hour)
	for range 2 {
		if err = reports.QueueReport(ctx, operator.PeriodDay, now); err != nil {
			t.Fatal(err)
		}
	}
	var paidOrders, newCustomers int64
	if err = pool.QueryRow(ctx, `SELECT (payload->>'paidOrders')::bigint, (payload->>'newCustomers')::bigint FROM operator_notifications WHERE kind = 'report'`).Scan(&paidOrders, &newCustomers); err != nil {
		t.Fatalf("report: %v", err)
	}
	if paidOrders != 1 || newCustomers != 2 {
		t.Fatalf("report paidOrders=%d newCustomers=%d", paidOrders, newCustomers)
	}

	// A logged error lands in the log once per fingerprint and is announced
	// without its message; resolving it is audited.
	handler := errorlog.New(slog.DiscardHandler, "worker")
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	handler.Attach(runCtx, pool)
	logger := slog.New(handler)
	logger.Error("charge failed for order 42", "error", "provider timeout")
	logger.Error("charge failed for order 43", "error", "provider timeout")
	deadline := time.Now().Add(5 * time.Second)
	var occurrences int64
	for time.Now().Before(deadline) {
		_ = pool.QueryRow(ctx, `SELECT coalesce(max(occurrences), 0) FROM app_errors`).Scan(&occurrences)
		if occurrences == 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if occurrences != 2 {
		t.Fatalf("occurrences = %d", occurrences)
	}
	var leaked int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM operator_notifications WHERE kind = 'error' AND payload::text LIKE '%provider timeout%'`).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("an error notice must not carry the message: %d, %v", leaked, err)
	}
	if count := countNotices(ctx, t, harness, "error"); count != 1 {
		t.Fatalf("error notices = %d", count)
	}
	service, err := panelpg.New(pool, operationsKey, panelpg.Options{})
	if err != nil {
		t.Fatal(err)
	}
	errors, err := service.AppErrors(ctx, "", false)
	if err != nil || len(errors) != 1 || errors[0].Message != "charge failed for order 42: provider timeout" {
		t.Fatalf("error log = %+v, %v", errors, err)
	}
	actor := harness.operator(ctx, t, "errors@example.test")
	if _, err = service.ResolveAppError(ctx, errors[0].ID, actor); err != nil {
		t.Fatal(err)
	}
	if open, _ := service.AppErrors(ctx, "", false); len(open) != 0 {
		t.Fatalf("resolved errors are hidden by default: %+v", open)
	}

	// The bot's start notice names the version.
	if err = operator.AnnounceStart(ctx, pool, "bot", "1.2.3", time.Now()); err != nil {
		t.Fatal(err)
	}
	var version string
	if err = pool.QueryRow(ctx, `SELECT payload->>'version' FROM operator_notifications WHERE kind = 'infrastructure'`).Scan(&version); err != nil || version != "1.2.3" {
		t.Fatalf("start notice version = %q, %v", version, err)
	}
}

func countNotices(ctx context.Context, t *testing.T, harness *harness, kind string) int {
	t.Helper()
	var count int
	if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM operator_notifications WHERE kind = $1`, kind).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
