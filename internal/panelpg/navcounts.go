package panelpg

import "context"

// NavigationCounts are the numbers beside the panel's menu entries: what is
// waiting on an operator in each area. Only areas the operator may read are
// counted; the caller passes which.
type NavigationCounts map[string]int64

// navigationCountQueries count, per menu entry, the records an operator has
// to act on. Each is one indexed count; none walks history.
var navigationCountQueries = map[string]string{
	// Tickets a customer is waiting on that are this operator's to answer:
	// theirs, or nobody's yet. A reply moves a ticket to pending, so an
	// answered ticket leaves the count until the customer writes again.
	"support": `SELECT count(*) FROM support_tickets
		WHERE status = 'open' AND merged_into_ticket_id IS NULL
		  AND (assignee_id IS NULL OR assignee_id = $1::uuid)`,
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

// navigationCountsForOperator are the counts that depend on who is asking;
// they take the operator's identifier as $1.
var navigationCountsForOperator = map[string]bool{"support": true}

// NavigationCounts counts the entries named in areas for one operator; an area
// it does not know is skipped.
func (service *Service) NavigationCounts(
	ctx context.Context, areas []string, operatorID string,
) (NavigationCounts, error) {
	counts := NavigationCounts{}
	for _, area := range areas {
		statement, known := navigationCountQueries[area]
		if !known {
			continue
		}
		var arguments []any
		if navigationCountsForOperator[area] {
			operator, err := parseUUID(operatorID)
			if err != nil {
				return nil, ErrValidaton
			}
			arguments = append(arguments, operator)
		}
		var count int64
		if err := service.pool.QueryRow(ctx, statement, arguments...).Scan(&count); err != nil {
			return nil, err
		}
		counts[area] = count
	}
	return counts, nil
}
