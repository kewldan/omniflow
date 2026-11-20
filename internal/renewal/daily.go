package renewal

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// Daily plans are charged one day at a time from the wallet (Bedolaga's
// daily subscriptions). Every pass looks at each subscription whose current
// day is about to end, or ended not long ago, and buys the next day:
//
//   - Auto-renew never renews a daily plan (ListAutoRenewDue leaves daily
//     versions out), so the two cannot both buy the same day.
//   - The order is an ordinary wallet-funded renewal of the plan version,
//     created and settled through commercepg, so the day is priced, posted
//     to the ledger, and provisioned exactly as any other renewal — its
//     traffic choice included.
//   - Its idempotency key is daily:<subscription>:<date the day starts>, so
//     the orders' unique key makes one order per subscription and day however
//     many passes or worker replicas reach it.
//   - It is created only when the wallet covers it in full (WalletOnly). A
//     day the wallet does not cover is not ordered at all, rather than left
//     pending with the balance reserved: access ends with the paid day,
//     which is the suspension, and the customer is told once. A suspended
//     subscription is picked up again for DailyResumeWindow, so a top-up
//     resumes it on the next pass.
//   - A paused subscription has no running day, so nothing is charged until
//     the customer resumes it.

// DailyLead is how long before a paid day ends the next one is bought, so a
// subscription in good standing never drops between two days.
const DailyLead = 10 * time.Minute

// DailyResumeWindow is how long after a day the wallet did not cover the
// subscription still resumes on its own once the wallet covers the next one.
// Past it, the customer buys the plan again.
const DailyResumeWindow = 30 * 24 * time.Hour

// dailyCandidate is one daily subscription whose day is ending or has ended.
type dailyCandidate struct {
	userID, subscriptionID, planVersionID, currency string
	endsAt                                          time.Time
}

// RunDaily charges every daily subscription that needs its next day.
func (worker *Worker) RunDaily(ctx context.Context) {
	candidates, err := worker.dailyCandidates(ctx)
	if err != nil {
		worker.logger.Error("daily charge candidates failed", "error", err)
		return
	}
	for _, candidate := range candidates {
		if ctx.Err() != nil {
			return
		}
		if err := worker.chargeDay(ctx, candidate); err != nil {
			worker.logger.Error("daily charge failed", "subscriptionId", candidate.subscriptionID, "error", err)
		}
	}
}

