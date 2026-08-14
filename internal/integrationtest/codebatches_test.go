//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/accesscode"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/panelpg"
)

// Wholesale code batches against a real database.
//
// Two properties need a container and neither can be checked in Go. Single
// redemption is an UPDATE predicate — only an `issued` code in a live batch
// matches, and the same statement writes the redemption — and revocation is a
// second predicate that must leave redeemed codes alone.

func TestABatchIssuesCodesThatAreStoredOnlyAsDigests(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service := newOperations(t, harness)
	actor := harness.operator(ctx, t, "codes@example.test")

	planVersion := seedPlanVersion(ctx, t, harness)
	generated, err := service.CreateCodeBatch(ctx, panelpg.CodeBatch{
		Reference: "reseller-acme", PlanVersionID: planVersion, Quantity: 25,
		UnitPriceMinor: 400, Currency: "USD",
	}, actor)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(generated.Codes) != 25 {
		t.Fatalf("%d codes returned, want 25", len(generated.Codes))
	}

	// Every code is distinct, and none of them is anywhere in the database.
	seen := map[string]bool{}
	for _, code := range generated.Codes {
		if seen[code] {
			t.Fatalf("the batch contains a duplicate code")
		}
		seen[code] = true

		var stored int
		if err := harness.pool.QueryRow(ctx,
			`SELECT count(*) FROM access_codes WHERE encode(code_hash, 'hex') = $1`, code,
		).Scan(&stored); err != nil {
			t.Fatalf("probe: %v", err)
		}
		if stored != 0 {
			t.Fatalf("a plaintext code appears in the table")
		}
	}

	// The panel's own view carries hints and never codes.
	codes, err := service.BatchCodes(ctx, generated.Batch.ID)
	if err != nil {
		t.Fatalf("list codes: %v", err)
	}
	if len(codes) != 25 {
		t.Fatalf("%d code rows, want 25", len(codes))
	}
	for _, code := range codes {
		if len(code.Hint) != 4 || code.Status != "issued" {
			t.Fatalf("code row is %+v", code)
		}
	}
}

func TestACodeRedeemsExactlyOnce(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service := newOperations(t, harness)
	store := commercepg.New(harness.pool, nil, testOptions())
	actor := harness.operator(ctx, t, "codes-redeem@example.test")

	planVersion := seedPlanVersion(ctx, t, harness)
	generated, err := service.CreateCodeBatch(ctx, panelpg.CodeBatch{
		Reference: "reseller-once", PlanVersionID: planVersion, Quantity: 2,
		UnitPriceMinor: 400, Currency: "USD",
	}, actor)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	customer := seedCustomer(ctx, t, harness, "redeemer@example.test")
	redeemed, err := store.RedeemAccessCode(ctx, generated.Codes[0], customer)
	if err != nil {
		t.Fatalf("redeem: %v", err)
	}
	if redeemed.EntitlementID == "" || !redeemed.EndsAt.After(time.Now()) {
		t.Fatalf("the redemption produced %+v", redeemed)
	}

	// The same code again, by the same customer and by another one.
	if _, err := store.RedeemAccessCode(ctx, generated.Codes[0], customer); err == nil {
		t.Fatal("a code was redeemed twice")
	}
	other := seedCustomer(ctx, t, harness, "second@example.test")
	if _, err := store.RedeemAccessCode(ctx, generated.Codes[0], other); err == nil {
		t.Fatal("a redeemed code was accepted from a different customer")
	}

	// A code that never existed is refused the same way, so the endpoint says
	// nothing about which codes exist.
	unknown, _, _ := accesscode.New()
	if _, err := store.RedeemAccessCode(ctx, unknown, other); err == nil {
		t.Fatal("an invented code was accepted")
	}

	// The zero-value order exists so the entitlement has the transaction every
	// entitlement has, and carries no money: nobody paid at redemption time.
	var operation string
	var subtotal, paid int64
	if err := harness.pool.QueryRow(ctx, `
		SELECT o.operation, o.subtotal_minor, o.paid_minor
		FROM orders o JOIN entitlements e ON e.order_id = o.id
		WHERE e.id = $1`, redeemed.EntitlementID,
	).Scan(&operation, &subtotal, &paid); err != nil {
		t.Fatalf("read order: %v", err)
	}
	if operation != "code" || subtotal != 0 || paid != 0 {
		t.Fatalf("the redemption order is %s with %d/%d; a wholesale redemption "+
			"must not put revenue in the sales report", operation, subtotal, paid)
	}
}

func TestRevokingKillsTheRemainderAndSparesTheRedeemed(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service := newOperations(t, harness)
	store := commercepg.New(harness.pool, nil, testOptions())
	actor := harness.operator(ctx, t, "codes-revoke@example.test")

	planVersion := seedPlanVersion(ctx, t, harness)
	generated, err := service.CreateCodeBatch(ctx, panelpg.CodeBatch{
		Reference: "reseller-leak", PlanVersionID: planVersion, Quantity: 5,
		UnitPriceMinor: 400, Currency: "USD",
	}, actor)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	customer := seedCustomer(ctx, t, harness, "early@example.test")
	if _, err := store.RedeemAccessCode(ctx, generated.Codes[0], customer); err != nil {
		t.Fatalf("redeem: %v", err)
	}

	// A revocation with no reason is refused: this is the action taken when a
	// list leaks, and the reason is what somebody reads six months later.
	if _, err := service.RevokeCodeBatch(ctx, generated.Batch.ID, "", actor); !errors.Is(
		err, panelpg.ErrValidaton,
	) {
		t.Fatalf("a reasonless revocation was accepted: %v", err)
	}

	revoked, err := service.RevokeCodeBatch(
		ctx, generated.Batch.ID, "the distributor published the list", actor)
	if err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if revoked != 4 {
		t.Fatalf("%d codes revoked, want 4 — the redeemed one must be spared", revoked)
	}

	// The remaining codes no longer redeem.
	late := seedCustomer(ctx, t, harness, "late@example.test")
	if _, err := store.RedeemAccessCode(ctx, generated.Codes[1], late); err == nil {
		t.Fatal("a revoked code was redeemed")
	}

	// And the subscription the redeemed code produced is untouched: somebody is
	// using it, and taking it back is a different decision.
	var entitlements int
	if err := harness.pool.QueryRow(ctx,
		`SELECT count(*) FROM entitlements WHERE user_id = $1::uuid AND status <> 'superseded'`,
		customer,
	).Scan(&entitlements); err != nil {
		t.Fatalf("probe: %v", err)
	}
	if entitlements != 1 {
		t.Fatalf("the redeemed customer has %d entitlements after a revocation", entitlements)
	}
}

func seedPlanVersion(ctx context.Context, t *testing.T, harness *harness) string {
	t.Helper()
	var planVersion string
	if err := harness.pool.QueryRow(ctx, `
		WITH plan AS (
			INSERT INTO plans (code, kind) VALUES ('wholesale-plan', 'one_time') RETURNING id
		), version AS (
			INSERT INTO plan_versions (plan_id, version, billing_period, duration_seconds)
			SELECT id, 1, 'month', 2592000 FROM plan
			RETURNING id
		)
		INSERT INTO plan_prices (plan_version_id, currency, amount_minor)
		SELECT id, 'USD', 1000 FROM version
		RETURNING plan_version_id`,
	).Scan(&planVersion); err != nil {
		t.Fatalf("seed plan version: %v", err)
	}
	return planVersion
}
