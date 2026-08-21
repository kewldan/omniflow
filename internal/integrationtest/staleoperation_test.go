//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"testing"

	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/fulfillment"
	"github.com/omniflow/omniflow/internal/remnawave"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// untouchableProvisioner fails every call. It stands in for Remnawave in a
// test whose whole point is that Remnawave is never asked anything.
type untouchableProvisioner struct{ t *testing.T }

func (p untouchableProvisioner) fail(call string) error {
	p.t.Errorf("Remnawave was called (%s) for an operation that must be cancelled without it", call)
	return errors.New("unexpected call")
}

func (p untouchableProvisioner) User(context.Context, int64) (remnawave.User, error) {
	return remnawave.User{}, p.fail("User")
}

func (p untouchableProvisioner) UserByUsername(context.Context, string) (remnawave.User, error) {
	return remnawave.User{}, p.fail("UserByUsername")
}

func (p untouchableProvisioner) CreateUser(context.Context, remnawave.ProvisionUser) (remnawave.User, error) {
	return remnawave.User{}, p.fail("CreateUser")
}

func (p untouchableProvisioner) UpdateUser(context.Context, int64, remnawave.ProvisionUser) (remnawave.User, error) {
	return remnawave.User{}, p.fail("UpdateUser")
}

func (p untouchableProvisioner) EnableUser(context.Context, int64) error { return p.fail("EnableUser") }
func (p untouchableProvisioner) DisableUser(context.Context, int64) error {
	return p.fail("DisableUser")
}
func (p untouchableProvisioner) ResetUserTraffic(context.Context, int64) error {
	return p.fail("ResetUserTraffic")
}

// TestAnOperationOnASupersededEntitlementIsCancelledUntouched proves the
// guard against a stale reconcile: once a newer entitlement has retired the
// one an operation was written for, the operation is closed with a reason and
// Remnawave is never asked anything — so the old expiry can never be pushed
// over the new one.
func TestAnOperationOnASupersededEntitlementIsCancelledUntouched(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	store := commercepg.New(harness.pool, nil, testOptions())
	customerID := harness.customer(ctx, t)
	planVersionID := harness.catalog(ctx, t, "stale-plan", 0)

	if _, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: planVersionID, Currency: "RUB",
		Operation: "purchase", IdempotencyKey: "stale-order",
	}); err != nil {
		t.Fatalf("create order: %v", err)
	}
	var operationID string
	if err := harness.pool.QueryRow(ctx, `SELECT id::text FROM fulfillment_operations`).Scan(&operationID); err != nil {
		t.Fatalf("read operation: %v", err)
	}
	// A newer entitlement of the same subscription has been provisioned since.
	if _, err := harness.pool.Exec(ctx, `UPDATE entitlements SET status = 'superseded'`); err != nil {
		t.Fatalf("supersede: %v", err)
	}

	worker := fulfillment.NewWorker(harness.pool, untouchableProvisioner{t: t})
	job := &river.Job[fulfillment.JobArgs]{JobRow: &rivertype.JobRow{Attempt: 1}, Args: fulfillment.JobArgs{OperationID: operationID}}
	if err := worker.Work(ctx, job); err != nil {
		t.Fatalf("work: %v", err)
	}

	var status, reason string
	if err := harness.pool.QueryRow(ctx, `SELECT status, COALESCE(last_error_code, '') FROM fulfillment_operations WHERE id = $1::uuid`, operationID).Scan(&status, &reason); err != nil {
		t.Fatalf("read operation: %v", err)
	}
	if status != "cancelled" || reason != "entitlement_superseded" {
		t.Fatalf("operation ended as %s/%s, want cancelled/entitlement_superseded", status, reason)
	}
	var entitlementStatus string
	if err := harness.pool.QueryRow(ctx, `SELECT status FROM entitlements`).Scan(&entitlementStatus); err != nil {
		t.Fatalf("read entitlement: %v", err)
	}
	if entitlementStatus != "superseded" {
		t.Fatalf("the retired entitlement was resurrected as %q", entitlementStatus)
	}
	// Running the job again is a no-op: a cancelled operation commits and does
	// nothing, and still asks Remnawave nothing.
	if err := worker.Work(ctx, job); err != nil {
		t.Fatalf("second run: %v", err)
	}
}
