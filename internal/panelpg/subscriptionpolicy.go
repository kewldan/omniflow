package panelpg

import (
	"context"
	"time"

	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// SubscriptionPolicy is the installation-wide half of what a paid change does
// to a subscription. A plan version's own upgrade and downgrade policies still
// apply beneath it.
type SubscriptionPolicy struct {
	UpgradesEnabled          bool      `json:"upgradesEnabled"`
	DowngradesEnabled        bool      `json:"downgradesEnabled"`
	ResetTrafficOnRenewal    bool      `json:"resetTrafficOnRenewal"`
	ResetTrafficOnPlanChange bool      `json:"resetTrafficOnPlanChange"`
	ResetDevicesOnRenewal    bool      `json:"resetDevicesOnRenewal"`
	TrialRemainder           string    `json:"trialRemainder"`
	UpdatedAt                time.Time `json:"updatedAt"`
}

var validTrialRemainder = map[string]bool{"policy": true, "carry": true, "reset": true}

// SubscriptionPolicy reads the installation's plan-change switches.
func (service *Service) SubscriptionPolicy(ctx context.Context) (SubscriptionPolicy, error) {
	row, err := service.queries().GetSubscriptionPolicy(ctx)
	if err != nil {
		return SubscriptionPolicy{}, notFound(err)
	}
	return subscriptionPolicyFrom(row), nil
}

// SaveSubscriptionPolicy stores the switches. A change applies to orders
// priced after it; an order already open keeps the rules it was priced under
// only as far as its price goes, because settlement reads the switches again.
func (service *Service) SaveSubscriptionPolicy(
	ctx context.Context, policy SubscriptionPolicy, actor Actor,
) (SubscriptionPolicy, error) {
	if !validTrialRemainder[policy.TrialRemainder] {
		return SubscriptionPolicy{}, ErrValidaton
	}
	var saved SubscriptionPolicy
	err := service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, err := queries.UpdateSubscriptionPolicy(ctx, dbgen.UpdateSubscriptionPolicyParams{
			UpgradesEnabled:          policy.UpgradesEnabled,
			DowngradesEnabled:        policy.DowngradesEnabled,
			ResetTrafficOnRenewal:    policy.ResetTrafficOnRenewal,
			ResetTrafficOnPlanChange: policy.ResetTrafficOnPlanChange,
			ResetDevicesOnRenewal:    policy.ResetDevicesOnRenewal,
			TrialRemainder:           policy.TrialRemainder,
			UpdatedBy:                optionalUUID(actor.AdminID),
		})
		if err != nil {
			return notFound(err)
		}
		saved = subscriptionPolicyFrom(row)
		return appendAudit(ctx, queries, actor.audit(
			"panel.settings.subscription_policy.updated", "configuration", "settings", "subscription_policy",
			map[string]any{
				"upgradesEnabled": policy.UpgradesEnabled, "downgradesEnabled": policy.DowngradesEnabled,
				"resetTrafficOnRenewal":    policy.ResetTrafficOnRenewal,
				"resetTrafficOnPlanChange": policy.ResetTrafficOnPlanChange,
				"resetDevicesOnRenewal":    policy.ResetDevicesOnRenewal,
				"trialRemainder":           policy.TrialRemainder,
			},
		))
	})
	return saved, err
}

func subscriptionPolicyFrom(row dbgen.SubscriptionPolicy) SubscriptionPolicy {
	return SubscriptionPolicy{
		UpgradesEnabled:          row.UpgradesEnabled,
		DowngradesEnabled:        row.DowngradesEnabled,
		ResetTrafficOnRenewal:    row.ResetTrafficOnRenewal,
		ResetTrafficOnPlanChange: row.ResetTrafficOnPlanChange,
		ResetDevicesOnRenewal:    row.ResetDevicesOnRenewal,
		TrialRemainder:           row.TrialRemainder,
		UpdatedAt:                timeValue(row.UpdatedAt),
	}
}
