package panelpg

import "context"

// RecordCustomerAction audits an operator action on a customer that another
// service performed, such as a grant or a purchase made on their behalf.
func (service *Service) RecordCustomerAction(
	ctx context.Context, action, category, customerID string, metadata map[string]any, actor Actor,
) error {
	return appendAudit(ctx, service.queries(), actor.audit(action, category, "customer", customerID, metadata))
}

// LatestPlanVersion resolves a plan an operator picked by name to its newest
// live version and the first currency it is priced in.
func (service *Service) LatestPlanVersion(ctx context.Context, planID string) (string, string, error) {
	id, err := parseUUID(planID)
	if err != nil {
		return "", "", err
	}
	row, err := service.queries().LatestLivePlanVersion(ctx, id)
	if err != nil {
		return "", "", notFound(err)
	}
	return uuidString(row.ID), row.Currency, nil
}
