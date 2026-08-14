package panelpg

import "context"

// CustomerRef is the little Omniflow knows about a Remnawave user: who it
// belongs to and what the customer calls that subscription.
//
// It exists so a traffic report can put a name beside a number. Consumption
// itself is never stored here — Remnawave owns traffic, and this repository has
// no column for a byte a customer used.
type CustomerRef struct {
	RemnawaveID int64  `json:"remnawaveId"`
	CustomerID  string `json:"customerId"`
	Label       string `json:"label"`
	Status      string `json:"status"`
}

// CustomersByRemnawaveIDs resolves Remnawave user identifiers to customers.
//
// Identifiers with no subscription behind them are simply absent from the
// result: a Remnawave user Omniflow did not create is a real state — an
// operator provisioned it directly, or an import has not run — and the report
// shows it as unattributed rather than inventing an owner.
func (service *Service) CustomersByRemnawaveIDs(
	ctx context.Context, ids []int64,
) (map[int64]CustomerRef, error) {
	if len(ids) == 0 {
		return map[int64]CustomerRef{}, nil
	}
	rows, err := service.queries().CustomersByRemnawaveIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	resolved := make(map[int64]CustomerRef, len(rows))
	for _, row := range rows {
		resolved[row.RemnawaveUserID.Int64] = CustomerRef{
			RemnawaveID: row.RemnawaveUserID.Int64,
			CustomerID:  uuidString(row.CustomerID),
			Label:       row.Label,
			Status:      row.CustomerStatus,
		}
	}
	return resolved, nil
}
