package renewal

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/recurring"
)

// DefaultLowBalanceWarning is how long before a wallet-funded renewal's first
// charge the customer is warned that the balance will not cover it.
const DefaultLowBalanceWarning = 48 * time.Hour

// warnLowBalance tells customers whose wallet-funded renewal is coming up
// that their balance will not cover it, once per renewal cycle.
//
// The renewal is priced exactly as the charge will be — the same order input,
// previewed against the current balance — so the warning names the real
// shortfall, discounts included. The notice goes through the customer event
// notices, which the bot delivers under the `payment` kind with the
// customer's own preferences and quiet hours.
func (worker *Worker) warnLowBalance(ctx context.Context) {
	if worker.config.LowBalanceWarning < 0 {
		return
	}
	window := worker.config.LowBalanceWarning
	if window == 0 {
		window = DefaultLowBalanceWarning
	}
	rows, err := worker.pool.Query(ctx, `SELECT a.user_id::text, COALESCE(a.subscription_id::text, ''),
			a.plan_version_id::text, a.currency,
			(e.id::text || ':' || extract(epoch FROM e.ends_at)::bigint::text) AS cycle_key,
			e.ends_at - make_interval(secs => a.lead_time_seconds) AS charge_at
		FROM auto_renew_settings a
		JOIN entitlements e ON e.subscription_id IS NOT DISTINCT FROM a.subscription_id
			AND e.user_id = a.user_id AND e.status IN ('active', 'limited')
		JOIN users u ON u.id = a.user_id AND u.status = 'active'
		WHERE a.enabled AND a.consent_at IS NOT NULL AND a.funding = $1 AND a.state = 'scheduled'
		  AND a.plan_version_id IS NOT NULL AND a.currency IS NOT NULL
		  AND e.ends_at - make_interval(secs => a.lead_time_seconds) > now()
		  AND e.ends_at - make_interval(secs => a.lead_time_seconds) <= now() + make_interval(secs => $2)
		  AND NOT EXISTS (
			SELECT 1 FROM customer_event_notices n
			WHERE n.dedupe_key = 'low-balance:' || e.id::text || ':' || extract(epoch FROM e.ends_at)::bigint::text
		  )
		LIMIT $3`, recurring.FundingWallet, int64(window/time.Second), batchSize)
	if err != nil {
		worker.logger.Error("low-balance candidates failed", "error", err)
		return
	}
	type candidate struct {
		userID, subscriptionID, planVersionID, currency, cycleKey string
		chargeAt                                                  time.Time
	}
	candidates := make([]candidate, 0, 16)
	for rows.Next() {
		var item candidate
		var chargeAt pgtype.Timestamptz
		if err := rows.Scan(&item.userID, &item.subscriptionID, &item.planVersionID, &item.currency, &item.cycleKey, &chargeAt); err != nil {
			rows.Close()
			worker.logger.Error("low-balance candidate scan failed", "error", err)
			return
		}
		item.chargeAt = chargeAt.Time
		candidates = append(candidates, item)
	}
	rows.Close()
	for _, item := range candidates {
		// Priced as the renewal will be: the same period and traffic.
		periodDays, trafficGB, err := worker.orders.RenewalSelection(ctx, item.subscriptionID, item.planVersionID, item.currency)
		if err != nil {
			continue
		}
		quote, err := worker.orders.PreviewOrder(ctx, commercepg.CreateOrderInput{
			CustomerID: item.userID, PlanVersionID: item.planVersionID, Currency: item.currency,
			Operation: "renewal", SubscriptionID: item.subscriptionID,
			IdempotencyKey: "low-balance-preview:" + item.cycleKey,
			PeriodDays:     periodDays, TrafficGB: trafficGB,
		})
		if err != nil || quote.ExternalMinor <= 0 {
			// Covered, or not priceable now — the charge itself will say so.
			continue
		}
		days := int(time.Until(item.chargeAt).Hours()/24 + 0.5)
		params, _ := json.Marshal(map[string]any{
			"amountMinor": quote.ExternalMinor, "currency": item.currency, "days": days,
		})
		if _, err := worker.pool.Exec(ctx, `INSERT INTO customer_event_notices (user_id, kind, params, dedupe_key)
			VALUES ($1::uuid, 'renewal_low_balance', $2, 'low-balance:' || $3)
			ON CONFLICT (dedupe_key) DO NOTHING`, item.userID, params, item.cycleKey); err != nil {
			worker.logger.Error("low-balance notice failed", "error", err)
		}
	}
}
