package commercepg

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// changePolicy reads the installation's plan-change switches inside the
// caller's transaction, so an order is priced and settled under the policy
// that was in force when it was.
//
// The migration seeds the row; a database restored without it reads as the
// default policy, which is what the code did before the switches existed.
func (store *Store) changePolicy(ctx context.Context, queries *dbgen.Queries) (commerce.ChangePolicy, error) {
	row, err := queries.GetSubscriptionPolicy(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return commerce.DefaultChangePolicy(), nil
	}
	if err != nil {
		return commerce.ChangePolicy{}, err
	}
	return ChangePolicyFrom(row), nil
}

// ChangePolicy reads the installation's plan-change switches.
func (store *Store) ChangePolicy(ctx context.Context) (commerce.ChangePolicy, error) {
	return store.changePolicy(ctx, dbgen.New(store.pool))
}

// resetsTraffic is the installation's traffic reset rule for a change that
// grants a period outside an order — a gift, an access code, a reward. A
// failed read falls back to the default rule rather than failing the grant:
// the reset is a courtesy to the customer, not a condition of the grant.
func (store *Store) resetsTraffic(ctx context.Context, queries *dbgen.Queries, operation string) bool {
	policy, err := store.changePolicy(ctx, queries)
	if err != nil {
		return commerce.ResetsTraffic(operation)
	}
	return policy.ResetsTraffic(operation)
}

// upgradePolicyFor is the upgrade policy a settling order is scheduled with.
// A move off a trial or a free plan follows the installation's rule for the
// time left on it; every other change follows the paid version's own policy.
func (store *Store) upgradePolicyFor(ctx context.Context, queries *dbgen.Queries, order dbgen.Order, versionPolicy string, policy commerce.ChangePolicy) (string, error) {
	if order.Operation != "upgrade" || policy.TrialRemainder == commerce.TrialRemainderPolicy {
		return versionPolicy, nil
	}
	kind, err := queries.GetHeldPlanKind(ctx, order.SubscriptionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return versionPolicy, nil
	}
	if err != nil {
		return "", err
	}
	if kind != "trial" && kind != "free" {
		return versionPolicy, nil
	}
	return policy.UpgradeFromTrial(versionPolicy), nil
}

// ChangePolicyFrom maps the stored row onto the domain policy.
func ChangePolicyFrom(row dbgen.SubscriptionPolicy) commerce.ChangePolicy {
	return commerce.ChangePolicy{
		UpgradesEnabled:          row.UpgradesEnabled,
		DowngradesEnabled:        row.DowngradesEnabled,
		ResetTrafficOnRenewal:    row.ResetTrafficOnRenewal,
		ResetTrafficOnPlanChange: row.ResetTrafficOnPlanChange,
		ResetDevicesOnRenewal:    row.ResetDevicesOnRenewal,
		TrialRemainder:           row.TrialRemainder,
	}
}
