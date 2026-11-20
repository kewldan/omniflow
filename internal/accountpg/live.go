package accountpg

import (
	"context"
	"fmt"
)

// LiveVersions reads one value per area of a customer's own panel that
// changes whenever something in it does. The customer panel's live stream
// sends the areas whose value moved, and the open screens re-read their data
// through the routes they already use — so the stream itself never carries a
// record, and nothing it says is about anybody but the signed-in customer.
//
//   - support: the newest message on the customer's tickets, how many have
//     been withdrawn, and when a ticket last changed (a status, a merge).
//   - orders: when one of the customer's orders or its payments last changed.
//   - subscriptions: when a subscription or one of its entitlements last
//     changed — provisioning finished, a renewal, a pause.
//   - wallet: the newest wallet entry.
func (service *Service) LiveVersions(ctx context.Context, customerID string) (map[string]string, error) {
	var (
		messageID, withdrawn                             int64
		tickets, orders, payments, subscriptions, wallet int64
		entitlements                                     int64
	)
	err := service.pool.QueryRow(ctx, `SELECT
		COALESCE((SELECT max(m.id) FROM support_messages m
			JOIN support_tickets t ON t.id = m.ticket_id WHERE t.user_id = $1::uuid), 0),
		COALESCE((SELECT count(*) FROM support_messages m
			JOIN support_tickets t ON t.id = m.ticket_id
			WHERE t.user_id = $1::uuid AND m.deleted_at IS NOT NULL), 0),
		COALESCE((SELECT (extract(epoch FROM max(updated_at)) * 1000)::bigint
			FROM support_tickets WHERE user_id = $1::uuid), 0),
		COALESCE((SELECT (extract(epoch FROM max(updated_at)) * 1000)::bigint
			FROM orders WHERE user_id = $1::uuid), 0),
		COALESCE((SELECT (extract(epoch FROM max(p.updated_at)) * 1000)::bigint
			FROM payment_intents p JOIN orders o ON o.id = p.order_id WHERE o.user_id = $1::uuid), 0),
		COALESCE((SELECT (extract(epoch FROM max(updated_at)) * 1000)::bigint
			FROM subscriptions WHERE user_id = $1::uuid), 0),
		COALESCE((SELECT (extract(epoch FROM max(updated_at)) * 1000)::bigint
			FROM entitlements WHERE user_id = $1::uuid), 0),
		COALESCE((SELECT (extract(epoch FROM max(created_at)) * 1000)::bigint
			FROM ledger_entries WHERE user_id = $1::uuid AND account_type = 'customer_wallet'), 0)`,
		customerID).Scan(&messageID, &withdrawn, &tickets, &orders, &payments, &subscriptions, &entitlements, &wallet)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"support":       fmt.Sprintf("%d.%d.%d", messageID, withdrawn, tickets),
		"orders":        fmt.Sprintf("%d.%d", orders, payments),
		"subscriptions": fmt.Sprintf("%d.%d", subscriptions, entitlements),
		"wallet":        fmt.Sprintf("%d", wallet),
	}, nil
}
