package fulfillment

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/remnawave"
)

// close is the Remnawave half of a subscription the customer closed: its
// user is disabled or deleted, as the installation chose when it was closed.
//
// It runs against an entitlement that closing retired on purpose, which is the
// one case a retired entitlement's operation is not obsolete. A subscription
// that never reached Remnawave has nothing to close. Both calls treat a user
// already in the asked-for state as success, so a retried run is harmless.
func (worker *Worker) close(ctx context.Context, remoteID int64, mode string) (remnawave.User, error) {
	if remoteID == 0 {
		return remnawave.User{}, nil
	}
	if mode == "delete" {
		if err := worker.provisioner.DeleteUser(ctx, remoteID); err != nil {
			return remnawave.User{}, err
		}
		return remnawave.User{ID: remoteID, Status: "DELETED"}, nil
	}
	if err := worker.provisioner.DisableUser(ctx, remoteID); err != nil {
		return remnawave.User{}, err
	}
	return remnawave.User{ID: remoteID, Status: "DISABLED"}, nil
}

// recordClosed records a finished close. The entitlement stays retired and
// the subscription closed; nothing observed is written back onto either, and
// the operation and its history say what was done.
func (worker *Worker) recordClosed(ctx context.Context, tx pgx.Tx, queries *dbgen.Queries, operation dbgen.FulfillmentOperation, claimed claim) error {
	if _, err := queries.UpdateFulfillmentOperation(ctx, dbgen.UpdateFulfillmentOperationParams{
		OperationID: operation.ID, Status: "succeeded", AttemptCount: operation.AttemptCount,
		NextAttemptAt: pgtype.Timestamptz{Time: worker.clock(), Valid: true},
	}); err != nil {
		return err
	}
	if _, err := queries.InsertFulfillmentHistory(ctx, dbgen.InsertFulfillmentHistoryParams{
		OperationID: operation.ID, Status: "succeeded", CorrelationID: operation.CorrelationID,
		RequestSummary:  []byte(fmt.Sprintf(`{"closeMode":%q}`, claimed.desired.CloseMode)),
		ResponseSummary: []byte(fmt.Sprintf(`{"remnawaveUserId":%d}`, claimed.remoteID)),
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
