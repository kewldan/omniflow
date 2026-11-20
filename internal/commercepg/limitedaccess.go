package commercepg

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"github.com/omniflow/omniflow/internal/commerce"
	databaseutil "github.com/omniflow/omniflow/internal/database"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// restoreLimitedAccess ends a subscription's open limited access period when
// money is paid on it, in the settling transaction: the customer paid, so the
// full plan comes back. A period opened on expiry and replaced by the
// entitlement this order creates needs nothing more — that entitlement's own
// `create` pushes the plan. Anything else queues one `restore_access` that
// pushes the entitlement's own state again, keyed on the period.
func (store *Store) restoreLimitedAccess(ctx context.Context, tx pgx.Tx, queries *dbgen.Queries, order dbgen.Order, correlationID string) error {
	if !order.SubscriptionID.Valid {
		return nil
	}
	restored, err := queries.RestoreLimitedAccess(ctx, order.SubscriptionID)
	if err != nil {
		return err
	}
	for _, session := range restored {
		if session.Trigger == "expiry" && commerce.SubscriptionOperation(order.Operation) {
			continue
		}
		entitlement, getErr := queries.GetEntitlement(ctx, session.EntitlementID)
		if getErr != nil {
			return getErr
		}
		desired, marshalErr := json.Marshal(map[string]any{
			"effectiveAt": store.clock().UTC(), "endsAt": entitlement.EndsAt.Time,
			"trafficAllowanceBytes": nullableInt8(entitlement.TrafficAllowanceBytes),
			"deviceLimit":           nullableInt4(entitlement.DeviceLimit),
			"squadIds":              databaseutil.UUIDStrings(entitlement.RemnawaveSquadIds),
			"sessionId":             uuidString(session.ID),
		})
		if marshalErr != nil {
			return marshalErr
		}
		operation, createErr := queries.CreateFulfillmentOperation(ctx, dbgen.CreateFulfillmentOperationParams{
			EntitlementID: entitlement.ID, Operation: "restore_access",
			IdempotencyKey: "limited:" + uuidString(session.ID) + ":restore", CorrelationID: correlationID,
			DesiredState: desired,
		})
		if createErr != nil {
			return createErr
		}
		if store.enqueue != nil {
			if err = store.enqueue(ctx, tx, uuidString(operation.ID)); err != nil {
				return err
			}
		}
	}
	return nil
}
