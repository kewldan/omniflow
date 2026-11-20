package accountcheckout

import (
	"context"

	"github.com/omniflow/omniflow/internal/commercepg"
)

// DailyBilling reads how one of the customer's subscriptions is billed when
// its plan is daily.
func (service *Service) DailyBilling(ctx context.Context, customerID, subscriptionID string) (commercepg.DailyStatus, error) {
	return service.orders.DailyBillingStatus(ctx, customerID, subscriptionID)
}

// SetDailyPause pauses or resumes one of the customer's daily subscriptions.
// The request's idempotency key names the operation it queues.
func (service *Service) SetDailyPause(ctx context.Context, customerID, subscriptionID string, pause bool, idempotencyKey string) (commercepg.DailyStatus, error) {
	if err := service.orders.SetDailyPause(ctx, customerID, subscriptionID, pause, "web:"+idempotencyKey); err != nil {
		return commercepg.DailyStatus{}, err
	}
	return service.orders.DailyBillingStatus(ctx, customerID, subscriptionID)
}
