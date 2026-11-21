package panelpg

import (
	"context"
	"strconv"
	"time"

	"github.com/omniflow/omniflow/internal/audience"
)

// MaxAudiencePreviewIDs bounds the customer IDs a preview returns: enough to
// feed one bulk action (MaxBulkTargets), small enough to answer at once.
const MaxAudiencePreviewIDs = MaxBulkTargets

// AudiencePreview is what a filter set selects right now, without saving it.
type AudiencePreview struct {
	Explain    []string `json:"explain"`
	Size       int64    `json:"size"`
	ChatClosed int64    `json:"chatClosed"`
	// CustomerIDs are the first matching customers in identifier order, at
	// most MaxAudiencePreviewIDs; Truncated says there were more.
	CustomerIDs []string `json:"customerIds"`
	Truncated   bool     `json:"truncated"`
}

// PreviewAudience compiles a filter set and counts it the way a saved segment
// is counted, and lists the customers it selects — the input an automation
// needs to estimate a broadcast or target a bulk action.
func (service *Service) PreviewAudience(ctx context.Context, filters map[string]any) (AudiencePreview, error) {
	if filters == nil {
		filters = map[string]any{}
	}
	query, err := audience.Compile(filters, time.Now().UTC())
	if err != nil {
		return AudiencePreview{}, ErrValidaton
	}
	preview := AudiencePreview{Explain: query.Explain, CustomerIDs: []string{}}
	if err = service.pool.QueryRow(ctx,
		`SELECT count(*), count(*) FILTER (WHERE EXISTS (SELECT 1 FROM bot_delivery_state d
			WHERE d.user_id = u.id AND d.status IN ('blocked', 'deactivated', 'not_started')))
		FROM users u WHERE u.status = 'active' AND (`+query.Where+")",
		query.Args...,
	).Scan(&preview.Size, &preview.ChatClosed); err != nil {
		return AudiencePreview{}, err
	}
	rows, err := service.pool.Query(ctx,
		"SELECT u.id::text FROM users u WHERE u.status = 'active' AND ("+query.Where+") ORDER BY u.id LIMIT "+
			strconv.Itoa(MaxAudiencePreviewIDs), query.Args...)
	if err != nil {
		return AudiencePreview{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return AudiencePreview{}, err
		}
		preview.CustomerIDs = append(preview.CustomerIDs, id)
	}
	preview.Truncated = preview.Size > int64(len(preview.CustomerIDs))
	return preview, rows.Err()
}

// CustomerPayments is one customer's payments: in the period, from Omniflow
// and from a previous bot apart, and the first and last ever, which is what a
// cohort or a repeat-payment figure is computed from.
type CustomerPayments struct {
	CustomerID        string    `json:"customerId"`
	Currency          string    `json:"currency"`
	Payments          int64     `json:"payments"`
	AmountMinor       int64     `json:"amountMinor"`
	LegacyPayments    int64     `json:"legacyPayments"`
	LegacyAmountMinor int64     `json:"legacyAmountMinor"`
	FirstPaidAt       time.Time `json:"firstPaidAt"`
	LastPaidAt        time.Time `json:"lastPaidAt"`
}

// MaxCustomerPaymentRows bounds one export.
const MaxCustomerPaymentRows = 100000

// CustomerPaymentsReport lists every customer who paid in the period. A
// payment is a provider-settled order for a non-zero amount, or a VPN purchase
// a previous bot recorded, as in the returning-payers figure.
func (service *Service) CustomerPaymentsReport(
	ctx context.Context, since, until time.Time, currency string,
) ([]CustomerPayments, error) {
	if !until.After(since) || until.Sub(since) > MaxSalesReportWindow {
		return nil, ErrValidaton
	}
	rows, err := service.pool.Query(ctx, `WITH payments AS (
			SELECT o.user_id, o.currency, o.paid_minor AS amount, o.paid_at AS at, false AS legacy
			FROM orders o WHERE o.paid_at IS NOT NULL AND o.paid_minor > 0
			UNION ALL
			SELECT lp.user_id, lp.currency, lp.amount_minor, lp.paid_at, true FROM legacy_payments lp
		), scoped AS (
			SELECT * FROM payments WHERE ($3 = '' OR currency = $3)
		)
		SELECT user_id::text, currency,
			count(*) FILTER (WHERE NOT legacy AND at >= $1 AND at < $2),
			COALESCE(sum(amount) FILTER (WHERE NOT legacy AND at >= $1 AND at < $2), 0)::bigint,
			count(*) FILTER (WHERE legacy AND at >= $1 AND at < $2),
			COALESCE(sum(amount) FILTER (WHERE legacy AND at >= $1 AND at < $2), 0)::bigint,
			min(at), max(at)
		FROM scoped
		GROUP BY user_id, currency
		HAVING bool_or(at >= $1 AND at < $2)
		ORDER BY user_id, currency
		LIMIT $4`, since.UTC(), until.UTC(), currency, MaxCustomerPaymentRows)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CustomerPayments{}
	for rows.Next() {
		var row CustomerPayments
		if err := rows.Scan(&row.CustomerID, &row.Currency, &row.Payments, &row.AmountMinor,
			&row.LegacyPayments, &row.LegacyAmountMinor, &row.FirstPaidAt, &row.LastPaidAt); err != nil {
			return nil, err
		}
		row.FirstPaidAt, row.LastPaidAt = row.FirstPaidAt.UTC(), row.LastPaidAt.UTC()
		out = append(out, row)
	}
	return out, rows.Err()
}
