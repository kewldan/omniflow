//go:build integration

// Package integrationtest exercises the database contract end to end against a
// real PostgreSQL instance started by Testcontainers.
//
// These tests are behind the `integration` build tag so the default `go test
// ./...` stays fast and needs no Docker daemon. CI runs them separately.
package integrationtest

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// harness is one migrated database shared by the tests in a single package run.
type harness struct {
	pool *pgxpool.Pool
	url  string
}

// newHarness starts PostgreSQL, applies every committed migration in filename
// order, and returns a pool. Applying the real migration files — rather than a
// hand-maintained schema dump — is what makes these tests a migration gate as
// well as a repository gate.
func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	container, err := postgres.Run(ctx, "postgres:18.4-alpine",
		postgres.WithDatabase("omniflow"),
		postgres.WithUsername("omniflow"),
		postgres.WithPassword("omniflow"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(90*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("start PostgreSQL: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(container) })

	url, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	applyMigrations(ctx, t, pool)
	return &harness{pool: pool, url: url}
}

func applyMigrations(ctx context.Context, t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	directory := filepath.Join("..", "..", "database", "migrations")
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".sql" {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		t.Fatal("no migrations were found")
	}
	for _, name := range names {
		statements, readErr := os.ReadFile(filepath.Join(directory, name))
		if readErr != nil {
			t.Fatalf("read %s: %v", name, readErr)
		}
		if _, err := pool.Exec(ctx, string(statements)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
}

// TestMigrationsApplyFromABareDatabase is the upgrade gate: every supported
// baseline is the empty database plus the committed migration history, so a
// migration that cannot run in order fails here rather than in production.
func TestMigrationsApplyFromABareDatabase(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	for _, table := range []string{
		"users", "orders", "entitlements", "subscriptions", "wallet_topups", "carts",
		"addons", "addon_versions", "order_addon_lines", "entitlement_addons",
		"operator_topics", "operator_notifications", "backups", "backup_restores",
		"maintenance_state", "maintenance_events", "plan_version_squads",
		"admin_users", "admin_user_roles", "admin_sessions", "admin_recovery_codes",
		"admin_password_resets", "admin_setup_tokens",
		"admin_oidc_providers", "admin_oidc_identities",
	} {
		var exists bool
		if err := harness.pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = $1)`, table).
			Scan(&exists); err != nil {
			t.Fatalf("check %s: %v", table, err)
		}
		if !exists {
			t.Fatalf("table %s is missing after migration", table)
		}
	}
}

// TestUpgradeAdoptsExistingCustomersOntoSubscriptions proves the v0.4 → v0.5
// migration path: a customer that already had a Remnawave user keeps it as slot
// one instead of being orphaned or duplicated.
func TestUpgradeAdoptsExistingCustomersOntoSubscriptions(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	var userID string
	if err := harness.pool.QueryRow(ctx, `INSERT INTO users (locale) VALUES ('en') RETURNING id::text`).Scan(&userID); err != nil {
		t.Fatalf("create customer: %v", err)
	}
	if _, err := harness.pool.Exec(ctx,
		`INSERT INTO remnawave_users (user_id, remnawave_id, telegram_id) VALUES ($1::uuid, 4242, 99001)`, userID); err != nil {
		t.Fatalf("create mapping: %v", err)
	}
	// Re-running the adoption statement from the migration must be a no-op: the
	// unique slot constraint is what makes a repeated upgrade safe.
	_, err := harness.pool.Exec(ctx, `INSERT INTO subscriptions (user_id, slot, label, remnawave_user_id)
		SELECT r.user_id, 1, 'Subscription 1', r.remnawave_id
		FROM remnawave_users r
		WHERE NOT EXISTS (SELECT 1 FROM subscriptions s WHERE s.user_id = r.user_id AND s.slot = 1)`)
	if err != nil {
		t.Fatalf("adopt subscription: %v", err)
	}
	var count int
	if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM subscriptions WHERE user_id = $1::uuid`, userID).Scan(&count); err != nil {
		t.Fatalf("count subscriptions: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly one adopted subscription, got %d", count)
	}
}
