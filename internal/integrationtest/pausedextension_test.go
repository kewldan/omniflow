//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// TestAnExtensionBoughtWhilePausedResumesWithTheRemainingTime proves the pause
// accounting survives a change built on a paused entitlement: the new
// entitlement ends at now plus exactly the time the pause was preserving plus
// the period bought, and superseding the paused row closes its pause so a later
// resume cannot hand the days out again.
func TestAnExtensionBoughtWhilePausedResumesWithTheRemainingTime(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	store := commercepg.New(harness.pool, nil, testOptions())
	customerID := harness.customer(ctx, t)
	planVersionID := harness.catalog(ctx, t, "paused-plan", 0)

	first, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: planVersionID, Currency: "RUB",
		Operation: "purchase", IdempotencyKey: "paused-purchase",
	})
	if err != nil {
		t.Fatalf("purchase: %v", err)
	}
	var entitlementID pgtype.UUID
	if err := harness.pool.QueryRow(ctx, `SELECT id FROM entitlements WHERE order_id = $1`, first.ID).Scan(&entitlementID); err != nil {
		t.Fatalf("read entitlement: %v", err)
	}
	// Provisioned and then paused with eleven days left, a while ago.
	if _, err := harness.pool.Exec(ctx, `UPDATE entitlements
		SET status = 'paused', starts_at = now() - interval '40 days',
		    ends_at = now() - interval '9 days', paused_at = now() - interval '20 days'
		WHERE id = $1`, entitlementID); err != nil {
		t.Fatalf("pause: %v", err)
	}

	// An add-on has no remaining period to price against while paused.
	if _, err := store.CreateAddonOrder(ctx, commercepg.AddonOrderInput{
		CustomerID: customerID, Currency: "RUB", IdempotencyKey: "paused-addon",
		Addons: []commercepg.AddonSelection{{AddonVersionID: "00000000-0000-0000-0000-000000000001", Quantity: 1}},
	}); !errors.Is(err, commercepg.ErrNoActiveSubscription) {
		t.Fatalf("an add-on on a paused subscription must be refused, got %v", err)
	}

	extension, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: planVersionID, Currency: "RUB",
		Operation: "extension", IdempotencyKey: "paused-extension",
	})
	if err != nil {
		t.Fatalf("extension: %v", err)
	}
	var endsAt time.Time
	if err := harness.pool.QueryRow(ctx, `SELECT ends_at FROM entitlements WHERE order_id = $1`, extension.ID).Scan(&endsAt); err != nil {
		t.Fatalf("read new entitlement: %v", err)
	}
	// Eleven preserved days plus the thirty-day plan, from now.
	want := time.Now().Add(41 * 24 * time.Hour)
	if drift := endsAt.Sub(want); drift < -time.Minute || drift > time.Minute {
		t.Fatalf("the extension ends at %s, want about %s: the paused remainder was not carried", endsAt, want)
	}

	// Provisioning the new entitlement supersedes the paused one and closes its
	// pause, with the elapsed pause recorded.
	queries := dbgen.New(harness.pool)
	var newID pgtype.UUID
	if err := harness.pool.QueryRow(ctx, `SELECT id FROM entitlements WHERE order_id = $1`, extension.ID).Scan(&newID); err != nil {
		t.Fatalf("read new entitlement id: %v", err)
	}
	if err := queries.SupersedePreviousEntitlements(ctx, dbgen.SupersedePreviousEntitlementsParams{
		UserID: mustUUID(t, customerID), CurrentEntitlementID: newID, SubscriptionID: first.SubscriptionID,
	}); err != nil {
		t.Fatalf("supersede: %v", err)
	}
	old, err := queries.GetEntitlement(ctx, entitlementID)
	if err != nil {
		t.Fatalf("read old entitlement: %v", err)
	}
	if old.Status != "superseded" || old.PausedAt.Valid {
		t.Fatalf("the paused entitlement was not retired cleanly: %s paused_at=%v", old.Status, old.PausedAt)
	}
	if old.PausedSeconds < 19*24*3600 || old.PausedSeconds > 21*24*3600 {
		t.Fatalf("paused_seconds = %d, want about twenty days", old.PausedSeconds)
	}
	// A resume of the retired row is refused, so the days are never given twice.
	if _, err := queries.ResumeEntitlement(ctx, entitlementID); err == nil {
		t.Fatal("a superseded entitlement must not be resumable")
	}
}
