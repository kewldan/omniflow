//go:build integration

package integrationtest

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// Pausing a subscription against a real database.
//
// The feature is one sentence — "stop the access without spending the remaining
// days" — and every property that makes it true lives in SQL: the guard that
// stops a double pause is a predicate, the arithmetic that gives the time back
// is an UPDATE, and the pairing of `status` and `paused_at` is a table
// constraint. None of it can be checked in Go.

func TestAPauseGivesBackExactlyTheTimeItTook(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)

	entitlement := seedEntitlement(ctx, t, harness, 30*24*time.Hour)
	before := entitlementEndsAt(ctx, t, harness, entitlement)

	queries := harnessQueries(harness)
	paused, err := queries.PauseEntitlement(ctx, mustUUID(t, entitlement))
	if err != nil {
		t.Fatalf("pause: %v", err)
	}
	if paused.Status != "paused" || !paused.PausedAt.Valid {
		t.Fatalf("the entitlement is %+v", paused)
	}
	// The expiry does not move when the pause begins. It is the clock that
	// stops, not the date.
	if !paused.EndsAt.Time.Equal(before) {
		t.Fatalf("pausing moved the expiry from %s to %s", before, paused.EndsAt.Time)
	}

	// Let a measurable amount of time pass.
	time.Sleep(1100 * time.Millisecond)

	resumed, err := queries.ResumeEntitlement(ctx, mustUUID(t, entitlement))
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if resumed.Status != "active" || resumed.PausedAt.Valid {
		t.Fatalf("the entitlement is %+v", resumed)
	}

	// The expiry moved forward by the length of the pause, and `paused_seconds`
	// records the same amount — which is what makes an entitlement whose expiry
	// sits later than its order paid for explainable by its own columns rather
	// than looking like a mistake.
	moved := resumed.EndsAt.Time.Sub(before)
	if moved < time.Second || moved > 10*time.Second {
		t.Fatalf("the expiry moved by %s, which does not match a pause of about a second", moved)
	}
	if resumed.PausedSeconds < 1 {
		t.Fatalf("paused_seconds is %d after a pause of about a second", resumed.PausedSeconds)
	}
}

// The guard is a predicate rather than a Go check, so two operators pressing the
// button at the same moment produce one pause and one refusal. A second pause
// would reset the instant the first recorded and silently swallow the days
// between them.
func TestPausingTwiceIsRefusedByTheDatabase(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	queries := harnessQueries(harness)

	entitlement := seedEntitlement(ctx, t, harness, 30*24*time.Hour)
	id := mustUUID(t, entitlement)

	if _, err := queries.PauseEntitlement(ctx, id); err != nil {
		t.Fatalf("first pause: %v", err)
	}
	if _, err := queries.PauseEntitlement(ctx, id); err == nil {
		t.Fatal("a second pause was accepted; the first pause instant would have been reset")
	}

	// And resuming twice must not hand out the elapsed time again.
	if _, err := queries.ResumeEntitlement(ctx, id); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if _, err := queries.ResumeEntitlement(ctx, id); err == nil {
		t.Fatal("a second resume was accepted; the customer would be given the pause twice")
	}
}

// The table refuses to hold `status` and `paused_at` apart, which is what stops
// a reconcile that mapped a disabled Remnawave user back to `disabled` from
// silently dropping the record that time was owed.
func TestTheTableRefusesAHalfPause(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	entitlement := seedEntitlement(ctx, t, harness, 30*24*time.Hour)

	for name, statement := range map[string]string{
		"paused without an instant": `UPDATE entitlements SET status = 'paused' WHERE id = $1`,
		"an instant without paused": `UPDATE entitlements SET paused_at = now() WHERE id = $1`,
	} {
		if _, err := harness.pool.Exec(ctx, statement, entitlement); err == nil {
			t.Errorf("%s was accepted; the pairing constraint is not holding", name)
		}
	}

	// And the pairing survives the write the reconciler actually makes: moving
	// a paused entitlement to any other status has to clear the instant, which
	// the query does rather than leaving it to a caller to remember.
	if _, err := harness.pool.Exec(ctx,
		`UPDATE entitlements SET status = 'paused', paused_at = now() WHERE id = $1`, entitlement,
	); err != nil {
		t.Fatalf("pause: %v", err)
	}
	queries := harnessQueries(harness)
	if _, err := queries.UpdateEntitlementObservedState(ctx, observedStateParams(t, entitlement, "active")); err != nil {
		t.Fatalf("the reconciler's write failed on a paused entitlement: %v", err)
	}
}

// Helpers. The entitlement is written directly rather than bought, because what
// is under test is the pause arithmetic and a real purchase would need a plan, a
// provider, and a Remnawave user none of these assertions look at.

func seedEntitlement(
	ctx context.Context, t *testing.T, harness *harness, remaining time.Duration,
) string {
	t.Helper()
	customer := seedCustomer(ctx, t, harness, "pause@example.test")

	var planVersion string
	if err := harness.pool.QueryRow(ctx, `
		WITH plan AS (
			INSERT INTO plans (code, kind) VALUES ('pause-plan', 'one_time') RETURNING id
		)
		INSERT INTO plan_versions (plan_id, version, billing_period, duration_seconds)
		SELECT id, 1, 'month', 2592000 FROM plan
		RETURNING id`,
	).Scan(&planVersion); err != nil {
		t.Fatalf("seed plan: %v", err)
	}

	var order string
	if err := harness.pool.QueryRow(ctx, `
		INSERT INTO orders (
			user_id, state, operation, currency, subtotal_minor, external_minor,
			paid_minor, idempotency_key, paid_at
		) VALUES ($1, 'paid', 'purchase', 'USD', 1000, 1000, 1000, 'seed-pause', now())
		RETURNING id`, customer,
	).Scan(&order); err != nil {
		t.Fatalf("seed order: %v", err)
	}

	var entitlement string
	if err := harness.pool.QueryRow(ctx, `
		INSERT INTO entitlements (
			user_id, order_id, plan_version_id, status, starts_at, ends_at, remnawave_user_id
		) VALUES ($1, $2, $3, 'active', now(), now() + $4::interval, 4242)
		RETURNING id`,
		customer, order, planVersion, remaining.String(),
	).Scan(&entitlement); err != nil {
		t.Fatalf("seed entitlement: %v", err)
	}
	return entitlement
}

func entitlementEndsAt(
	ctx context.Context, t *testing.T, harness *harness, entitlement string,
) time.Time {
	t.Helper()
	var endsAt time.Time
	if err := harness.pool.QueryRow(ctx,
		`SELECT ends_at FROM entitlements WHERE id = $1`, entitlement,
	).Scan(&endsAt); err != nil {
		t.Fatalf("read entitlement: %v", err)
	}
	return endsAt
}

func harnessQueries(harness *harness) *dbgen.Queries {
	return dbgen.New(harness.pool)
}

func mustUUID(t *testing.T, value string) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := id.Scan(value); err != nil {
		t.Fatalf("parse %q: %v", value, err)
	}
	return id
}

func observedStateParams(
	t *testing.T, entitlement, status string,
) dbgen.UpdateEntitlementObservedStateParams {
	t.Helper()
	return dbgen.UpdateEntitlementObservedStateParams{
		EntitlementID: mustUUID(t, entitlement),
		Status:        status,
		ObservedState: []byte(`{}`),
	}
}
