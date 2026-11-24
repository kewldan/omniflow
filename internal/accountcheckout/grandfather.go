package accountcheckout

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// heldVersion returns the version a subscription's latest entitlement on the
// chosen version's plan was bought at, while it still has a price in the
// currency, or "" when there is none.
//
// An extension keeps what the customer bought: when the operator publishes a
// new version — fewer devices, say — a customer extending from the web store
// or the bot's plan list stays on their own version, as automatic renewal and
// the bot's "Extend" button already do. The new version is for new customers.
func heldVersion(ctx context.Context, querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, subscriptionID, chosenVersionID, currency string) (string, error) {
	var version string
	err := querier.QueryRow(ctx, `SELECT e.plan_version_id::text
		FROM entitlements e
		JOIN plan_versions held ON held.id = e.plan_version_id
		JOIN plan_versions chosen ON chosen.id = $2::uuid AND chosen.plan_id = held.plan_id
		WHERE e.subscription_id = $1::uuid AND e.status <> 'failed'
		  AND EXISTS (SELECT 1 FROM plan_prices pp WHERE pp.plan_version_id = held.id AND pp.currency = $3)
		ORDER BY e.ends_at DESC LIMIT 1`, subscriptionID, chosenVersionID, currency).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return version, err
}
