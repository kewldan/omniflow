package panelpg

import "context"

// NavigationCounts are the numbers beside the panel's menu entries: what is
// waiting on an operator in each area. Only areas the operator may read are
// counted; the caller passes which.
type NavigationCounts map[string]int64

// navigationCountQueries count, per menu entry, the records an operator has
// to act on. Each is one indexed count; none walks history.
var navigationCountQueries = map[string]string{
	// Tickets a customer is waiting on.
	"support": `SELECT count(*) FROM support_tickets WHERE status = 'open' AND merged_into_ticket_id IS NULL`,
	// Shop orders parked for a decision about the operator's money.
	"shop": `SELECT count(*) FROM goods_orders WHERE status = 'needs_review'`,
	// Partner withdrawals waiting for approval.
	"payouts": `SELECT count(*) FROM referral_withdrawals WHERE status = 'pending'`,
	// A previous bot's payments that could not be credited on their own.
	"finance": `SELECT count(*) FROM bedolaga_payment_claims WHERE status = 'needs_review'`,
	// Open anomaly signals and blocklist matches.
	"risk": `SELECT (SELECT count(*) FROM anomaly_signals WHERE status = 'open')
		+ (SELECT count(*) FROM blocklist_matches WHERE status IN ('open', 'appealed'))`,
	// Application errors nobody has resolved.
	"system": `SELECT count(*) FROM app_errors WHERE resolved_at IS NULL`,
}

// NavigationCounts counts the entries named in areas; an area it does not
// know is skipped.
func (service *Service) NavigationCounts(ctx context.Context, areas []string) (NavigationCounts, error) {
	counts := NavigationCounts{}
	for _, area := range areas {
		statement, known := navigationCountQueries[area]
		if !known {
			continue
		}
		var count int64
		if err := service.pool.QueryRow(ctx, statement).Scan(&count); err != nil {
			return nil, err
		}
		counts[area] = count
	}
	return counts, nil
}