// dailyCandidates lists daily subscriptions whose latest entitlement ends
// within the lead time or ended inside the resume window. A paused
// subscription, a closed one, and an account that is not active are left
// alone, and so is a subscription a later entitlement already covers — a
// plan change, or a day already bought.
func (worker *Worker) dailyCandidates(ctx context.Context) ([]dailyCandidate, error) {
	// The day is bought at the plan's newest live daily version, so a price
	// the operator publishes applies from the next day; a plan with none
	// left keeps charging the version the subscription holds.
	rows, err := worker.pool.Query(ctx, `SELECT e.user_id::text, e.subscription_id::text,
			COALESCE((SELECT nv.id FROM plan_versions nv
				WHERE nv.plan_id = v.plan_id AND nv.retired_at IS NULL AND nv.daily_billing
				ORDER BY nv.version DESC LIMIT 1), v.id)::text,
			o.currency, e.ends_at
		FROM entitlements e
		JOIN plan_versions v ON v.id = e.plan_version_id AND v.daily_billing
		JOIN orders o ON o.id = e.order_id
		JOIN subscriptions s ON s.id = e.subscription_id AND s.status = 'active'
		JOIN users u ON u.id = e.user_id AND u.status = 'active'
		WHERE e.status IN ('pending', 'active', 'limited', 'expired')
		  AND e.ends_at <= now() + make_interval(secs => $1)
		  AND e.ends_at > now() - make_interval(secs => $2)
		  AND NOT EXISTS (
			SELECT 1 FROM entitlements later
			WHERE later.subscription_id = e.subscription_id AND later.id <> e.id
			  AND later.status NOT IN ('superseded', 'failed')
			  AND (later.ends_at > e.ends_at OR later.status = 'paused')
		  )
		ORDER BY e.ends_at
		LIMIT $3`, int64(DailyLead/time.Second), int64(DailyResumeWindow/time.Second), batchSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	candidates := make([]dailyCandidate, 0, 16)
	for rows.Next() {
		var (
			candidate dailyCandidate
			endsAt    pgtype.Timestamptz
		)
		if err = rows.Scan(&candidate.userID, &candidate.subscriptionID, &candidate.planVersionID, &candidate.currency, &endsAt); err != nil {
			return nil, err
		}
		candidate.endsAt = endsAt.Time.UTC()
		candidates = append(candidates, candidate)
	}
	return candidates, rows.Err()
}

func parseUUID(value string) (pgtype.UUID, error) {
	var id pgtype.UUID
	err := id.Scan(value)
	return id, err
}

// dailyKey is the one key the day starting at `start` is charged under.
func dailyKey(subscriptionID string, start time.Time) string {
	return "daily:" + subscriptionID + ":" + start.UTC().Format(time.DateOnly)
}

// chargeDay buys one subscription's next day, or records why it could not.
func (worker *Worker) chargeDay(ctx context.Context, candidate dailyCandidate) error {
	now := worker.clock().UTC()
	// A day bought after the last one ended starts now; the key names the
	// day that starts, which is what makes a resumed day a new key.
	start := candidate.endsAt
	if start.Before(now) {
		start = now
	}
	key := dailyKey(candidate.subscriptionID, start)
	userID, err := parseUUID(candidate.userID)
	if err != nil {
		return err
	}
	if _, err = dbgen.New(worker.pool).GetOrderByIdempotency(ctx, dbgen.GetOrderByIdempotencyParams{
		UserID: userID, IdempotencyKey: key,
	}); err == nil {
		return nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	_, trafficGB, err := worker.orders.RenewalSelection(ctx, candidate.subscriptionID, candidate.planVersionID, candidate.currency)
	if err != nil {
		return err
	}
	squads, err := worker.orders.RenewalSquads(ctx, candidate.subscriptionID, candidate.planVersionID)
	if err != nil {
		return err
	}
	input := commercepg.CreateOrderInput{
		CustomerID: candidate.userID, PlanVersionID: candidate.planVersionID, Currency: candidate.currency,
		Operation: "renewal", SubscriptionID: candidate.subscriptionID, IdempotencyKey: key,
		TrafficGB: trafficGB, SelectedSquadIDs: squads, WalletOnly: true,
	}
	order, err := worker.orders.CreateOrder(ctx, input)
	if errors.Is(err, commercepg.ErrWalletShort) {
		// Not charged. While the paid day still runs, the next pass tries
		// again; once it has ended, access is suspended and the customer is
		// told once for that day.
		if candidate.endsAt.After(now) {
			return nil
		}
		return worker.dailyNotice(ctx, candidate, "daily_suspended",
			"daily-suspended:"+candidate.subscriptionID+":"+candidate.endsAt.Format(time.DateOnly), input)
	}
	if err != nil {
		return err
	}
	worker.logger.Info("daily plan charged", "subscriptionId", candidate.subscriptionID, "orderId", uuidString(order.ID))
	// Warn a day ahead when the balance left will not cover the next day.
	next := input
	next.IdempotencyKey = "daily-preview:" + key
	next.WalletOnly = false
	quote, err := worker.orders.PreviewOrder(ctx, next)
	if err != nil || quote.ExternalMinor <= 0 {
		return nil
	}
	return worker.dailyNotice(ctx, candidate, "daily_low_balance", "daily-low:"+key, input)
}

// dailyNotice queues one customer event notice about a daily plan, priced as
// the next day would be, under a key that makes it once per day.
func (worker *Worker) dailyNotice(ctx context.Context, candidate dailyCandidate, kind, dedupe string, input commercepg.CreateOrderInput) error {
	input.IdempotencyKey = "daily-notice:" + dedupe
	input.WalletOnly = false
	quote, err := worker.orders.PreviewOrder(ctx, input)
	if err != nil {
		return err
	}
	params, err := json.Marshal(map[string]any{
		"amountMinor": quote.ExternalMinor, "priceMinor": quote.TotalMinor(), "currency": candidate.currency,
	})
	if err != nil {
		return err
	}
	_, err = worker.pool.Exec(ctx, `INSERT INTO customer_event_notices (user_id, kind, params, dedupe_key)
		VALUES ($1::uuid, $2, $3, $4)
		ON CONFLICT (dedupe_key) DO NOTHING`, candidate.userID, kind, params, dedupe)
	return err
}
