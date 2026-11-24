package accountcheckout

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// heldVersion returns the plan version a subscription's latest entitlement on
// this plan was bought at, or "" when it has none.
//
// An extension keeps what the customer bought: when the operator publishes a
// new version — fewer devices, say — a customer extending from the web store
// stays on their own version, as automatic renewal and the bot's "Extend"
// already do. The new version is for new customers.
func (store *Store) heldVersion(ctx context.Context, subscriptionID, planID string) (string, error) {
	var version string
	err := store.pool.QueryRow(ctx, `SELECT e.plan_version_id::text
		FROM entitlements e
		JOIN plan_versions v ON v.id = e.plan_version_id
		WHERE e.subscription_id = $1::uuid AND v.plan_id = $2::uuid
		  AND e.status <> 'failed'
		ORDER BY e.ends_at DESC LIMIT 1`, subscriptionID, planID).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return version, err
}
