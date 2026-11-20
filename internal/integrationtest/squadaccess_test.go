//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/fulfillment"
)

const grantedSquad = "44444444-4444-4444-4444-444444444444"

func (panel *memoryRemnawave) squadsOf(id int64) []string {
	panel.mu.Lock()
	defer panel.mu.Unlock()
	squads := []string{}
	for _, squad := range panel.users[id].ActiveInternalSquads {
		squads = append(squads, squad.UUID)
	}
	return squads
}

// An operator opens a squad for some hours: the grant reaches Remnawave on top
// of the plan's squads, a reconcile keeps it, a repeated request is the same
// grant, and when the hours are up the scheduled revoke takes it back. A promo
// code opens one the same way.
func TestTemporarySquadAccessIsGrantedAndTakenBack(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	panel := newMemoryRemnawave()
	worker := fulfillment.NewWorker(harness.pool, panel)
	service := fulfillment.NewService(harness.pool, nil)
	scheduler := fulfillment.NewScheduler(harness.pool, &recordingInserter{})
	customerID := harness.customer(ctx, t)
	planVersionID := harness.catalog(ctx, t, "grant-plan", 0)
	if _, err := harness.pool.Exec(ctx, `UPDATE plan_versions SET remnawave_squad_ids = ARRAY[$2::uuid] WHERE id = $1::uuid`, planVersionID, planSquad); err != nil {
		t.Fatalf("plan squads: %v", err)
	}
	bought, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: planVersionID, Currency: "RUB", Operation: "purchase", IdempotencyKey: "grant-buy",
	})
	if err != nil {
		t.Fatalf("buy: %v", err)
	}
	harness.fulfillAll(ctx, t, worker)
	var remoteID int64
	if err = harness.pool.QueryRow(ctx, `SELECT remnawave_user_id FROM entitlements WHERE order_id = $1::uuid`, uuidText(bought.ID)).Scan(&remoteID); err != nil {
		t.Fatalf("remote user: %v", err)
	}
	var customer pgtype.UUID
	_ = customer.Scan(customerID)
	input := fulfillment.SquadGrantInput{
		CustomerID: customer, SubscriptionID: bought.SubscriptionID, SquadID: grantedSquad, Hours: 2,
		Source: "operator", Reason: "support", IdempotencyKey: "grant-1",
	}
	if _, err = service.GrantSquadAccess(ctx, fulfillment.SquadGrantInput{CustomerID: customer, SubscriptionID: bought.SubscriptionID, SquadID: grantedSquad, Hours: 721, Source: "operator", IdempotencyKey: "grant-too-long"}); !errors.Is(err, fulfillment.ErrSquadGrantInvalid) {
		t.Fatalf("721 hours: %v", err)
	}
	grant, err := service.GrantSquadAccess(ctx, input)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	again, err := service.GrantSquadAccess(ctx, input)
	if err != nil || again.ID != grant.ID {
		t.Fatalf("a repeated grant = %v, %v", again.ID, err)
	}
	grantKey := "squad-grant:" + uuidText(grant.ID)
	harness.runOperation(ctx, t, worker, grantKey+":grant")
	if squads := panel.squadsOf(remoteID); !slices.Contains(squads, grantedSquad) || !slices.Contains(squads, planSquad) {
		t.Fatalf("squads with the grant = %v", squads)
	}
	if _, err = harness.pool.Exec(ctx, `UPDATE entitlements SET reconciled_at = NULL`); err != nil {
		t.Fatalf("make it due: %v", err)
	}
	if err = scheduler.Schedule(ctx); err != nil {
		t.Fatalf("schedule: %v", err)
	}
	harness.runOperation(ctx, t, worker, harness.latestKey(ctx, t, "reconcile:%"))
	if squads := panel.squadsOf(remoteID); !slices.Contains(squads, grantedSquad) {
		t.Fatalf("a reconcile dropped the grant: %v", squads)
	}

	// The hours run out.
	if _, err = harness.pool.Exec(ctx, `UPDATE squad_access_grants SET starts_at = now() - interval '3 hours', ends_at = now() - interval '1 hour'`); err != nil {
		t.Fatalf("expire the grant: %v", err)
	}
	if _, err = harness.pool.Exec(ctx, `UPDATE fulfillment_operations SET desired_state = jsonb_set(desired_state, '{effectiveAt}', to_jsonb(now() - interval '1 hour'))
		WHERE idempotency_key = $1`, grantKey+":revoke"); err != nil {
		t.Fatalf("expire the grant: %v", err)
	}
	harness.runOperation(ctx, t, worker, grantKey+":revoke")
	if squads := panel.squadsOf(remoteID); slices.Contains(squads, grantedSquad) || !slices.Contains(squads, planSquad) {
		t.Fatalf("squads after the hours = %v", squads)
	}
	var status string
	if err = harness.pool.QueryRow(ctx, `SELECT status FROM squad_access_grants WHERE id = $1`, grant.ID).Scan(&status); err != nil || status != "ended" {
		t.Fatalf("grant status = %s, %v", status, err)
	}

	// A promo code opens one too.
	var promotionID string
	if err = harness.pool.QueryRow(ctx, `INSERT INTO promotions (code, kind, value, per_customer_limit, eligibility, active, squad_id)
		VALUES ('squad-promo', 'squad_access', 6, 1, '{}', true, $1::uuid) RETURNING id::text`, grantedSquad).Scan(&promotionID); err != nil {
		t.Fatalf("promotion: %v", err)
	}
	if _, err = harness.pool.Exec(ctx, `INSERT INTO promo_codes (promotion_id, normalized_code, active) VALUES ($1::uuid, 'OPENSERVER', true)`, promotionID); err != nil {
		t.Fatalf("code: %v", err)
	}
	reward, err := store.RedeemRewardPromo(ctx, "OPENSERVER", customerID)
	if err != nil || reward.Kind != commercepg.RewardSquadAccess || reward.Hours != 6 {
		t.Fatalf("redeem = %+v, %v", reward, err)
	}
	var promoGrants int
	if err = harness.pool.QueryRow(ctx, `SELECT count(*) FROM squad_access_grants WHERE source = 'promo' AND status = 'active'`).Scan(&promoGrants); err != nil || promoGrants != 1 {
		t.Fatalf("promo grants = %d, %v", promoGrants, err)
	}
}
