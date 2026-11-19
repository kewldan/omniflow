package accountcheckout

import (
	"context"

	"github.com/omniflow/omniflow/internal/commercepg"
)

// LoyaltyChoices leaves out the payment methods the customer's loyalty group
// does not allow. Both surfaces filter through it; the payment service refuses
// a restricted method at intent creation whatever a screen offered.
func LoyaltyChoices(ctx context.Context, orders *commercepg.Store, customerID string, choices []PaymentChoice) ([]PaymentChoice, error) {
	tier, held, err := orders.LoyaltyTier(ctx, customerID)
	if err != nil || !held {
		return choices, err
	}
	kept := make([]PaymentChoice, 0, len(choices))
	for _, choice := range choices {
		if tier.AllowsProvider(choice.Provider) {
			kept = append(kept, choice)
		}
	}
	return kept, nil
}
