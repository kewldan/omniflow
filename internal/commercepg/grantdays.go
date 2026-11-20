package commercepg

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// GrantDays extends a customer's subscription on a plan version by a number
// of days inside the caller's transaction, exactly as a redeemed days code
// does: a zero-priced order, an entitlement that starts where the current one
// ends, and a fulfillment job in the same transaction. The key makes it
// idempotent through the order's idempotency key.
//
// It exists for grants decided outside the order flow — engagement prizes and
// poll rewards — so they extend access through the one path that already
// handles squads, traffic, and fulfillment.
func (store *Store) GrantDays(
	ctx context.Context, tx pgx.Tx, customerID, planVersionID, currency string, days int, key string,
) (time.Time, error) {
	userID, err := parseUUID(customerID)
	if err != nil {
		return time.Time{}, err
	}
	versionID, err := parseUUID(planVersionID)
	if err != nil {
		return time.Time{}, err
	}
	endsAt, _, err := store.grantRewardDays(ctx, tx, dbgen.New(tx), userID, versionID, currency,
		time.Duration(days)*24*time.Hour, false, key)
	return endsAt, err
}
