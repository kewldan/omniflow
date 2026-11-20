// Package blockedbot switches off the subscriptions of customers who blocked
// the bot, and switches them back on when they return — Bedolaga's option for
// installations that sell access only to reachable customers.
//
// It runs only when Settings → Marketing → disableBlockedSubscriptions is on.
// Every change goes through the ordinary fulfillment pipeline with its own
// idempotency key, and each disable is recorded in blocked_bot_disables, so
// a restore enables exactly what this pass disabled and nothing an operator
// disabled by hand.
package blockedbot

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/fulfillment"
)

// Enqueuer is the fulfillment pipeline.
type Enqueuer interface {
	Enqueue(ctx context.Context, entitlementID string, input fulfillment.OperationInput) error
}

// FulfillmentEnqueuer adapts the fulfillment service.
type FulfillmentEnqueuer struct{ Service *fulfillment.Service }

// Enqueue queues one operation.
func (adapter FulfillmentEnqueuer) Enqueue(ctx context.Context, entitlementID string, input fulfillment.OperationInput) error {
	_, err := adapter.Service.Enqueue(ctx, entitlementID, input)
	return err
}

// Sweeper is the periodic pass.
type Sweeper struct {
	pool     *pgxpool.Pool
	enqueuer Enqueuer
	logger   *slog.Logger
}

// New builds the pass.
func New(pool *pgxpool.Pool, enqueuer Enqueuer, logger *slog.Logger) *Sweeper {
	if logger == nil {
		logger = slog.Default()
	}
	return &Sweeper{pool: pool, enqueuer: enqueuer, logger: logger}
}

// batch bounds one pass.
const batch = 200

// Run disables what is due and restores what came back.
func (sweeper *Sweeper) Run(ctx context.Context) error {
	var enabled bool
	var graceHours int
	err := sweeper.pool.QueryRow(ctx, `SELECT
			COALESCE((document->>'disableBlockedSubscriptions')::boolean, false),
			COALESCE(NULLIF(document->>'blockedGraceHours', '')::int, 24)
		FROM installation_settings WHERE section = 'marketing'`).Scan(&enabled, &graceHours)
	if err != nil {
		return err
	}
	if err := sweeper.restore(ctx); err != nil {
		return err
	}
	if !enabled {
		return nil
	}
	rows, err := sweeper.pool.Query(ctx, `SELECT e.id::text, e.user_id::text FROM entitlements e
		JOIN bot_delivery_state d ON d.user_id = e.user_id
		WHERE d.status = 'blocked' AND d.updated_at < now() - make_interval(hours => $1)
		  AND e.status IN ('active', 'limited') AND e.ends_at > now()
		  AND NOT EXISTS (SELECT 1 FROM blocked_bot_disables b WHERE b.entitlement_id = e.id AND b.restored_at IS NULL)
		ORDER BY e.id LIMIT $2`, max(graceHours, 0), batch)
	if err != nil {
		return err
	}
	type target struct{ entitlement, user string }
	targets := []target{}
	for rows.Next() {
		var item target
		if err := rows.Scan(&item.entitlement, &item.user); err != nil {
			rows.Close()
			return err
		}
		targets = append(targets, item)
	}
	rows.Close()
	for _, item := range targets {
		var disabledAt string
		if err := sweeper.pool.QueryRow(ctx, `INSERT INTO blocked_bot_disables (entitlement_id, user_id)
			VALUES ($1::uuid, $2::uuid)
			ON CONFLICT (entitlement_id) DO UPDATE SET disabled_at = now(), restored_at = NULL
			RETURNING disabled_at::text`, item.entitlement, item.user).Scan(&disabledAt); err != nil {
			return err
		}
		key := "blocked-bot:disable:" + item.entitlement + ":" + disabledAt
		if err := sweeper.enqueuer.Enqueue(ctx, item.entitlement, fulfillment.OperationInput{
			Operation: "disable", IdempotencyKey: key, CorrelationID: key,
		}); err != nil {
			sweeper.logger.Warn("blocked customer's subscription could not be disabled", "error", err)
		}
	}
	return nil
}

// restore enables what this pass disabled for customers the bot can reach
// again.
func (sweeper *Sweeper) restore(ctx context.Context) error {
	rows, err := sweeper.pool.Query(ctx, `SELECT b.entitlement_id::text, b.disabled_at::text FROM blocked_bot_disables b
		LEFT JOIN bot_delivery_state d ON d.user_id = b.user_id
		WHERE b.restored_at IS NULL AND COALESCE(d.status, 'active') <> 'blocked'
		ORDER BY b.entitlement_id LIMIT $1`, batch)
	if err != nil {
		return err
	}
	type item struct{ entitlement, disabledAt string }
	items := []item{}
	for rows.Next() {
		var entry item
		if err := rows.Scan(&entry.entitlement, &entry.disabledAt); err != nil {
			rows.Close()
			return err
		}
		items = append(items, entry)
	}
	rows.Close()
	for _, entry := range items {
		key := "blocked-bot:enable:" + entry.entitlement + ":" + entry.disabledAt
		if err := sweeper.enqueuer.Enqueue(ctx, entry.entitlement, fulfillment.OperationInput{
			Operation: "enable", IdempotencyKey: key, CorrelationID: key,
		}); err != nil {
			sweeper.logger.Warn("returning customer's subscription could not be enabled", "error", err)
			continue
		}
		if _, err := sweeper.pool.Exec(ctx, `UPDATE blocked_bot_disables SET restored_at = now()
			WHERE entitlement_id = $1::uuid AND restored_at IS NULL`, entry.entitlement); err != nil {
			return err
		}
	}
	return nil
}
