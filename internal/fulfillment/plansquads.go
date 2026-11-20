package fulfillment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	databaseutil "github.com/omniflow/omniflow/internal/database"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// ApplyPlanSquads moves every live subscription on a plan onto the squads of
// the plan's newest live version, as Bedolaga's tariff squad sync does when an
// operator changes a tariff's servers.
//
// Only the plan's own squads are swapped; a customer's checkout choice and a
// squad add-on are kept. Every changed entitlement gets a `set_squads`
// operation in the same transaction. The operation's key is the entitlement
// plus the squad set it pushes, so pressing the button twice queues nothing
// new. A paused entitlement keeps the new set in its row, and its resume
// pushes it. It returns how many entitlements were moved.
func (service *Service) ApplyPlanSquads(ctx context.Context, planID, correlationID string) (int, error) {
	id, err := parseUUID(planID)
	if err != nil {
		return 0, err
	}
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := dbgen.New(tx)
	moved, err := queries.ApplyPlanSquadsToEntitlements(ctx, id)
	if err != nil {
		return 0, err
	}
	now := service.clock().UTC()
	for _, entitlement := range moved {
		if entitlement.Status == "paused" {
			continue
		}
		squads := databaseutil.UUIDStrings(entitlement.RemnawaveSquadIds)
		desired, marshalErr := json.Marshal(map[string]any{
			"effectiveAt": now, "endsAt": entitlement.EndsAt.Time,
			"trafficAllowanceBytes": nullableInt8(entitlement.TrafficAllowanceBytes),
			"deviceLimit":           nullableInt4(entitlement.DeviceLimit), "squadIds": squads,
		})
		if marshalErr != nil {
			return 0, marshalErr
		}
		operation, opErr := queries.CreateFulfillmentOperation(ctx, dbgen.CreateFulfillmentOperationParams{
			EntitlementID: entitlement.ID, Operation: "set_squads",
			IdempotencyKey: "plan-squads:" + uuidString(entitlement.ID) + ":" + squadSetDigest(squads),
			CorrelationID:  correlationID, DesiredState: desired,
		})
		if opErr != nil {
			return 0, opErr
		}
		if operation.Status == "succeeded" || operation.Status == "cancelled" {
			continue
		}
		if _, err = service.client.InsertTx(ctx, tx, JobArgs{OperationID: uuidString(operation.ID)}, InsertOpts()); err != nil {
			return 0, err
		}
	}
	return len(moved), tx.Commit(ctx)
}

// squadSetDigest names a squad set independently of its order.
func squadSetDigest(squads []string) string {
	sorted := append([]string(nil), squads...)
	sort.Strings(sorted)
	sum := sha256.Sum256([]byte(strings.Join(sorted, ",")))
	return hex.EncodeToString(sum[:8])
}
