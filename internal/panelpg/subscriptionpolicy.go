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
	UpgradesEnabled          bool   `json:"upgradesEnabled"`
	DowngradesEnabled        bool   `json:"downgradesEnabled"`
	ResetTrafficOnRenewal    bool   `json:"resetTrafficOnRenewal"`
	ResetTrafficOnPlanChange bool   `json:"resetTrafficOnPlanChange"`
	ResetDevicesOnRenewal    bool   `json:"resetDevicesOnRenewal"`
	TrialRemainder           string `json:"trialRemainder"`
	// TrialTag and PaidTag are the Remnawave user tags a subscription carries
	// when its plan names none: one on a trial, the other once paid. Both
	// empty leaves the panel's tags alone.
	TrialTag  string    `json:"trialTag"`
	PaidTag   string    `json:"paidTag"`
	UpdatedAt time.Time `json:"updatedAt"`
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

// SaveSubscriptionPolicy stores the switches. Orders priced after the save
// follow them, and so does every settlement, which reads them again.
func (service *Service) SaveSubscriptionPolicy(
	ctx context.Context, policy SubscriptionPolicy, actor Actor,
) (SubscriptionPolicy, error) {
	if !validTrialRemainder[policy.TrialRemainder] {
		return SubscriptionPolicy{}, ErrValidaton
	}
	trialTag, err := normalizePanelTag(policy.TrialTag)
	if err != nil {
		return SubscriptionPolicy{}, err
	}
	paidTag, err := normalizePanelTag(policy.PaidTag)
	if err != nil {
		return SubscriptionPolicy{}, err
	}
	var saved SubscriptionPolicy
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		if _, txErr := queries.UpdateSubscriptionPolicy(ctx, dbgen.UpdateSubscriptionPolicyParams{
			UpgradesEnabled:          policy.UpgradesEnabled,
			DowngradesEnabled:        policy.DowngradesEnabled,
			ResetTrafficOnRenewal:    policy.ResetTrafficOnRenewal,
			ResetTrafficOnPlanChange: policy.ResetTrafficOnPlanChange,
			ResetDevicesOnRenewal:    policy.ResetDevicesOnRenewal,
			TrialRemainder:           policy.TrialRemainder,
			UpdatedBy:                optionalUUID(actor.AdminID),
		}); txErr != nil {
			return notFound(txErr)
		}
		row, txErr := queries.SetPolicyTags(ctx, dbgen.SetPolicyTagsParams{TrialTag: trialTag, PaidTag: paidTag})
		if txErr != nil {
			return txErr
		}
		saved = subscriptionPolicyFrom(row)
		return appendAudit(ctx, queries, actor.audit(
			"panel.settings.subscription_policy.updated", "configuration", "settings", "subscription_policy",
			map[string]any{"policy": saved},
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
		TrialTag:                 row.TrialTag.String,
		PaidTag:                  row.PaidTag.String,
		UpdatedAt:                timeValue(row.UpdatedAt),
	}
}
