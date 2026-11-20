package panelpg

import "context"

// LiveVersions reads one number per area that changes whenever something in
// it does: the newest ticket message, the newest paid order, the error log's
// latest occurrence, and the newest customer. The panel's live stream sends
// the areas whose number moved; a screen then re-reads its own data.
func (service *Service) LiveVersions(ctx context.Context) (map[string]int64, error) {
	var support, finance, errors, customers int64
	err := service.pool.QueryRow(ctx, `SELECT
		COALESCE((SELECT max(id) FROM support_messages), 0),
		COALESCE((SELECT extract(epoch FROM max(paid_at))::bigint FROM orders), 0),
		COALESCE((SELECT extract(epoch FROM max(last_seen_at))::bigint FROM app_errors), 0)
		  + COALESCE((SELECT sum(occurrences) FROM app_errors WHERE resolved_at IS NULL), 0)::bigint,
		COALESCE((SELECT extract(epoch FROM max(created_at))::bigint FROM users), 0)`).
		Scan(&support, &finance, &errors, &customers)
	return map[string]int64{"support": support, "finance": finance, "errors": errors, "customers": customers}, err
}
